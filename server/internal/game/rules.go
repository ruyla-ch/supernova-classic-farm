package game

// 本文件只处理农场规则和请求去重，不处理网络连接或 SQL。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"time"
)

var requestPattern = regexp.MustCompile(`^[a-zA-Z0-9:_-]{8,64}$`)

// Engine 将协议指令应用到 Store 中对应玩家的状态。
type Engine struct {
	Store Store
	Now   func() time.Time
}

// Execute 是游戏状态的唯一写入口。Store.Update 会锁住玩家状态，提交成功后才返回。
func (e *Engine) Execute(ctx context.Context, id string, c Command) (State, error) {
	if !requestPattern.MatchString(c.RequestID) || c.Data.Token != "" || c.Data.MailID != "" {
		return State{}, ErrInvalid
	}
	now := e.Now()
	if c.Action == "GET_PLAYER_SNAPSHOT" || c.Action == "GET_SHOP" || c.Action == "PING" {
		s, err := e.Store.Read(ctx, id)
		return s.view(now), err
	}
	body, _ := json.Marshal(struct {
		Action string
		Data   Args
	}{c.Action, c.Data})
	digest := sha256.Sum256(body)
	fingerprint := hex.EncodeToString(digest[:])
	s, err := e.Store.Update(ctx, id, func(s *State) error {
		// 去重记录与状态在同一事务保存，断线重试不会重复扣除物品。
		for _, r := range s.Receipts {
			if r.RequestID == c.RequestID {
				if r.Fingerprint != fingerprint {
					return ErrRequestConflict
				}
				return nil
			}
		}
		if err := apply(s, c.Action, c.Data, now); err != nil {
			return err
		}
		s.Version++
		s.Receipts = append(s.Receipts, Receipt{c.RequestID, fingerprint})
		if len(s.Receipts) > 100 {
			s.Receipts = s.Receipts[len(s.Receipts)-100:]
		}
		return nil
	})
	return s.view(now), err
}

func apply(s *State, action string, a Args, now time.Time) error {
	// 所有价格、产量和时间均从服务端配置读取，不能由客户端修改。
	cfg := GameConfig()
	progress := 1
	switch action {
	case "BUY_SEEDS", "BUY_FERTILIZER":
		if a.Quantity < 1 || a.Quantity > 100 || a.PlotID != 0 {
			return ErrInvalid
		}
		price := cfg.SeedPrice
		if action == "BUY_FERTILIZER" {
			price = cfg.FertilizerPrice
		}
		cost := price * a.Quantity
		if s.Coins < cost {
			return ErrCoins
		}
		if s.Seeds+s.Fertilizer+s.Crops+a.Quantity > cfg.Capacity {
			return ErrCapacity
		}
		s.Coins -= cost
		if action == "BUY_SEEDS" {
			s.Seeds += a.Quantity
		} else {
			s.Fertilizer += a.Quantity
		}
		progress = a.Quantity
	case "SELL_CROP":
		if a.Quantity < 1 || a.Quantity > cfg.Capacity || a.PlotID != 0 {
			return ErrInvalid
		}
		if s.Crops < a.Quantity {
			return ErrItems
		}
		s.Crops -= a.Quantity
		s.Coins += cfg.CropPrice * a.Quantity
		progress = a.Quantity
	case "CLAIM_CHAPTER_REWARD":
		if a.Quantity != 0 || a.PlotID != 0 {
			return ErrInvalid
		}
		for _, t := range s.Tasks {
			if t.Current < t.Target {
				return ErrTask
			}
		}
		if s.Seeds+s.Fertilizer+s.Crops+3 > cfg.Capacity {
			return ErrCapacity
		}
		s.Coins += 10
		s.Seeds += 3
		s.Chapter++
		s.Tasks = newTasks()
		return nil
	case "PLANT", "APPLY_FERTILIZER", "HARVEST", "CLEAN_PLOT":
		if a.PlotID < 1 || a.PlotID > len(s.Plots) || a.Quantity != 0 {
			return ErrInvalid
		}
		p := &s.Plots[a.PlotID-1]
		switch action {
		case "PLANT":
			if p.Status != "EMPTY" {
				return ErrPlot
			}
			if s.Seeds < 1 {
				return ErrItems
			}
			s.Seeds--
			*p = Plot{ID: p.ID, Status: "GROWING", PlantedAtMS: now.UnixMilli(), MatureAtMS: now.Add(time.Duration(cfg.GrowthSeconds) * time.Second).UnixMilli()}
		case "APPLY_FERTILIZER":
			if p.Status != "GROWING" || p.Fertilized || now.UnixMilli() >= p.MatureAtMS {
				return ErrPlot
			}
			if s.Fertilizer < 1 {
				return ErrItems
			}
			s.Fertilizer--
			p.Fertilized = true
			p.MatureAtMS -= int64(cfg.FertilizerSeconds) * 1000
			if p.MatureAtMS < now.UnixMilli() {
				p.MatureAtMS = now.UnixMilli()
			}
		case "HARVEST":
			if p.Status != "GROWING" {
				return ErrPlot
			}
			if now.UnixMilli() < p.MatureAtMS {
				return ErrNotMature
			}
			if s.Seeds+s.Fertilizer+s.Crops+cfg.Yield > cfg.Capacity {
				return ErrCapacity
			}
			s.Crops += cfg.Yield
			p.Status = "NEED_CLEANUP"
		case "CLEAN_PLOT":
			if p.Status != "NEED_CLEANUP" {
				return ErrPlot
			}
			*p = Plot{ID: p.ID, Status: "EMPTY"}
		}
	default:
		return ErrInvalid
	}
	for i := range s.Tasks {
		t := &s.Tasks[i]
		if t.Action == action {
			t.Current = min(t.Target, t.Current+progress)
		}
	}
	return nil
}
