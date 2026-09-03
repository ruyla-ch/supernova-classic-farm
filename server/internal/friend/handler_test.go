package friend

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	wsv1 "github.com/Wriosley/supernova-classic-farm/server/gen/classicfarm/v1/ws"
	"google.golang.org/protobuf/proto"
)

type fakeStore struct {
	create func(context.Context, uint64, time.Time) (Code, error)
	redeem func(context.Context, uint64, string, time.Time) (RedeemResult, error)
	list   func(context.Context, uint64) ([]View, error)
	check  func(context.Context, uint64, uint64) (bool, error)
}

func (s *fakeStore) CreateCode(ctx context.Context, id uint64, now time.Time) (Code, error) {
	return s.create(ctx, id, now)
}
func (s *fakeStore) RedeemCode(ctx context.Context, id uint64, code string, now time.Time) (RedeemResult, error) {
	return s.redeem(ctx, id, code, now)
}
func (s *fakeStore) List(ctx context.Context, id uint64) ([]View, error) {
	return s.list(ctx, id)
}

func (s *fakeStore) CheckMutual(ctx context.Context, first, second uint64) (bool, error) {
	if s.check == nil {
		return false, nil
	}
	return s.check(ctx, first, second)
}

func TestHandlerCreateCodeUsesTrustedCaller(t *testing.T) {
	now := time.UnixMilli(1_800_000_000_000)
	store := &fakeStore{
		create: func(_ context.Context, id uint64, got time.Time) (Code, error) {
			if id != 7 || !got.Equal(now) {
				t.Fatalf("CreateCode(%d, %v)", id, got)
			}
			return Code{Value: "00112233445566778899aabbccddeeff", CreatedAt: now, ExpiresAt: now.Add(CodeTTL)}, nil
		},
	}
	handler, err := NewHandler(store, func() time.Time { return now }, nil)
	if err != nil {
		t.Fatal(err)
	}
	requestBody, err := proto.Marshal(&wsv1.WsEnvelope{
		ProtocolVersion: 1, MessageKind: wsv1.MessageKind_REQUEST,
		Action: wsv1.Action_CREATE_FRIEND_CODE, RequestId: "request-1",
		TargetPlayerId: 7,
		Payload: &wsv1.WsEnvelope_CreateFriendCodeRequest{
			CreateFriendCodeRequest: &wsv1.CreateFriendCodeRequest{},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/internal/v1/command", bytes.NewReader(requestBody))
	request.Header.Set("Content-Type", "application/x-protobuf")
	request.Header.Set("X-Caller-Player-ID", "7")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	response := &wsv1.WsEnvelope{}
	if err := proto.Unmarshal(recorder.Body.Bytes(), response); err != nil {
		t.Fatal(err)
	}
	if response.GetCreateFriendCodeResponse().GetCode() != "00112233445566778899aabbccddeeff" {
		t.Fatalf("unexpected response: %v", response)
	}
}

func TestHandlerRejectsTargetDifferentFromTrustedCaller(t *testing.T) {
	store := &fakeStore{}
	handler, err := NewHandler(store, time.Now, nil)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := proto.Marshal(&wsv1.WsEnvelope{
		ProtocolVersion: 1, MessageKind: wsv1.MessageKind_REQUEST,
		Action: wsv1.Action_LIST_FRIENDS, RequestId: "request-2",
		TargetPlayerId: 9,
		Payload: &wsv1.WsEnvelope_ListFriendsRequest{
			ListFriendsRequest: &wsv1.ListFriendsRequest{},
		},
	})
	request := httptest.NewRequest(http.MethodPost, "/internal/v1/command", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/x-protobuf")
	request.Header.Set("X-Caller-Player-ID", "7")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d", recorder.Code)
	}
}

type recordingCreditor struct {
	ids []uint64
	err error
}

func (c *recordingCreditor) Credit(_ context.Context, playerID uint64) error {
	c.ids = append(c.ids, playerID)
	return c.err
}

func TestHandlerRedeemCreditsBothPlayers(t *testing.T) {
	now := time.UnixMilli(1_800_000_000_000)
	store := &fakeStore{
		redeem: func(_ context.Context, id uint64, code string, _ time.Time) (RedeemResult, error) {
			if id != 7 || code != "aabbccddeeff00112233445566778899" {
				t.Fatalf("RedeemCode(%d, %s)", id, code)
			}
			return RedeemResult{
				NewlyCreated: true,
				Friend:       View{PlayerID: 9, AccountName: "other", CreatedAt: now},
			}, nil
		},
	}
	credit := &recordingCreditor{}
	handler, err := NewHandler(store, func() time.Time { return now }, credit)
	if err != nil {
		t.Fatal(err)
	}
	body, err := proto.Marshal(&wsv1.WsEnvelope{
		ProtocolVersion: 1, MessageKind: wsv1.MessageKind_REQUEST,
		Action: wsv1.Action_REDEEM_FRIEND_CODE, RequestId: "request-3",
		TargetPlayerId: 7,
		Payload: &wsv1.WsEnvelope_RedeemFriendCodeRequest{
			RedeemFriendCodeRequest: &wsv1.RedeemFriendCodeRequest{
				Code: "aabbccddeeff00112233445566778899",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/internal/v1/command", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/x-protobuf")
	request.Header.Set("X-Caller-Player-ID", "7")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	response := &wsv1.WsEnvelope{}
	if err := proto.Unmarshal(recorder.Body.Bytes(), response); err != nil {
		t.Fatal(err)
	}
	if response.Error != nil || !response.GetRedeemFriendCodeResponse().GetNewlyCreated() {
		t.Fatalf("unexpected response: %v", response)
	}
	if len(credit.ids) != 2 || credit.ids[0] != 7 || credit.ids[1] != 9 {
		t.Fatalf("credited %v", credit.ids)
	}
}

func TestHandlerRedeemCreditFailureIsRetryable(t *testing.T) {
	now := time.UnixMilli(1_800_000_000_000)
	store := &fakeStore{
		redeem: func(context.Context, uint64, string, time.Time) (RedeemResult, error) {
			return RedeemResult{
				Friend: View{PlayerID: 9, AccountName: "other", CreatedAt: now},
			}, nil
		},
	}
	handler, err := NewHandler(store, func() time.Time { return now }, &recordingCreditor{
		err: errors.New("zone unreachable"),
	})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := proto.Marshal(&wsv1.WsEnvelope{
		ProtocolVersion: 1, MessageKind: wsv1.MessageKind_REQUEST,
		Action: wsv1.Action_REDEEM_FRIEND_CODE, RequestId: "request-4",
		TargetPlayerId: 7,
		Payload: &wsv1.WsEnvelope_RedeemFriendCodeRequest{
			RedeemFriendCodeRequest: &wsv1.RedeemFriendCodeRequest{Code: "code"},
		},
	})
	request := httptest.NewRequest(http.MethodPost, "/internal/v1/command", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/x-protobuf")
	request.Header.Set("X-Caller-Player-ID", "7")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	response := &wsv1.WsEnvelope{}
	if err := proto.Unmarshal(recorder.Body.Bytes(), response); err != nil {
		t.Fatal(err)
	}
	if response.Error == nil ||
		response.Error.Code != wsv1.ErrorCode_SERVICE_UNAVAILABLE ||
		!response.Error.Retryable {
		t.Fatalf("unexpected error: %v", response.Error)
	}
}
