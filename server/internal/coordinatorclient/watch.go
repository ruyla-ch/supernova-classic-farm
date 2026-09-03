package coordinatorclient

import (
	"context"
	"errors"
	"time"

	"github.com/Wriosley/supernova-classic-farm/server/internal/routing"
)

func (c *Client) watchLoop() {
	defer c.wg.Done()
	backoff := c.cfg.MinBackoff
	for {
		updated, err := c.watchOnce()
		if c.ctx.Err() != nil {
			return
		}
		if err == nil {
			backoff = c.cfg.MinBackoff
			if !updated {
				continue
			}
			continue
		}
		timer := time.NewTimer(backoff)
		select {
		case <-c.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if backoff < c.cfg.MaxBackoff {
			backoff *= 2
			if backoff > c.cfg.MaxBackoff {
				backoff = c.cfg.MaxBackoff
			}
		}
		if err := c.Refresh(c.ctx); err == nil {
			backoff = c.cfg.MinBackoff
		}
	}
}

func (c *Client) watchOnce() (bool, error) {
	after := c.MapVersion()
	ctx, cancel := context.WithTimeout(c.ctx, c.cfg.WatchTimeout+2*time.Second)
	defer cancel()
	snapshot, updated, err := routing.WatchSnapshot(
		ctx, c.cfg.Client, c.cfg.BaseURL, after, c.cfg.WatchTimeout,
	)
	if err != nil || !updated {
		return false, err
	}
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	err = c.publish(snapshot, true)
	if errors.Is(err, ErrStaleSnapshot) && c.MapVersion() >= snapshot.MapVersion {
		return false, nil
	}
	return err == nil, err
}
