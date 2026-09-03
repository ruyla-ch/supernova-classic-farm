package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPFriendCommanderForwardsTrustedCallerAndProtobuf(t *testing.T) {
	const payload = "protobuf-body"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost ||
			r.Header.Get("Content-Type") != "application/x-protobuf" ||
			r.Header.Get("X-Caller-Player-ID") != "42" {
			t.Fatalf("unexpected request: method=%s headers=%v", r.Method, r.Header)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil || string(body) != payload {
			t.Fatalf("body=%q err=%v", body, err)
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
		_, _ = w.Write([]byte("friend-response"))
	}))
	defer server.Close()
	commander := &HTTPFriendCommander{Client: server.Client(), Endpoint: server.URL}
	response, err := commander.Command(context.Background(), 42, []byte(payload))
	if err != nil {
		t.Fatal(err)
	}
	if string(response) != "friend-response" {
		t.Fatalf("response=%q", response)
	}
}
