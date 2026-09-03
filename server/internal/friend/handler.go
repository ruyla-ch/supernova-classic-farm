package friend

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	wsv1 "github.com/Wriosley/supernova-classic-farm/server/gen/classicfarm/v1/ws"
	zonev1 "github.com/Wriosley/supernova-classic-farm/server/gen/classicfarm/v1/zone"
	"google.golang.org/protobuf/proto"
)

const maxMessageBytes = 64 << 10

type Handler struct {
	store  Store
	now    func() time.Time
	credit TaskCreditor
}

func NewHandler(store Store, now func() time.Time, credit TaskCreditor) (http.Handler, error) {
	if store == nil {
		return nil, errors.New("friend store is required")
	}
	if now == nil {
		now = time.Now
	}
	h := &Handler{store: store, now: now, credit: credit}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /internal/v1/command", h.command)
	mux.HandleFunc("POST /internal/v1/friends/check", h.checkMutual)
	return mux, nil
}

func (h *Handler) checkMutual(w http.ResponseWriter, r *http.Request) {
	if !loopbackRemote(r.RemoteAddr) {
		http.Error(w, "loopback only", http.StatusForbidden)
		return
	}
	if media := strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]); media != "application/x-protobuf" {
		http.Error(w, "protobuf required", http.StatusUnsupportedMediaType)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxMessageBytes+1))
	if err != nil || len(body) == 0 || len(body) > maxMessageBytes {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	request := &zonev1.CheckMutualFriendRequest{}
	if proto.Unmarshal(body, request) != nil ||
		request.FirstPlayerId == 0 || request.SecondPlayerId == 0 ||
		request.FirstPlayerId == request.SecondPlayerId {
		http.Error(w, "invalid players", http.StatusBadRequest)
		return
	}
	mutual, err := h.store.CheckMutual(
		r.Context(), request.FirstPlayerId, request.SecondPlayerId,
	)
	if err != nil {
		http.Error(w, "friend store unavailable", http.StatusServiceUnavailable)
		return
	}
	encoded, err := proto.Marshal(&zonev1.CheckMutualFriendResponse{Mutual: mutual})
	if err != nil {
		http.Error(w, "encode response", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(encoded)
}

func loopbackRemote(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (h *Handler) command(w http.ResponseWriter, r *http.Request) {
	if media := strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]); media != "application/x-protobuf" {
		http.Error(w, "protobuf required", http.StatusUnsupportedMediaType)
		return
	}
	callerID, err := strconv.ParseUint(r.Header.Get("X-Caller-Player-ID"), 10, 64)
	if err != nil || callerID == 0 {
		http.Error(w, "invalid caller", http.StatusUnauthorized)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxMessageBytes+1))
	if err != nil || len(body) == 0 || len(body) > maxMessageBytes {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	request := &wsv1.WsEnvelope{}
	if proto.Unmarshal(body, request) != nil ||
		request.ProtocolVersion != 1 ||
		request.MessageKind != wsv1.MessageKind_REQUEST ||
		request.RequestId == "" ||
		request.TargetPlayerId != callerID {
		http.Error(w, "invalid envelope", http.StatusBadRequest)
		return
	}

	response := &wsv1.WsEnvelope{
		ProtocolVersion: 1,
		MessageKind:     wsv1.MessageKind_RESPONSE,
		Action:          request.Action,
		RequestId:       request.RequestId,
		TargetPlayerId:  callerID,
		ServerTimeMs:    h.now().UnixMilli(),
	}
	switch request.Action {
	case wsv1.Action_CREATE_FRIEND_CODE:
		if request.GetCreateFriendCodeRequest() == nil {
			http.Error(w, "invalid payload", http.StatusBadRequest)
			return
		}
		code, storeErr := h.store.CreateCode(r.Context(), callerID, h.now())
		if storeErr == nil {
			response.Payload = &wsv1.WsEnvelope_CreateFriendCodeResponse{
				CreateFriendCodeResponse: &wsv1.CreateFriendCodeResponse{
					Code: code.Value, CreatedAtMs: code.CreatedAt.UnixMilli(),
					ExpiresAtMs: code.ExpiresAt.UnixMilli(),
				},
			}
		} else {
			response.Error = friendError(storeErr)
		}
	case wsv1.Action_REDEEM_FRIEND_CODE:
		payload := request.GetRedeemFriendCodeRequest()
		if payload == nil || strings.TrimSpace(payload.Code) == "" {
			http.Error(w, "invalid payload", http.StatusBadRequest)
			return
		}
		result, storeErr := h.store.RedeemCode(r.Context(), callerID, payload.Code, h.now())
		if storeErr == nil && h.credit != nil {
			if creditErr := h.credit.Credit(r.Context(), callerID); creditErr != nil {
				storeErr = fmt.Errorf("credit caller: %w", creditErr)
			} else if creditErr := h.credit.Credit(r.Context(), result.Friend.PlayerID); creditErr != nil {
				storeErr = fmt.Errorf("credit friend: %w", creditErr)
			}
		}
		if storeErr == nil {
			response.Payload = &wsv1.WsEnvelope_RedeemFriendCodeResponse{
				RedeemFriendCodeResponse: &wsv1.RedeemFriendCodeResponse{
					Friend: &wsv1.FriendView{
						PlayerId: result.Friend.PlayerID, AccountName: result.Friend.AccountName,
						CreatedAtMs: result.Friend.CreatedAt.UnixMilli(),
					},
					NewlyCreated: result.NewlyCreated,
				},
			}
		} else {
			response.Error = friendError(storeErr)
		}
	case wsv1.Action_LIST_FRIENDS:
		if request.GetListFriendsRequest() == nil {
			http.Error(w, "invalid payload", http.StatusBadRequest)
			return
		}
		views, storeErr := h.store.List(r.Context(), callerID)
		if storeErr == nil {
			friends := make([]*wsv1.FriendView, 0, len(views))
			for _, view := range views {
				friends = append(friends, &wsv1.FriendView{
					PlayerId: view.PlayerID, AccountName: view.AccountName,
					CreatedAtMs: view.CreatedAt.UnixMilli(),
				})
			}
			response.Payload = &wsv1.WsEnvelope_ListFriendsResponse{
				ListFriendsResponse: &wsv1.ListFriendsResponse{Friends: friends},
			}
		} else {
			response.Error = friendError(storeErr)
		}
	default:
		response.Error = &wsv1.Error{Code: wsv1.ErrorCode_UNKNOWN_ACTION}
	}
	encoded, err := proto.Marshal(response)
	if err != nil {
		http.Error(w, "encode response", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(encoded)
}

func friendError(err error) *wsv1.Error {
	code := wsv1.ErrorCode_SERVICE_UNAVAILABLE
	retryable := true
	switch {
	case errors.Is(err, ErrCodeNotFound):
		code, retryable = wsv1.ErrorCode_FRIEND_CODE_NOT_FOUND, false
	case errors.Is(err, ErrCodeExpired):
		code, retryable = wsv1.ErrorCode_FRIEND_CODE_EXPIRED, false
	case errors.Is(err, ErrCannotSelf):
		code, retryable = wsv1.ErrorCode_CANNOT_FRIEND_SELF, false
	case errors.Is(err, ErrFriendLimit):
		code, retryable = wsv1.ErrorCode_FRIEND_LIMIT_REACHED, false
	}
	return &wsv1.Error{Code: code, Retryable: retryable}
}
