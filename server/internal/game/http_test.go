package game

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/coder/websocket"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestJSONLoginAndWebSocket(t *testing.T) {
	app := NewServer(NewMemoryStore())
	defer app.Close()
	srv := httptest.NewServer(app.Handler())
	defer srv.Close()
	post := func(path, body string, want int) Response {
		t.Helper()
		res, err := http.Post(srv.URL+path, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != want {
			t.Fatalf("%s status %d", path, res.StatusCode)
		}
		var out Response
		if err = json.NewDecoder(res.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	post("/api/register", `{"username":"alice","password":"student-password"}`, 201)
	post("/api/register", `{"username":"alice","password":"student-password"}`, 409)
	post("/api/login", `{"username":"alice","password":"wrong-password"}`, 401)
	login := post("/api/login", `{"username":"alice","password":"student-password"}`, 200)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	call := func(c Command) Response {
		t.Helper()
		b, _ := json.Marshal(c)
		if err := conn.Write(ctx, websocket.MessageText, b); err != nil {
			t.Fatal(err)
		}
		typ, b, err := conn.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if typ != websocket.MessageText {
			t.Fatal("not JSON text")
		}
		var res Response
		if err = json.Unmarshal(b, &res); err != nil {
			t.Fatal(err)
		}
		return res
	}
	res := call(Command{RequestID: "auth-0001", Action: "AUTH", Data: Args{Token: login.Token}})
	if res.Code != "OK" {
		t.Fatal(res)
	}
	res = call(Command{RequestID: "buy-00001", Action: "BUY_SEEDS", Data: Args{Quantity: 3}})
	if res.Code != "OK" || res.Snapshot.Seeds != 3 {
		t.Fatal(res)
	}
	req, _ := http.NewRequest("POST", srv.URL+"/api/logout", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Authorization", "Bearer "+login.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
	_, _, err = conn.Read(ctx)
	if err == nil {
		t.Fatal("logout did not close connection")
	}
}

func TestRejectUnknownFieldsAndUnauthenticatedGame(t *testing.T) {
	app := NewServer(NewMemoryStore())
	defer app.Close()
	srv := httptest.NewServer(app.Handler())
	defer srv.Close()
	res, err := http.Post(srv.URL+"/api/register", "application/json", strings.NewReader(`{"username":"alice","password":"student-password","coins":999}`))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 400 {
		t.Fatal(res.StatusCode)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	_ = conn.Write(ctx, websocket.MessageText, []byte(`{"request_id":"buy-00001","action":"BUY_SEEDS","data":{"quantity":1}}`))
	_, b, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var reply Response
	_ = json.Unmarshal(b, &reply)
	if reply.Code != "UNAUTHENTICATED" {
		t.Fatal(string(b))
	}
}

func TestJSONMailbox(t *testing.T) {
	app := NewServer(NewMemoryStore())
	defer app.Close()
	srv := httptest.NewServer(app.Handler())
	defer srv.Close()
	registerAndLogin := func(username string) Response {
		t.Helper()
		body := `{"username":"` + username + `","password":"student-password"}`
		res, err := http.Post(srv.URL+"/api/register", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusCreated {
			t.Fatal(res.StatusCode)
		}
		res, err = http.Post(srv.URL+"/api/login", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var reply Response
		if err := json.NewDecoder(res.Body).Decode(&reply); err != nil {
			t.Fatal(err)
		}
		return reply
	}
	connect := func(login Response) (*websocket.Conn, func(Command) Response) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		t.Cleanup(cancel)
		conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws", nil)
		if err != nil {
			t.Fatal(err)
		}
		call := func(command Command) Response {
			body, _ := json.Marshal(command)
			if err := conn.Write(ctx, websocket.MessageText, body); err != nil {
				t.Fatal(err)
			}
			_, body, err = conn.Read(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var reply Response
			if err := json.Unmarshal(body, &reply); err != nil {
				t.Fatal(err)
			}
			return reply
		}
		if reply := call(Command{RequestID: "auth-0001", Action: "AUTH", Data: Args{Token: login.Token}}); reply.Code != "OK" {
			t.Fatal(reply)
		}
		return conn, call
	}

	loginA := registerAndLogin("alice")
	connA, callA := connect(loginA)
	defer connA.CloseNow()
	mailbox := callA(Command{RequestID: "mailbox-0001", Action: "GET_MAILBOX"})
	if mailbox.Code != "OK" || len(mailbox.Mails) != 1 || mailbox.Mails[0].Title != WelcomeMailTitle {
		t.Fatalf("mailbox mismatch: %+v", mailbox)
	}
	read := callA(Command{RequestID: "readmail-001", Action: "READ_MAIL", Data: Args{MailID: mailbox.Mails[0].ID}})
	if read.Code != "OK" || len(read.Mails) != 1 || !read.Mails[0].IsRead {
		t.Fatalf("read mismatch: %+v", read)
	}

	loginB := registerAndLogin("bob")
	connB, callB := connect(loginB)
	defer connB.CloseNow()
	foreign := callB(Command{RequestID: "readmail-002", Action: "READ_MAIL", Data: Args{MailID: mailbox.Mails[0].ID}})
	if foreign.Code != "MAIL_NOT_FOUND" {
		t.Fatalf("foreign mail returned %+v", foreign)
	}
	invalid := callB(Command{RequestID: "readmail-003", Action: "READ_MAIL", Data: Args{MailID: "abc"}})
	if invalid.Code != "INVALID_ARGUMENT" {
		t.Fatalf("invalid mail id returned %+v", invalid)
	}
}
