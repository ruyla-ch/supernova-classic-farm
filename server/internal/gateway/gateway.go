// Package gateway implements the local GateSvr WebSocket boundary.
package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	wsv1 "github.com/Wriosley/supernova-classic-farm/server/gen/classicfarm/v1/ws"
	"github.com/Wriosley/supernova-classic-farm/server/internal/routing"
	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"
)

const (
	ProtocolVersion  uint32 = 1
	MaxMessageBytes         = 64 << 10
	DefaultGatewayID        = "local-gateway"
	DefaultConfigURL        = "http://127.0.0.1:8080/v1/client-config/1"
)

var (
	ErrNotOwner      = errors.New("zone is not owner")
	errUnknownAction = errors.New("unknown action")
	defaultConfigSHA = sha256.Sum256([]byte("classicfarm-client-config-v1"))
)

type TicketConsumer interface {
	Consume(context.Context, string) (uint64, error)
}

type Route struct {
	ShardID        uint32
	OwnerZoneID    string
	OwnerEpoch     uint64
	RouteVersion   uint64
	MapVersion     uint64
	LeaseExpiresAt time.Time
	OwnerEndpoint  string
}

type RouteResolver interface {
	Resolve(context.Context, uint32) (Route, error)
}

// RouteRefresher synchronously replaces a stale complete route snapshot.
type RouteRefresher interface {
	Refresh(context.Context) error
}

type ZoneCommander interface {
	Command(context.Context, Route, uint64, []byte) ([]byte, error)
}

type FriendCommander interface {
	Command(context.Context, uint64, []byte) ([]byte, error)
}

type Config struct {
	Tickets           TicketConsumer
	Routes            RouteResolver
	Zone              ZoneCommander
	Friend            FriendCommander
	AuthTimeout       time.Duration
	CommandTimeout    time.Duration
	HeartbeatInterval time.Duration
	ClientConfigURL   string
	ClientConfigSHA   []byte
	Now               func() time.Time
}

type Handler struct {
	tickets           TicketConsumer
	routes            RouteResolver
	zone              ZoneCommander
	friend            FriendCommander
	authTimeout       time.Duration
	commandTimeout    time.Duration
	heartbeatInterval time.Duration
	clientConfigURL   string
	clientConfigSHA   []byte
	now               func() time.Time
	pushHub           *PushHub
	failureStats      *commandFailureStats
}

type commandFailureStats struct {
	mu         sync.Mutex
	counts     map[string]uint64
	lastErrors map[string]string
}

func NewHandler(cfg Config) (*Handler, error) {
	if cfg.Tickets == nil || cfg.Routes == nil || cfg.Zone == nil {
		return nil, errors.New("ticket, route, and Zone adapters are required")
	}
	if cfg.AuthTimeout == 0 {
		cfg.AuthTimeout = 10 * time.Second
	}
	if cfg.CommandTimeout == 0 {
		cfg.CommandTimeout = 5 * time.Second
	}
	if cfg.HeartbeatInterval == 0 {
		cfg.HeartbeatInterval = 30 * time.Second
	}
	if cfg.ClientConfigURL == "" {
		cfg.ClientConfigURL = DefaultConfigURL
	}
	if cfg.ClientConfigSHA == nil {
		cfg.ClientConfigSHA = defaultConfigSHA[:]
	}
	if len(cfg.ClientConfigSHA) != sha256.Size {
		return nil, fmt.Errorf("client config SHA-256 must be %d bytes", sha256.Size)
	}
	if cfg.AuthTimeout <= 0 || cfg.CommandTimeout <= 0 || cfg.HeartbeatInterval <= 0 {
		return nil, errors.New("timeouts and heartbeat interval must be positive")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Handler{
		tickets: cfg.Tickets, routes: cfg.Routes, zone: cfg.Zone,
		friend:      cfg.Friend,
		authTimeout: cfg.AuthTimeout, commandTimeout: cfg.CommandTimeout,
		heartbeatInterval: cfg.HeartbeatInterval,
		clientConfigURL:   cfg.ClientConfigURL,
		clientConfigSHA:   append([]byte(nil), cfg.ClientConfigSHA...),
		now:               cfg.Now,
		pushHub:           newPushHub(),
		failureStats: &commandFailureStats{
			counts: make(map[string]uint64), lastErrors: make(map[string]string),
		},
	}, nil
}

func (h *Handler) PushHandler() http.Handler {
	return h.pushHub
}

// DebugCommandFailuresHandler exposes aggregate local diagnostic counters.
func (h *Handler) DebugCommandFailuresHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		h.failureStats.mu.Lock()
		counts := make(map[string]uint64, len(h.failureStats.counts))
		for source, count := range h.failureStats.counts {
			counts[source] = count
		}
		lastErrors := make(map[string]string, len(h.failureStats.lastErrors))
		for source, message := range h.failureStats.lastErrors {
			lastErrors[source] = message
		}
		h.failureStats.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(struct {
			Failures   map[string]uint64 `json:"failures"`
			LastErrors map[string]string `json:"last_errors"`
		}{Failures: counts, LastErrors: lastErrors})
	})
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{"localhost:5173", "127.0.0.1:5173"},
	})
	if err != nil {
		return
	}
	conn.SetReadLimit(MaxMessageBytes)
	defer conn.CloseNow()
	h.serveConnection(r.Context(), conn)
}

type serializedWriter struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

func (w *serializedWriter) write(ctx context.Context, body []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.conn.Write(ctx, websocket.MessageBinary, body)
}

func (w *serializedWriter) writeBatch(ctx context.Context, bodies ...[]byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, body := range bodies {
		if err := w.conn.Write(ctx, websocket.MessageBinary, body); err != nil {
			return err
		}
	}
	return nil
}

func (h *Handler) serveConnection(parent context.Context, conn *websocket.Conn) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	writer := &serializedWriter{conn: conn}
	authDeadline := h.now().Add(h.authTimeout)
	authTimer := time.AfterFunc(h.authTimeout, func() {
		_ = conn.Close(websocket.StatusCode(4401), "authentication timeout")
	})
	defer authTimer.Stop()
	var caller uint64
	var authenticated bool
	var subscription *connectionSubscription
	var workers sync.WaitGroup
	defer func() { h.pushHub.unsubscribe(subscription) }()

	for {
		messageType, body, err := conn.Read(ctx)
		if err != nil {
			if websocket.CloseStatus(err) == websocket.StatusMessageTooBig {
				_ = conn.Close(websocket.StatusMessageTooBig, "message exceeds 64 KiB")
			}
			break
		}
		if messageType != websocket.MessageBinary {
			_ = conn.Close(websocket.StatusProtocolError, "binary protobuf required")
			break
		}
		request := &wsv1.WsEnvelope{}
		if len(body) == 0 || proto.Unmarshal(body, request) != nil {
			_ = conn.Close(websocket.StatusProtocolError, "malformed protobuf")
			break
		}
		if request.ProtocolVersion != ProtocolVersion {
			_ = conn.Close(websocket.StatusCode(4406), "unsupported protocol version")
			break
		}
		if err := validateRequestTuple(request); errors.Is(err, errUnknownAction) {
			if !authenticated {
				_ = conn.Close(websocket.StatusCode(4401), "AUTH must be first non-heartbeat request")
				break
			}
			if writer.write(ctx, marshalResponse(errorResponse(request, wsv1.ErrorCode_UNKNOWN_ACTION, false, h.now))) != nil {
				break
			}
			continue
		} else if err != nil {
			_ = conn.Close(websocket.StatusProtocolError, "invalid envelope tuple")
			break
		}

		if request.Action == wsv1.Action_PING {
			workers.Add(1)
			go func(req *wsv1.WsEnvelope) {
				defer workers.Done()
				_ = writer.write(ctx, marshalResponse(pingResponse(req, h.now)))
			}(request)
			continue
		}

		if !authenticated {
			if request.Action != wsv1.Action_AUTH {
				_ = conn.Close(websocket.StatusCode(4401), "AUTH must be first non-heartbeat request")
				break
			}
			authCtx, authCancel := context.WithDeadline(ctx, authDeadline)
			playerID, err := h.tickets.Consume(authCtx, request.GetAuthRequest().GetWsTicket())
			authCancel()
			if err != nil || playerID == 0 {
				_ = conn.Close(websocket.StatusCode(4401), "invalid authentication ticket")
				break
			}
			caller = playerID
			authenticated = true
			subscription = h.pushHub.subscribe(playerID, writer, ctx)
			authTimer.Stop()
			if writer.write(ctx, marshalResponse(h.authResponse(request, playerID))) != nil {
				break
			}
			continue
		}
		if request.Action == wsv1.Action_AUTH {
			_ = conn.Close(websocket.StatusProtocolError, "AUTH may only occur once")
			break
		}

		workers.Add(1)
		go func(req *wsv1.WsEnvelope, raw []byte) {
			defer workers.Done()
			h.handleGame(ctx, writer, subscription, caller, req, raw)
		}(request, append([]byte(nil), body...))
	}
	cancel()
	workers.Wait()
}

func validateRequestTuple(request *wsv1.WsEnvelope) error {
	if request.MessageKind != wsv1.MessageKind_REQUEST || request.RequestId == "" ||
		request.StateVersion != nil || request.Error != nil || request.Replayed ||
		request.ServerTimeMs != 0 {
		return errors.New("invalid common request fields")
	}
	switch request.Action {
	case wsv1.Action_AUTH:
		if request.TargetPlayerId != 0 || request.GetAuthRequest() == nil ||
			request.GetAuthRequest().GetWsTicket() == "" {
			return errors.New("invalid AUTH")
		}
	case wsv1.Action_PING:
		if request.TargetPlayerId != 0 || request.GetPingRequest() == nil {
			return errors.New("invalid PING")
		}
	case wsv1.Action_GET_PLAYER_SNAPSHOT:
		if request.TargetPlayerId == 0 || request.GetGetPlayerSnapshotRequest() == nil {
			return errors.New("invalid snapshot request")
		}
	case wsv1.Action_GET_SHOP:
		if request.TargetPlayerId == 0 || request.GetGetShopRequest() == nil {
			return errors.New("invalid shop request")
		}
	case wsv1.Action_BUY_SEEDS:
		if request.TargetPlayerId == 0 || request.GetBuySeedsRequest() == nil {
			return errors.New("invalid buy request")
		}
	case wsv1.Action_BUY_FERTILIZER:
		if request.TargetPlayerId == 0 || request.GetBuyFertilizerRequest() == nil {
			return errors.New("invalid buy fertilizer request")
		}
	case wsv1.Action_PLANT:
		if request.TargetPlayerId == 0 || request.GetPlantRequest() == nil {
			return errors.New("invalid plant request")
		}
	case wsv1.Action_APPLY_FERTILIZER:
		if request.TargetPlayerId == 0 || request.GetApplyFertilizerRequest() == nil {
			return errors.New("invalid fertilizer request")
		}
	case wsv1.Action_CATCH_PEST:
		payload := request.GetCatchPestRequest()
		if request.TargetPlayerId == 0 || payload == nil || payload.PlotId == 0 {
			return errors.New("invalid catch pest request")
		}
	case wsv1.Action_HARVEST:
		if request.TargetPlayerId == 0 || request.GetHarvestRequest() == nil {
			return errors.New("invalid harvest request")
		}
	case wsv1.Action_CLEAN_PLOT:
		if request.TargetPlayerId == 0 || request.GetCleanPlotRequest() == nil {
			return errors.New("invalid clean request")
		}
	case wsv1.Action_SELL_CROP:
		if request.TargetPlayerId == 0 || request.GetSellCropRequest() == nil {
			return errors.New("invalid sell request")
		}
	case wsv1.Action_CLAIM_CHAPTER_REWARD:
		if request.TargetPlayerId == 0 || request.GetClaimChapterRewardRequest() == nil {
			return errors.New("invalid claim request")
		}
	case wsv1.Action_CREATE_FRIEND_CODE:
		if request.TargetPlayerId == 0 || request.GetCreateFriendCodeRequest() == nil {
			return errors.New("invalid create friend code request")
		}
	case wsv1.Action_REDEEM_FRIEND_CODE:
		if request.TargetPlayerId == 0 || request.GetRedeemFriendCodeRequest() == nil ||
			request.GetRedeemFriendCodeRequest().GetCode() == "" {
			return errors.New("invalid redeem friend code request")
		}
	case wsv1.Action_LIST_FRIENDS:
		if request.TargetPlayerId == 0 || request.GetListFriendsRequest() == nil {
			return errors.New("invalid list friends request")
		}
	case wsv1.Action_ENTER_FRIEND_FARM:
		payload := request.GetEnterFriendFarmRequest()
		if request.TargetPlayerId == 0 || payload == nil ||
			payload.OwnerPlayerId == 0 || payload.OwnerPlayerId == request.TargetPlayerId {
			return errors.New("invalid enter friend farm request")
		}
	case wsv1.Action_FARM_HEARTBEAT:
		payload := request.GetFarmHeartbeatRequest()
		if request.TargetPlayerId == 0 || payload == nil ||
			payload.OwnerPlayerId == 0 || len(payload.VisitId) != 16 {
			return errors.New("invalid farm heartbeat request")
		}
	case wsv1.Action_EXIT_FRIEND_FARM:
		payload := request.GetExitFriendFarmRequest()
		if request.TargetPlayerId == 0 || payload == nil ||
			payload.OwnerPlayerId == 0 || len(payload.VisitId) != 16 {
			return errors.New("invalid exit friend farm request")
		}
	case wsv1.Action_APPLY_PEST_TO_FRIEND:
		payload := request.GetApplyPestToFriendRequest()
		if request.TargetPlayerId == 0 || payload == nil ||
			payload.OwnerPlayerId == 0 || payload.OwnerPlayerId == request.TargetPlayerId ||
			len(payload.VisitId) != 16 || payload.PlotId == 0 || payload.PestId == 0 {
			return errors.New("invalid apply pest request")
		}
	case wsv1.Action_CATCH_PEST_FOR_FRIEND:
		payload := request.GetCatchPestForFriendRequest()
		if request.TargetPlayerId == 0 || payload == nil ||
			payload.OwnerPlayerId == 0 || payload.OwnerPlayerId == request.TargetPlayerId ||
			len(payload.VisitId) != 16 || payload.PlotId == 0 {
			return errors.New("invalid catch pest for friend request")
		}
	case wsv1.Action_STEAL_FRIEND_CROP:
		payload := request.GetStealFriendCropRequest()
		if request.TargetPlayerId == 0 || payload == nil ||
			payload.OwnerPlayerId == 0 ||
			payload.OwnerPlayerId == request.TargetPlayerId ||
			len(payload.VisitId) != 16 || payload.PlotId == 0 ||
			payload.ExpectedCropItemId == 0 ||
			payload.ExpectedPlantedAtMs <= 0 ||
			payload.ExpectedStealQuantity == 0 {
			return errors.New("invalid steal friend crop request")
		}
	default:
		return errUnknownAction
	}
	return nil
}

func (h *Handler) authResponse(request *wsv1.WsEnvelope, playerID uint64) *wsv1.WsEnvelope {
	return &wsv1.WsEnvelope{
		ProtocolVersion: ProtocolVersion, MessageKind: wsv1.MessageKind_RESPONSE,
		Action: wsv1.Action_AUTH, RequestId: request.RequestId,
		ServerTimeMs: h.now().UnixMilli(),
		Payload: &wsv1.WsEnvelope_AuthResponse{AuthResponse: &wsv1.AuthResponse{
			PlayerId:            playerID,
			HeartbeatIntervalMs: uint32(h.heartbeatInterval / time.Millisecond),
			ClientConfigVersion: 1, ClientConfigUrl: h.clientConfigURL,
			ClientConfigSha256: append([]byte(nil), h.clientConfigSHA...),
			ProtocolMin:        ProtocolVersion, ProtocolMax: ProtocolVersion,
		}},
	}
}

func pingResponse(request *wsv1.WsEnvelope, now func() time.Time) *wsv1.WsEnvelope {
	ping := request.GetPingRequest()
	return &wsv1.WsEnvelope{
		ProtocolVersion: ProtocolVersion, MessageKind: wsv1.MessageKind_RESPONSE,
		Action: wsv1.Action_PING, RequestId: request.RequestId,
		ServerTimeMs: now().UnixMilli(),
		Payload: &wsv1.WsEnvelope_PingResponse{PingResponse: &wsv1.PingResponse{
			PingId: ping.GetPingId(), ClientSentAtMs: ping.GetClientSentAtMs(),
		}},
	}
}

func (h *Handler) handleGame(
	parent context.Context,
	writer *serializedWriter,
	subscription *connectionSubscription,
	caller uint64,
	request *wsv1.WsEnvelope,
	raw []byte,
) {
	if request.TargetPlayerId != caller {
		_ = writer.write(parent, marshalResponse(errorResponse(request, wsv1.ErrorCode_FORBIDDEN, false, h.now)))
		return
	}
	ctx, cancel := context.WithTimeout(parent, h.commandTimeout)
	defer cancel()
	if isFriendAction(request.Action) {
		if h.friend == nil {
			_ = writer.write(parent, marshalResponse(errorResponse(request, wsv1.ErrorCode_SERVICE_UNAVAILABLE, true, h.now)))
			return
		}
		response, err := h.friend.Command(ctx, caller, raw)
		if err == nil && validateFriendResponse(response, request) == nil {
			_ = writer.write(parent, response)
			return
		}
		code := wsv1.ErrorCode_SERVICE_UNAVAILABLE
		if errors.Is(err, context.DeadlineExceeded) {
			code = wsv1.ErrorCode_REQUEST_OUTCOME_UNKNOWN
		}
		_ = writer.write(parent, marshalResponse(errorResponse(request, code, true, h.now)))
		return
	}
	isSnapshot := request.Action == wsv1.Action_GET_PLAYER_SNAPSHOT
	if isSnapshot {
		subscription.beginSnapshot()
		defer subscription.abortSnapshot()
	}
	failureSource := "route_resolve"
	shardID := routing.ShardForPlayer(request.TargetPlayerId)
	route, err := h.routes.Resolve(ctx, shardID)
	if err == nil {
		failureSource = "zone_command"
		var response []byte
		response, err = h.zone.Command(ctx, route, caller, raw)
		var zoneFailure *zoneCommandError
		if errors.As(err, &zoneFailure) {
			failureSource = "zone_command_" + zoneFailure.kind
		}
		if errors.Is(err, ErrNotOwner) {
			if refresher, ok := h.routes.(RouteRefresher); ok {
				err = refresher.Refresh(ctx)
			} else {
				err = nil
			}
			if err == nil {
				route, err = h.routes.Resolve(ctx, shardID)
				if err == nil {
					response, err = h.zone.Command(ctx, route, caller, raw)
				}
			}
		}
		if err == nil {
			failureSource = "zone_response_validation"
			if validateZoneResponse(response, request) == nil {
				if isSnapshot {
					envelope := &wsv1.WsEnvelope{}
					if proto.Unmarshal(response, envelope) != nil || envelope.StateVersion == nil {
						err = errors.New("snapshot response lacks state version")
					} else {
						_ = subscription.finishSnapshot(parent, response, envelope.StateVersion)
						return
					}
				} else {
					_ = writer.write(parent, response)
					return
				}
			}
			if err == nil {
				err = errors.New("invalid Zone response")
			}
		}
	}
	h.failureStats.mu.Lock()
	h.failureStats.counts[failureSource]++
	if err != nil {
		h.failureStats.lastErrors[failureSource] = err.Error()
	}
	h.failureStats.mu.Unlock()
	code := wsv1.ErrorCode_SERVICE_UNAVAILABLE
	if errors.Is(err, context.DeadlineExceeded) {
		code = wsv1.ErrorCode_REQUEST_OUTCOME_UNKNOWN
	}
	_ = writer.write(parent, marshalResponse(errorResponse(request, code, true, h.now)))
}

func isFriendAction(action wsv1.Action) bool {
	switch action {
	case wsv1.Action_CREATE_FRIEND_CODE, wsv1.Action_REDEEM_FRIEND_CODE, wsv1.Action_LIST_FRIENDS:
		return true
	default:
		return false
	}
}

func validateFriendResponse(body []byte, request *wsv1.WsEnvelope) error {
	return validateZoneResponse(body, request)
}

func validateZoneResponse(body []byte, request *wsv1.WsEnvelope) error {
	response := &wsv1.WsEnvelope{}
	if len(body) == 0 || len(body) > MaxMessageBytes || proto.Unmarshal(body, response) != nil {
		return errors.New("malformed Zone response")
	}
	if response.ProtocolVersion != ProtocolVersion ||
		response.MessageKind != wsv1.MessageKind_RESPONSE ||
		response.Action != request.Action ||
		response.RequestId != request.RequestId {
		return errors.New("uncorrelated Zone response")
	}
	return nil
}

func errorResponse(request *wsv1.WsEnvelope, code wsv1.ErrorCode, retryable bool, now func() time.Time) *wsv1.WsEnvelope {
	return &wsv1.WsEnvelope{
		ProtocolVersion: ProtocolVersion, MessageKind: wsv1.MessageKind_RESPONSE,
		Action: request.Action, RequestId: request.RequestId,
		TargetPlayerId: request.TargetPlayerId, ServerTimeMs: now().UnixMilli(),
		Error: &wsv1.Error{Code: code, Retryable: retryable},
	}
}

func marshalResponse(response *wsv1.WsEnvelope) []byte {
	body, _ := proto.Marshal(response)
	return body
}
