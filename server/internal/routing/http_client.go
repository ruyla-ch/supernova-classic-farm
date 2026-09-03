package routing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// FetchSnapshot loads the Coordinator's complete committed ShardMap.
func FetchSnapshot(
	ctx context.Context,
	client *http.Client,
	baseURL string,
) (Snapshot, error) {
	return fetchSnapshot(ctx, client, strings.TrimRight(baseURL, "/")+"/internal/v1/routes", false)
}

// WatchSnapshot waits for a complete snapshot newer than afterMapVersion.
// The boolean result is false when the Coordinator returns a normal timeout.
func WatchSnapshot(
	ctx context.Context,
	client *http.Client,
	baseURL string,
	afterMapVersion uint64,
	timeout time.Duration,
) (Snapshot, bool, error) {
	if timeout <= 0 || timeout > 30*time.Second {
		return Snapshot{}, false, errors.New("watch timeout must be in (0,30s]")
	}
	endpoint := strings.TrimRight(baseURL, "/") + "/internal/v1/routes/watch?" +
		"after_map_version=" + strconv.FormatUint(afterMapVersion, 10) +
		"&timeout_ms=" + strconv.FormatInt(timeout.Milliseconds(), 10)
	snapshot, err := fetchSnapshot(ctx, client, endpoint, true)
	if errors.Is(err, errWatchTimeout) {
		return Snapshot{}, false, nil
	}
	if err != nil {
		return Snapshot{}, false, err
	}
	if snapshot.MapVersion <= afterMapVersion {
		return Snapshot{}, false, errors.New("route watch returned a stale snapshot")
	}
	return snapshot, true, nil
}

var errWatchTimeout = errors.New("route watch timed out")

func fetchSnapshot(
	ctx context.Context,
	client *http.Client,
	endpoint string,
	allowNoContent bool,
) (Snapshot, error) {
	if client == nil {
		client = http.DefaultClient
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Snapshot{}, err
	}
	response, err := client.Do(request)
	if err != nil {
		return Snapshot{}, err
	}
	defer response.Body.Close()
	if allowNoContent && response.StatusCode == http.StatusNoContent {
		return Snapshot{}, errWatchTimeout
	}
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return Snapshot{}, fmt.Errorf("route snapshot returned %s", response.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (8<<20)+1))
	if err != nil {
		return Snapshot{}, fmt.Errorf("read route snapshot: %w", err)
	}
	if len(raw) > 8<<20 {
		return Snapshot{}, errors.New("route snapshot exceeds 8 MiB")
	}
	var body snapshotResponse
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		return Snapshot{}, fmt.Errorf("decode route snapshot: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return Snapshot{}, err
	}
	mapVersion, err := parsePositiveVersion(body.MapVersion, "map_version")
	if err != nil {
		return Snapshot{}, err
	}
	term, err := parsePositiveVersion(body.CommittedTerm, "committed_term")
	if err != nil {
		return Snapshot{}, err
	}
	index, err := parsePositiveVersion(body.CommittedIndex, "committed_index")
	if err != nil {
		return Snapshot{}, err
	}
	snapshot := Snapshot{
		ShardCount:                 body.ShardCount,
		HashAlgorithmVersion:       body.HashAlgorithmVersion,
		AssignmentAlgorithmVersion: body.AssignmentAlgorithmVersion,
		MapVersion:                 mapVersion,
		CommittedTerm:              term,
		CommittedIndex:             index,
		Entries:                    make([]RouteEntry, len(body.Entries)),
	}
	for entryIndex, encoded := range body.Entries {
		entry, entryErr := decodeRouteResponse(encoded)
		entryMapVersion, mapErr := parsePositiveVersion(encoded.MapVersion, "entry map_version")
		if entryErr != nil {
			return Snapshot{}, fmt.Errorf("decode route %d: %w", entryIndex, entryErr)
		}
		if mapErr != nil || entry.ShardID != uint32(entryIndex) ||
			entryMapVersion != mapVersion {
			return Snapshot{}, fmt.Errorf("decode route %d: inconsistent snapshot metadata", entryIndex)
		}
		snapshot.Entries[entryIndex] = entry
	}
	if snapshot.ShardCount != ShardCount ||
		snapshot.HashAlgorithmVersion != HashAlgorithmVersion ||
		snapshot.AssignmentAlgorithmVersion != AssignmentAlgorithmVersion ||
		len(snapshot.Entries) != int(ShardCount) {
		return Snapshot{}, errors.New("route snapshot metadata is incompatible")
	}
	return snapshot, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("route snapshot contains trailing JSON")
		}
		return fmt.Errorf("decode trailing route snapshot data: %w", err)
	}
	return nil
}

func FetchRoute(
	ctx context.Context,
	client *http.Client,
	baseURL string,
	shardID uint32,
) (RouteEntry, error) {
	if client == nil {
		client = http.DefaultClient
	}
	endpoint := strings.TrimRight(baseURL, "/") +
		"/internal/v1/routes/" + strconv.FormatUint(uint64(shardID), 10)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return RouteEntry{}, err
	}
	response, err := client.Do(request)
	if err != nil {
		return RouteEntry{}, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 16<<10))
	if err != nil {
		return RouteEntry{}, err
	}
	if response.StatusCode != http.StatusOK {
		return RouteEntry{}, fmt.Errorf("route lookup returned %s", response.Status)
	}
	var encoded routeResponse
	if err := json.Unmarshal(body, &encoded); err != nil {
		return RouteEntry{}, fmt.Errorf("decode route: %w", err)
	}
	if !encoded.Routable {
		return RouteEntry{}, errors.New("route is not routable")
	}
	return decodeRouteResponse(encoded)
}

func decodeRouteResponse(encoded routeResponse) (RouteEntry, error) {
	epoch, err := parsePositiveVersion(encoded.OwnerEpoch, "owner_epoch")
	if err != nil {
		return RouteEntry{}, err
	}
	routeVersion, err := parsePositiveVersion(encoded.RouteVersion, "route_version")
	if err != nil {
		return RouteEntry{}, err
	}
	if encoded.ShardID >= ShardCount || encoded.UpdatedAtMS <= 0 {
		return RouteEntry{}, errors.New("route identity is invalid")
	}
	var leaseTerm uint64
	switch encoded.State {
	case RouteStateActive:
		leaseTerm, err = parsePositiveVersion(encoded.LeaseTerm, "lease_term")
		if err != nil || encoded.LeaseExpiresAtMS <= 0 {
			return RouteEntry{}, errors.New("ACTIVE route lease is invalid")
		}
		if encoded.OwnerZoneID == "" || encoded.OwnerEndpoint == "" ||
			encoded.LeaseID == "" {
			return RouteEntry{}, errors.New("ACTIVE route identity is invalid")
		}
	case RouteStatePreparing:
		leaseTerm, err = parsePositiveVersion(encoded.LeaseTerm, "lease_term")
		if err != nil || encoded.LeaseExpiresAtMS <= 0 ||
			encoded.OwnerZoneID == "" || encoded.OwnerEndpoint == "" ||
			encoded.TransitionID == "" {
			return RouteEntry{}, errors.New("PREPARING route identity is invalid")
		}
		if encoded.Routable {
			return RouteEntry{}, errors.New("inactive route cannot be routable")
		}
	case RouteStateUnassigned:
		if encoded.Routable {
			return RouteEntry{}, errors.New("inactive route cannot be routable")
		}
	default:
		return RouteEntry{}, errors.New("route state is invalid")
	}
	return RouteEntry{
		ShardID:             encoded.ShardID,
		OwnerZoneID:         encoded.OwnerZoneID,
		OwnerEndpoint:       encoded.OwnerEndpoint,
		OwnerEpoch:          epoch,
		RouteVersion:        routeVersion,
		State:               encoded.State,
		LeaseTerm:           leaseTerm,
		LeaseID:             encoded.LeaseID,
		LeaseExpiresAt:      time.UnixMilli(encoded.LeaseExpiresAtMS).UTC(),
		PreviousOwnerZoneID: encoded.PreviousOwnerZoneID,
		TransitionID:        encoded.TransitionID,
		UpdatedAt:           time.UnixMilli(encoded.UpdatedAtMS).UTC(),
	}, nil
}

func parsePositiveVersion(raw, field string) (uint64, error) {
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || value == 0 {
		return 0, fmt.Errorf("%s is invalid", field)
	}
	return value, nil
}
