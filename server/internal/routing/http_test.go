package routing

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRouteHTTPReturnsDecimalStrings(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	routes, err := NewLocalMap(now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHTTPHandler(routes, func() time.Time { return now })
	request := httptest.NewRequest(http.MethodGet, "/internal/v1/routes/42", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"owner_epoch", "route_version", "map_version", "lease_term"} {
		if _, ok := body[field].(string); !ok {
			t.Errorf("%s type = %T, want JSON string", field, body[field])
		}
	}
	if body["owner_zone_id"] != DefaultZoneID ||
		body["owner_endpoint"] != DefaultZoneEndpoint ||
		body["state"] != string(RouteStateActive) ||
		body["routable"] != true {
		t.Fatalf("unexpected response: %#v", body)
	}
}

func TestRouteHTTPSnapshotIsCompleteAndCounted(t *testing.T) {
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	routes, err := NewStaticMap(now, time.Minute, []ZoneCandidate{
		{ZoneID: "zone-a", Endpoint: "http://127.0.0.1:8082"},
		{ZoneID: "zone-b", Endpoint: "http://127.0.0.1:8084"},
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHTTPHandler(routes, func() time.Time { return now })
	response := httptest.NewRecorder()
	handler.ServeHTTP(response,
		httptest.NewRequest(http.MethodGet, "/internal/v1/routes", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var body struct {
		ShardCount                 uint32          `json:"shard_count"`
		HashAlgorithmVersion       uint32          `json:"hash_algorithm_version"`
		AssignmentAlgorithmVersion uint32          `json:"assignment_algorithm_version"`
		MapVersion                 string          `json:"map_version"`
		Entries                    []routeResponse `json:"entries"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.ShardCount != ShardCount ||
		body.HashAlgorithmVersion != HashAlgorithmVersion ||
		body.AssignmentAlgorithmVersion != AssignmentAlgorithmVersion ||
		body.MapVersion != "1" ||
		len(body.Entries) != int(ShardCount) {
		t.Fatalf("invalid snapshot metadata: shard_count=%d hash=%d assignment=%d map=%s entries=%d",
			body.ShardCount, body.HashAlgorithmVersion,
			body.AssignmentAlgorithmVersion, body.MapVersion, len(body.Entries))
	}
	for shardID, entry := range body.Entries {
		if entry.ShardID != uint32(shardID) || !entry.Routable {
			t.Fatalf("invalid snapshot entry %d: %+v", shardID, entry)
		}
	}

	statsResponse := httptest.NewRecorder()
	handler.ServeHTTP(statsResponse,
		httptest.NewRequest(http.MethodGet, "/internal/v1/debug/route-lookups", nil))
	var stats routeLookupStats
	if err := json.Unmarshal(statsResponse.Body.Bytes(), &stats); err != nil {
		t.Fatal(err)
	}
	if stats.Snapshot != 1 || stats.Shard != 0 {
		t.Fatalf("unexpected lookup stats: %+v", stats)
	}
}

func TestRouteHTTPRejectsExpiredLeaseAsNotOwner(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	routes, err := NewLocalMap(now, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHTTPHandler(routes, func() time.Time { return now.Add(time.Second) })
	request := httptest.NewRequest(http.MethodGet, "/internal/v1/routes/7", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var body struct {
		Routable bool `json:"routable"`
		Error    struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Routable || body.Error.Code != "NOT_OWNER" {
		t.Fatalf("unexpected response: %+v", body)
	}
}

func TestRouteHTTPRejectsInvalidShardID(t *testing.T) {
	now := time.Now().UTC()
	routes, err := NewLocalMap(now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHTTPHandler(routes, func() time.Time { return now })
	for _, path := range []string{
		"/internal/v1/routes/not-a-number",
		"/internal/v1/routes/4096",
	} {
		t.Run(path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, path, nil)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestRouteWatchTimesOutAndReturnsAdvancedSnapshot(t *testing.T) {
	now := time.Now().UTC()
	routes, err := NewLocalMap(now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(NewHTTPHandler(routes, time.Now))
	defer server.Close()

	timeoutResponse, err := server.Client().Get(
		server.URL + "/internal/v1/routes/watch?after_map_version=1&timeout_ms=10",
	)
	if err != nil {
		t.Fatal(err)
	}
	timeoutResponse.Body.Close()
	if timeoutResponse.StatusCode != http.StatusNoContent {
		t.Fatalf("timeout status = %d", timeoutResponse.StatusCode)
	}

	result := make(chan Snapshot, 1)
	errs := make(chan error, 1)
	go func() {
		snapshot, updated, watchErr := WatchSnapshot(
			t.Context(), server.Client(), server.URL, 1, time.Second,
		)
		if watchErr != nil {
			errs <- watchErr
			return
		}
		if !updated {
			errs <- errors.New("watch timed out before update")
			return
		}
		result <- snapshot
	}()
	if _, err := routes.RenewOwnedLeases(DefaultZoneID, time.Now(), time.Minute); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errs:
		t.Fatal(err)
	case snapshot := <-result:
		if snapshot.MapVersion != 2 || len(snapshot.Entries) != int(ShardCount) {
			t.Fatalf("watch snapshot = version %d entries %d",
				snapshot.MapVersion, len(snapshot.Entries))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watch did not observe map update")
	}
}
