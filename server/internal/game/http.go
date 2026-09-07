package game

// 本文件提供注册、登录、注销、健康检查和配置查询的 HTTP JSON 接口。

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/coder/websocket"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"
)

type session struct {
	PlayerID string
	Expires  time.Time
}
type connection struct {
	Token  string
	Socket *websocket.Conn
}

// Server 持有业务引擎以及仅存在于进程内的 Session 和 WebSocket 连接表。
type Server struct {
	engine      Engine
	mu          sync.Mutex
	sessions    map[string]session
	connections map[string]connection
	authSlots   chan struct{}
}

func NewServer(store Store) *Server {
	return &Server{engine: Engine{Store: store, Now: time.Now}, sessions: map[string]session{}, connections: map[string]connection{}, authSlots: make(chan struct{}, 4)}
}
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { s.reply(w, 200, Response{Code: "OK"}) })
	mux.HandleFunc("GET /api/config", func(w http.ResponseWriter, r *http.Request) {
		cfg := GameConfig()
		s.reply(w, 200, Response{Code: "OK", Config: &cfg})
	})
	mux.HandleFunc("POST /api/register", s.register)
	mux.HandleFunc("POST /api/login", s.login)
	mux.HandleFunc("POST /api/logout", s.logout)
	mux.HandleFunc("GET /ws", s.websocket)
	return mux
}
func decodeJSON(body []byte, target any) error {
	// 拒绝未知字段和第二个 JSON 值，让 Go、Vue 与 Qt 严格遵守同一合同。
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		return ErrInvalid
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return ErrInvalid
	}
	return nil
}

type credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func readCredentials(w http.ResponseWriter, r *http.Request) (credentials, error) {
	var c credentials
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return c, ErrInvalid
	}
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	if err != nil {
		return c, ErrInvalid
	}
	if err = decodeJSON(b, &c); err != nil {
		return c, err
	}
	return c, ValidateCredentials(c.Username, c.Password)
}
func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	c, err := readCredentials(w, r)
	if err != nil {
		s.failHTTP(w, err)
		return
	}
	select {
	// 密码派生计算量较大，限制并发可防止登录请求拖垮课堂演示服务。
	case s.authSlots <- struct{}{}:
		defer func() { <-s.authSlots }()
	default:
		s.reply(w, 429, Response{Code: "SERVER_BUSY"})
		return
	}
	hash, err := HashPassword(c.Password)
	if err != nil {
		s.failHTTP(w, err)
		return
	}
	id, err := randomHex(16)
	if err == nil {
		err = s.engine.Store.Create(r.Context(), Account{ID: id, Username: c.Username, PasswordHash: hash}, NewState(id))
	}
	if err != nil {
		s.failHTTP(w, err)
		return
	}
	s.reply(w, 201, Response{Code: "OK", PlayerID: id})
}
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	c, err := readCredentials(w, r)
	if err != nil {
		s.failHTTP(w, err)
		return
	}
	select {
	case s.authSlots <- struct{}{}:
		defer func() { <-s.authSlots }()
	default:
		s.reply(w, 429, Response{Code: "SERVER_BUSY"})
		return
	}
	a, err := s.engine.Store.Find(r.Context(), c.Username)
	if errors.Is(err, ErrCredentials) {
		// 账号不存在时仍执行一次密码派生，减少通过耗时判断账号是否存在的差异。
		_, _ = HashPassword(c.Password)
		s.failHTTP(w, ErrCredentials)
		return
	}
	if err != nil {
		s.failHTTP(w, err)
		return
	}
	if !VerifyPassword(a.PasswordHash, c.Password) {
		s.failHTTP(w, ErrCredentials)
		return
	}
	token, err := randomHex(32)
	if err != nil {
		s.failHTTP(w, err)
		return
	}
	now := time.Now()
	s.mu.Lock()
	for key, value := range s.sessions {
		if now.After(value.Expires) {
			delete(s.sessions, key)
		}
	}
	// Session 只保存在内存中并存活 24 小时，服务重启后需要重新登录。
	s.sessions[token] = session{a.ID, now.Add(24 * time.Hour)}
	s.mu.Unlock()
	s.reply(w, 200, Response{Code: "OK", Token: token, PlayerID: a.ID})
}
func (s *Server) identity(token string) (string, error) {
	// 后续 WebSocket 操作只相信 token 绑定的玩家身份。
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[token]
	if !ok || !time.Now().Before(session.Expires) {
		delete(s.sessions, token)
		return "", ErrUnauthenticated
	}
	return session.PlayerID, nil
}
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	id, err := s.identity(token)
	if err != nil {
		s.failHTTP(w, err)
		return
	}
	s.mu.Lock()
	delete(s.sessions, token)
	c, ok := s.connections[id]
	if ok && c.Token == token {
		delete(s.connections, id)
	}
	s.mu.Unlock()
	if ok && c.Token == token {
		_ = c.Socket.CloseNow()
	}
	s.reply(w, 200, Response{Code: "OK"})
}
func (s *Server) Close() {
	s.mu.Lock()
	connections := s.connections
	s.connections = map[string]connection{}
	s.sessions = map[string]session{}
	s.mu.Unlock()
	for _, c := range connections {
		_ = c.Socket.CloseNow()
	}
}
func (s *Server) reply(w http.ResponseWriter, status int, response Response) {
	response.Type = "response"
	response.ServerTimeMS = time.Now().UnixMilli()
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(response)
}
func errorResponse(err error) Response {
	messages := map[error]string{ErrInvalid: "参数格式不正确", ErrDuplicate: "账号已存在", ErrCredentials: "账号或密码错误", ErrUnauthenticated: "请重新登录", ErrCoins: "金币不足", ErrItems: "物品不足", ErrPlot: "地块状态不允许此操作", ErrNotMature: "作物尚未成熟", ErrCapacity: "仓库已满，请先出售作物", ErrTask: "请先完成全部章节任务", ErrRequestConflict: "同一个请求编号不能用于不同操作", ErrMailNotFound: "邮件不存在"}
	for code, message := range messages {
		if errors.Is(err, code) {
			return Response{Code: code.Error(), Message: message}
		}
	}
	return Response{Code: "SERVICE_UNAVAILABLE", Message: "服务暂时不可用，操作结果可能未确认，请使用同一请求编号重试"}
}
func (s *Server) failHTTP(w http.ResponseWriter, err error) {
	response := errorResponse(err)
	status := 400
	switch response.Code {
	case "INVALID_CREDENTIALS", "UNAUTHENTICATED":
		status = 401
	case "ACCOUNT_EXISTS":
		status = 409
	case "SERVICE_UNAVAILABLE":
		status = 503
	}
	s.reply(w, status, response)
}
