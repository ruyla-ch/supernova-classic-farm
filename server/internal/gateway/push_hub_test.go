package gateway

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	wsv1 "github.com/Wriosley/supernova-classic-farm/server/gen/classicfarm/v1/ws"
	reasonv1 "github.com/Wriosley/supernova-classic-farm/server/gen/classicfarm/v1/ws/reason"
	"google.golang.org/protobuf/proto"
)

func TestPushHTTPHandlerAcceptsValidFriendPushAndRejectsMalformed(t *testing.T) {
	hub := newPushHub()
	validBody, err := proto.Marshal(friendFarmChangedPush(10))
	if err != nil {
		t.Fatal(err)
	}
	validRequest := httptest.NewRequest(
		http.MethodPost, "/internal/v1/player-state-changes", bytes.NewReader(validBody),
	)
	validRequest.RemoteAddr = "127.0.0.1:12345"
	validResponse := httptest.NewRecorder()
	hub.ServeHTTP(validResponse, validRequest)
	if validResponse.Code != http.StatusNoContent {
		t.Fatalf("valid status=%d", validResponse.Code)
	}

	malformed := friendFarmChangedPush(10)
	malformed.Payload = &wsv1.WsEnvelope_PlayerStateChangedPush{
		PlayerStateChangedPush: &wsv1.PlayerStateChangedPush{},
	}
	malformedBody, err := proto.Marshal(malformed)
	if err != nil {
		t.Fatal(err)
	}
	malformedRequest := httptest.NewRequest(
		http.MethodPost, "/internal/v1/player-state-changes", bytes.NewReader(malformedBody),
	)
	malformedRequest.RemoteAddr = "127.0.0.1:12345"
	malformedResponse := httptest.NewRecorder()
	hub.ServeHTTP(malformedResponse, malformedRequest)
	if malformedResponse.Code != http.StatusBadRequest {
		t.Fatalf("malformed status=%d", malformedResponse.Code)
	}
}

func TestPushHTTPHandlerAcceptsFriendStealOwnerPush(t *testing.T) {
	hub := newPushHub()
	envelope := maturedPush(20, 7)
	push := envelope.GetPlayerStateChangedPush()
	push.Reason = reasonv1.StateChangeReason_FRIEND_STEAL
	causedByRequestID := "steal-request"
	push.CausedByRequestId = &causedByRequestID
	body, err := proto.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(
		http.MethodPost, "/internal/v1/player-state-changes", bytes.NewReader(body),
	)
	request.RemoteAddr = "127.0.0.1:12345"
	response := httptest.NewRecorder()
	hub.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("friend steal owner push status=%d", response.Code)
	}
}
