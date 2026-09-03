package friend

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/Wriosley/supernova-classic-farm/server/internal/routing"
)

type TaskCreditor interface {
	Credit(context.Context, uint64) error
}

type HTTPTaskCreditor struct {
	Client         *http.Client
	CoordinatorURL string
}

func (c *HTTPTaskCreditor) Credit(ctx context.Context, playerID uint64) error {
	if playerID == 0 {
		return fmt.Errorf("player_id is required")
	}
	client := c.Client
	if client == nil {
		client = http.DefaultClient
	}
	route, err := routing.FetchRoute(ctx, client, c.CoordinatorURL, routing.ShardForPlayer(playerID))
	if err != nil {
		return err
	}
	endpoint := strings.TrimRight(route.OwnerEndpoint, "/") +
		"/internal/v1/players/" + strconv.FormatUint(playerID, 10) + "/friend-task-credit"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader([]byte(`{}`)))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Shard-ID", strconv.FormatUint(uint64(route.ShardID), 10))
	request.Header.Set("X-Owner-Zone-ID", route.OwnerZoneID)
	request.Header.Set("X-Owner-Epoch", strconv.FormatUint(route.OwnerEpoch, 10))
	request.Header.Set("X-Route-Version", strconv.FormatUint(route.RouteVersion, 10))
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("task credit returned %s", response.Status)
	}
	return nil
}
