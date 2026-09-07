// Package game 实现单进程课设农场，包含协议模型、业务规则、登录会话和存储适配器。
package game

import (
	"errors"
	"time"
)

var (
	// 业务层用稳定错误码与前端约定失败类型，不让客户端解析中文错误文本。
	ErrInvalid         = errors.New("INVALID_ARGUMENT")
	ErrDuplicate       = errors.New("ACCOUNT_EXISTS")
	ErrUnauthenticated = errors.New("UNAUTHENTICATED")
	ErrCredentials     = errors.New("INVALID_CREDENTIALS")
	ErrCoins           = errors.New("INSUFFICIENT_COINS")
	ErrItems           = errors.New("INSUFFICIENT_ITEMS")
	ErrPlot            = errors.New("PLOT_STATE_CONFLICT")
	ErrNotMature       = errors.New("CROP_NOT_MATURE")
	ErrCapacity        = errors.New("WAREHOUSE_FULL")
	ErrTask            = errors.New("CHAPTER_NOT_CLAIMABLE")
	ErrRequestConflict = errors.New("REQUEST_ID_CONFLICT")
	ErrMailNotFound    = errors.New("MAIL_NOT_FOUND")
)

const WelcomeMailTitle = "欢迎来到经典农场"
const WelcomeMailContent = "欢迎来到经典农场！快去种下你的第一颗胡萝卜吧。"

// Account 只保存认证所需字段；密码明文从不进入存储层。
type Account struct{ ID, Username, PasswordHash string }

// Plot 描述一块地。成熟状态可根据 MatureAtMS 在读取时推导。
type Plot struct {
	ID          int    `json:"plot_id"`
	Status      string `json:"status"`
	PlantedAtMS int64  `json:"planted_at_ms"`
	MatureAtMS  int64  `json:"mature_at_ms"`
	Fertilized  bool   `json:"fertilized"`
}
type Task struct {
	Action  string `json:"action"`
	Label   string `json:"label"`
	Current int    `json:"current"`
	Target  int    `json:"target"`
}
type Receipt struct {
	RequestID   string `json:"request_id"`
	Fingerprint string `json:"fingerprint"`
}
type Mail struct {
	ID          string `json:"mail_id"`
	Title       string `json:"title"`
	Content     string `json:"content"`
	IsRead      bool   `json:"is_read"`
	CreatedAtMS int64  `json:"created_at_ms"`
}

// State 是一个玩家的完整农场存档，MySQL 将它整体保存为 JSON。
type State struct {
	PlayerID   string    `json:"player_id"`
	Version    uint64    `json:"state_version,string"`
	Coins      int       `json:"coins"`
	Seeds      int       `json:"seeds"`
	Fertilizer int       `json:"fertilizer"`
	Crops      int       `json:"crops"`
	Plots      []Plot    `json:"plots"`
	Chapter    int       `json:"chapter"`
	Tasks      []Task    `json:"tasks"`
	Receipts   []Receipt `json:"receipts,omitempty"`
}
type Args struct {
	PlotID   int    `json:"plot_id,omitempty"`
	Quantity int    `json:"quantity,omitempty"`
	Token    string `json:"token,omitempty"`
	MailID   string `json:"mail_id,omitempty"`
}

// Command 是 WebSocket 客户端发送的一条 JSON 指令。
type Command struct {
	RequestID string `json:"request_id"`
	Action    string `json:"action"`
	Data      Args   `json:"data"`
}

// Response 是 HTTP 与 WebSocket 共用的 JSON 响应外壳。
type Response struct {
	Type         string  `json:"type"`
	RequestID    string  `json:"request_id,omitempty"`
	Action       string  `json:"action,omitempty"`
	Code         string  `json:"code"`
	Message      string  `json:"message,omitempty"`
	ServerTimeMS int64   `json:"server_time_ms"`
	Snapshot     *State  `json:"snapshot,omitempty"`
	Token        string  `json:"token,omitempty"`
	PlayerID     string  `json:"player_id,omitempty"`
	Config       *Config `json:"config,omitempty"`
	Mails        []Mail  `json:"mails,omitempty"`
}
type Config struct {
	SeedPrice         int `json:"seed_price"`
	FertilizerPrice   int `json:"fertilizer_price"`
	CropPrice         int `json:"crop_price"`
	GrowthSeconds     int `json:"growth_seconds"`
	FertilizerSeconds int `json:"fertilizer_seconds"`
	Yield             int `json:"yield"`
	Capacity          int `json:"capacity"`
}

// GameConfig 返回由服务端掌握的固定玩法参数，客户端只负责显示。
func GameConfig() Config { return Config{2, 2, 5, 100, 30, 3, 200} }
func newTasks() []Task {
	return []Task{
		{"BUY_SEEDS", "购买 3 颗胡萝卜种子", 0, 3}, {"PLANT", "种植胡萝卜 1 次", 0, 1},
		{"APPLY_FERTILIZER", "施肥 1 次", 0, 1}, {"HARVEST", "收获 1 次", 0, 1}, {"SELL_CROP", "出售 1 份作物", 0, 1},
	}
}

// NewState 创建包含四块空地的新玩家初始存档。
func NewState(id string) State {
	return State{PlayerID: id, Version: 1, Coins: 10, Fertilizer: 1, Chapter: 1, Tasks: newTasks(), Plots: []Plot{
		{ID: 1, Status: "EMPTY"}, {ID: 2, Status: "EMPTY"}, {ID: 3, Status: "EMPTY"}, {ID: 4, Status: "EMPTY"},
	}}
}
func (s State) clone() State {
	s.Plots = append([]Plot(nil), s.Plots...)
	s.Tasks = append([]Task(nil), s.Tasks...)
	s.Receipts = append([]Receipt(nil), s.Receipts...)
	return s
}
func (s State) view(now time.Time) State {
	// 成熟只改变返回视图，不需要后台定时任务每秒更新数据库。
	s = s.clone()
	s.Receipts = nil
	for i := range s.Plots {
		if s.Plots[i].Status == "GROWING" && now.UnixMilli() >= s.Plots[i].MatureAtMS {
			s.Plots[i].Status = "MATURE"
		}
	}
	return s
}
