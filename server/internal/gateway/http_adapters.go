package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"syscall"
)

type HTTPTicketConsumer struct {
	Client    *http.Client
	Endpoint  string
	GatewayID string
}

func (c *HTTPTicketConsumer) Consume(ctx context.Context, ticket string) (uint64, error) {
	body, err := json.Marshal(struct {
		Ticket    string `json:"ticket"`
		GatewayID string `json:"gateway_id"`
	}{Ticket: ticket, GatewayID: valueOr(c.GatewayID, DefaultGatewayID)})
	if err != nil {
		return 0, err
	}
	endpoint := valueOr(c.Endpoint, "http://127.0.0.1:8080/internal/v1/ws-tickets/consume")
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := clientOrDefault(c.Client).Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return 0, fmt.Errorf("ticket consume returned %s", response.Status)
	}
	var result struct {
		PlayerID string `json:"player_id"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 4097))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return 0, fmt.Errorf("decode ticket response: %w", err)
	}
	playerID, err := strconv.ParseUint(result.PlayerID, 10, 64)
	if err != nil || playerID == 0 {
		return 0, errors.New("ticket response has invalid player_id")
	}
	return playerID, nil
}

type HTTPZoneCommander struct {
	Client *http.Client
}

type HTTPFriendCommander struct {
	Client   *http.Client
	Endpoint string
}

func (f *HTTPFriendCommander) Command(ctx context.Context, caller uint64, body []byte) ([]byte, error) {
	endpoint := valueOr(f.Endpoint, "http://127.0.0.1:8085/internal/v1/command")
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/x-protobuf")
	request.Header.Set("X-Caller-Player-ID", strconv.FormatUint(caller, 10))
	response, err := clientOrDefault(f.Client).Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return nil, fmt.Errorf("FriendSvr returned %s", response.Status)
	}
	result, err := io.ReadAll(io.LimitReader(response.Body, MaxMessageBytes+1))
	if err != nil {
		return nil, err
	}
	if len(result) > MaxMessageBytes {
		return nil, errors.New("FriendSvr response exceeds 64 KiB")
	}
	return result, nil
}

type zoneCommandError struct {
	kind string
	err  error
}

func (e *zoneCommandError) Error() string {
	return e.err.Error()
}

func (e *zoneCommandError) Unwrap() error {
	return e.err
}

func (z *HTTPZoneCommander) Command(ctx context.Context, route Route, caller uint64, body []byte) ([]byte, error) {
	endpoint := strings.TrimRight(route.OwnerEndpoint, "/") + "/internal/v1/command"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, &zoneCommandError{kind: "request", err: err}
	}
	request.Header.Set("Content-Type", "application/x-protobuf")
	request.Header.Set("X-Caller-Player-ID", strconv.FormatUint(caller, 10))
	request.Header.Set("X-Shard-ID", strconv.FormatUint(uint64(route.ShardID), 10))
	request.Header.Set("X-Owner-Zone-ID", route.OwnerZoneID)
	request.Header.Set("X-Owner-Epoch", strconv.FormatUint(route.OwnerEpoch, 10))
	request.Header.Set("X-Route-Version", strconv.FormatUint(route.RouteVersion, 10))
	response, err := clientOrDefault(z.Client).Do(request)
	if err != nil {
		return nil, &zoneCommandError{kind: "transport_" + classifyTransportError(err), err: err}
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusConflict {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return nil, ErrNotOwner
	}
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return nil, &zoneCommandError{
			kind: fmt.Sprintf("http_%d", response.StatusCode),
			err:  fmt.Errorf("Zone returned %s", response.Status),
		}
	}
	body, err = io.ReadAll(io.LimitReader(response.Body, MaxMessageBytes+1))
	if err != nil {
		return nil, &zoneCommandError{kind: "read", err: err}
	}
	if len(body) > MaxMessageBytes {
		return nil, &zoneCommandError{kind: "too_large", err: errors.New("Zone response exceeds 64 KiB")}
	}
	return body, nil
}

func classifyTransportError(err error) string {
	var networkError net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.As(err, &networkError) && networkError.Timeout():
		return "timeout"
	case errors.Is(err, syscall.ECONNRESET):
		return "connection_reset"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "connection_refused"
	case errors.Is(err, syscall.EPIPE):
		return "broken_pipe"
	case errors.Is(err, io.EOF):
		return "eof"
	default:
		return "other"
	}
}

func clientOrDefault(client *http.Client) *http.Client {
	if client == nil {
		return http.DefaultClient
	}
	return client
}

func valueOr(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
