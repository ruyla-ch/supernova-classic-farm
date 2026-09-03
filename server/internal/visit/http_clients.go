package visit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	zonev1 "github.com/Wriosley/supernova-classic-farm/server/gen/classicfarm/v1/zone"
	"github.com/Wriosley/supernova-classic-farm/server/internal/routing"
	"google.golang.org/protobuf/proto"
)

const maxMessageBytes = 64 << 10

type HTTPClients struct {
	Client    *http.Client
	Routes    RouteResolver
	FriendURL string
}

type RouteResolver interface {
	ResolvePlayer(uint64) (routing.RouteEntry, error)
	ForceResync(context.Context) error
}

func (c *HTTPClients) CheckMutual(
	ctx context.Context,
	firstPlayerID, secondPlayerID uint64,
) (bool, error) {
	request := &zonev1.CheckMutualFriendRequest{
		FirstPlayerId: firstPlayerID, SecondPlayerId: secondPlayerID,
	}
	response := &zonev1.CheckMutualFriendResponse{}
	endpoint := strings.TrimRight(c.FriendURL, "/") + "/internal/v1/friends/check"
	if err := c.postProto(ctx, endpoint, request, response, nil); err != nil {
		return false, err
	}
	return response.Mutual, nil
}

func (c *HTTPClients) EnterOwner(
	ctx context.Context,
	request *zonev1.EnterVisitorRequest,
) (*zonev1.EnterVisitorResponse, error) {
	response := &zonev1.EnterVisitorResponse{}
	err := c.postOwner(ctx, request.OwnerPlayerId, "/internal/v1/friend-visits/enter", request, response)
	return response, err
}

func (c *HTTPClients) HeartbeatOwner(
	ctx context.Context,
	request *zonev1.HeartbeatVisitorRequest,
) (*zonev1.HeartbeatVisitorResponse, error) {
	response := &zonev1.HeartbeatVisitorResponse{}
	err := c.postOwner(ctx, request.OwnerPlayerId, "/internal/v1/friend-visits/heartbeat", request, response)
	return response, err
}

func (c *HTTPClients) ExitOwner(
	ctx context.Context,
	request *zonev1.ExitVisitorRequest,
) (*zonev1.ExitVisitorResponse, error) {
	response := &zonev1.ExitVisitorResponse{}
	err := c.postOwner(ctx, request.OwnerPlayerId, "/internal/v1/friend-visits/exit", request, response)
	return response, err
}

func (c *HTTPClients) ApplySteal(
	ctx context.Context,
	request *zonev1.ApplyStealRequest,
) (*zonev1.ApplyStealResponse, error) {
	response := &zonev1.ApplyStealResponse{}
	err := c.postOwner(
		ctx, request.OwnerPlayerId,
		"/internal/v1/friend-visits/apply-steal", request, response,
	)
	return response, err
}

func (c *HTTPClients) ApplyPest(
	ctx context.Context,
	request *zonev1.ApplyPestRequest,
) (*zonev1.ApplyPestResponse, error) {
	response := &zonev1.ApplyPestResponse{}
	err := c.postOwner(
		ctx, request.OwnerPlayerId,
		"/internal/v1/friend-visits/apply-pest", request, response,
	)
	return response, err
}

func (c *HTTPClients) CatchPest(
	ctx context.Context,
	request *zonev1.CatchPestRequest,
) (*zonev1.CatchPestResponse, error) {
	response := &zonev1.CatchPestResponse{}
	err := c.postOwner(
		ctx, request.OwnerPlayerId,
		"/internal/v1/friend-visits/catch-pest", request, response,
	)
	return response, err
}

func (c *HTTPClients) postOwner(
	ctx context.Context,
	ownerPlayerID uint64,
	path string,
	request, response proto.Message,
) error {
	if ownerPlayerID == 0 {
		return errors.New("owner player ID is required")
	}
	if c.Routes == nil {
		return errors.New("owner route resolver is required")
	}
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		route, err := c.Routes.ResolvePlayer(ownerPlayerID)
		if err != nil {
			return err
		}
		headers := map[string]string{
			"X-Shard-ID":      strconv.FormatUint(uint64(route.ShardID), 10),
			"X-Owner-Zone-ID": route.OwnerZoneID,
			"X-Owner-Epoch":   strconv.FormatUint(route.OwnerEpoch, 10),
			"X-Route-Version": strconv.FormatUint(route.RouteVersion, 10),
		}
		endpoint := strings.TrimRight(route.OwnerEndpoint, "/") + path
		lastErr = c.postProto(ctx, endpoint, request, response, headers)
		var statusErr *httpStatusError
		if !errors.As(lastErr, &statusErr) || statusErr.status != http.StatusConflict {
			return lastErr
		}
		if err := c.Routes.ForceResync(ctx); err != nil {
			return err
		}
	}
	return lastErr
}

func (c *HTTPClients) postProto(
	ctx context.Context,
	endpoint string,
	request, response proto.Message,
	headers map[string]string,
) error {
	body, err := proto.Marshal(request)
	if err != nil {
		return err
	}
	httpRequest, err := http.NewRequestWithContext(
		ctx, http.MethodPost, endpoint, bytes.NewReader(body),
	)
	if err != nil {
		return err
	}
	httpRequest.Header.Set("Content-Type", "application/x-protobuf")
	for name, value := range headers {
		httpRequest.Header.Set(name, value)
	}
	httpResponse, err := c.client().Do(httpRequest)
	if err != nil {
		return err
	}
	defer httpResponse.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(httpResponse.Body, maxMessageBytes+1))
	if err != nil {
		return err
	}
	if httpResponse.StatusCode != http.StatusOK {
		return &httpStatusError{status: httpResponse.StatusCode}
	}
	if len(responseBody) > maxMessageBytes {
		return errors.New("invalid protobuf response size")
	}
	if err := proto.Unmarshal(responseBody, response); err != nil {
		return fmt.Errorf("decode protobuf response: %w", err)
	}
	return nil
}

func (c *HTTPClients) client() *http.Client {
	if c.Client != nil {
		return c.Client
	}
	return http.DefaultClient
}

type httpStatusError struct {
	status int
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("HTTP request returned %d", e.status)
}
