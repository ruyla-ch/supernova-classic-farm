// Package coordinatorclient maintains a watched, immutable local copy of the
// loopback Coordinator's committed HTTP/JSON routing snapshot.
package coordinatorclient

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wriosley/supernova-classic-farm/server/internal/routing"
)

var (
	ErrRouteUnavailable = errors.New("coordinator route unavailable")
	ErrStaleSnapshot    = errors.New("coordinator snapshot is stale")
)

type Config struct {
	BaseURL      string
	Client       *http.Client
	WatchTimeout time.Duration
	MinBackoff   time.Duration
	MaxBackoff   time.Duration
	Now          func() time.Time
	OnSnapshot   func(routing.Snapshot) error
}

type Client struct {
	cfg      Config
	snapshot atomic.Pointer[routing.Snapshot]

	startMu sync.Mutex
	started bool
	closed  bool
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup

	refreshMu sync.Mutex
}

func New(cfg Config) (*Client, error) {
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if cfg.BaseURL == "" {
		return nil, errors.New("Coordinator base URL is required")
	}
	if err := validateLoopbackHTTPURL(cfg.BaseURL); err != nil {
		return nil, fmt.Errorf("Coordinator base URL: %w", err)
	}
	if cfg.Client == nil {
		cfg.Client = http.DefaultClient
	}
	if cfg.WatchTimeout == 0 {
		cfg.WatchTimeout = 25 * time.Second
	}
	if cfg.MinBackoff == 0 {
		cfg.MinBackoff = 100 * time.Millisecond
	}
	if cfg.MaxBackoff == 0 {
		cfg.MaxBackoff = 5 * time.Second
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.WatchTimeout <= 0 || cfg.WatchTimeout > 30*time.Second ||
		cfg.MinBackoff <= 0 || cfg.MaxBackoff < cfg.MinBackoff {
		return nil, errors.New("Coordinator client durations are invalid")
	}
	return &Client{cfg: cfg}, nil
}

// Start synchronously loads the initial snapshot before starting the watch.
func (c *Client) Start(parent context.Context) error {
	if parent == nil {
		return errors.New("parent context is required")
	}
	c.startMu.Lock()
	defer c.startMu.Unlock()
	if c.started {
		return errors.New("Coordinator client is already started")
	}
	if c.closed {
		return errors.New("Coordinator client is closed")
	}
	if err := c.Refresh(parent); err != nil {
		return err
	}
	c.ctx, c.cancel = context.WithCancel(parent)
	c.started = true
	c.wg.Add(1)
	go c.watchLoop()
	return nil
}

func (c *Client) ResolvePlayer(playerID uint64) (routing.RouteEntry, error) {
	if playerID == 0 {
		return routing.RouteEntry{}, ErrRouteUnavailable
	}
	return c.ResolveShard(routing.ShardForPlayer(playerID))
}

func (c *Client) ResolveShard(shardID uint32) (routing.RouteEntry, error) {
	if shardID >= routing.ShardCount {
		return routing.RouteEntry{}, ErrRouteUnavailable
	}
	current := c.snapshot.Load()
	if current == nil {
		return routing.RouteEntry{}, ErrRouteUnavailable
	}
	entry := current.Entries[shardID]
	if entry.State != routing.RouteStateActive ||
		!c.cfg.Now().UTC().Before(entry.LeaseExpiresAt) {
		return routing.RouteEntry{}, ErrRouteUnavailable
	}
	return entry, nil
}

func (c *Client) Snapshot() routing.Snapshot {
	current := c.snapshot.Load()
	if current == nil {
		return routing.Snapshot{}
	}
	return cloneSnapshot(*current)
}

func (c *Client) MapVersion() uint64 {
	current := c.snapshot.Load()
	if current == nil {
		return 0
	}
	return current.MapVersion
}

// Refresh synchronously fetches and publishes a complete snapshot.
func (c *Client) Refresh(ctx context.Context) error {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	snapshot, err := routing.FetchSnapshot(ctx, c.cfg.Client, c.cfg.BaseURL)
	if err != nil {
		return err
	}
	return c.publish(snapshot, false)
}

// ForceResync is the synchronous recovery path used after NOT_OWNER.
func (c *Client) ForceResync(ctx context.Context) error {
	return c.Refresh(ctx)
}

func (c *Client) Close() error {
	c.startMu.Lock()
	if c.closed {
		c.startMu.Unlock()
		return nil
	}
	c.closed = true
	if c.cancel != nil {
		c.cancel()
	}
	c.startMu.Unlock()
	c.wg.Wait()
	return nil
}

func (c *Client) publish(snapshot routing.Snapshot, requireAdvance bool) error {
	if err := validateSnapshot(snapshot); err != nil {
		return err
	}
	current := c.snapshot.Load()
	if current != nil {
		if snapshot.MapVersion < current.MapVersion ||
			(requireAdvance && snapshot.MapVersion <= current.MapVersion) {
			return ErrStaleSnapshot
		}
		if snapshot.MapVersion == current.MapVersion {
			return nil
		}
	}
	next := cloneSnapshot(snapshot)
	if c.cfg.OnSnapshot != nil {
		if err := c.cfg.OnSnapshot(cloneSnapshot(next)); err != nil {
			return fmt.Errorf("apply Coordinator snapshot callback: %w", err)
		}
	}
	c.snapshot.Store(&next)
	return nil
}

func validateSnapshot(snapshot routing.Snapshot) error {
	if snapshot.ShardCount != routing.ShardCount ||
		snapshot.HashAlgorithmVersion != routing.HashAlgorithmVersion ||
		snapshot.AssignmentAlgorithmVersion != routing.AssignmentAlgorithmVersion ||
		snapshot.MapVersion == 0 || snapshot.CommittedTerm == 0 ||
		snapshot.CommittedIndex == 0 ||
		len(snapshot.Entries) != int(routing.ShardCount) {
		return errors.New("Coordinator snapshot metadata is incompatible")
	}
	for index, entry := range snapshot.Entries {
		if entry.ShardID != uint32(index) || entry.OwnerEpoch == 0 ||
			entry.RouteVersion == 0 {
			return fmt.Errorf("Coordinator snapshot route %d is invalid", index)
		}
		switch entry.State {
		case routing.RouteStateActive:
			if entry.OwnerZoneID == "" || entry.OwnerEndpoint == "" ||
				entry.LeaseID == "" || entry.LeaseExpiresAt.IsZero() {
				return fmt.Errorf("Coordinator ACTIVE route %d is invalid", index)
			}
			if err := validateLoopbackHTTPURL(entry.OwnerEndpoint); err != nil {
				return fmt.Errorf("Coordinator route %d endpoint: %w", index, err)
			}
		case routing.RouteStatePreparing:
			if entry.OwnerZoneID == "" || entry.OwnerEndpoint == "" ||
				entry.LeaseID == "" || entry.LeaseExpiresAt.IsZero() ||
				entry.TransitionID == "" {
				return fmt.Errorf("Coordinator PREPARING route %d is invalid", index)
			}
			if err := validateLoopbackHTTPURL(entry.OwnerEndpoint); err != nil {
				return fmt.Errorf("Coordinator route %d endpoint: %w", index, err)
			}
		case routing.RouteStateUnassigned:
		default:
			return fmt.Errorf("Coordinator route %d has invalid state", index)
		}
	}
	return nil
}

func cloneSnapshot(snapshot routing.Snapshot) routing.Snapshot {
	snapshot.Entries = append([]routing.RouteEntry(nil), snapshot.Entries...)
	return snapshot
}

func validateLoopbackHTTPURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "http" || parsed.Hostname() == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("must be a plain loopback HTTP URL")
	}
	host := parsed.Hostname()
	ip := net.ParseIP(host)
	if !strings.EqualFold(host, "localhost") && (ip == nil || !ip.IsLoopback()) {
		return errors.New("must use a loopback host")
	}
	return nil
}
