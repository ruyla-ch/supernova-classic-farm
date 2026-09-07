package game

// 本文件定义业务层存储接口，并提供退出即丢失数据的内存实现。

import (
	"context"
	"strconv"
	"sync"
	"time"
)

// Store 隔离业务规则与具体数据库；MySQL 和内存模式遵守同一语义。
type Store interface {
	Create(context.Context, Account, State) error
	Find(context.Context, string) (Account, error)
	Read(context.Context, string) (State, error)
	Update(context.Context, string, func(*State) error) (State, error)
	ListMails(context.Context, string) ([]Mail, error)
	MarkMailRead(context.Context, string, string) ([]Mail, error)
}

// MemoryStore 仅供本地演示和测试，进程退出后数据全部丢失。
type MemoryStore struct {
	mu       sync.Mutex
	accounts map[string]Account
	states   map[string]State
	mails    map[string][]Mail
	nextMail uint64
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{accounts: map[string]Account{}, states: map[string]State{}, mails: map[string][]Mail{}, nextMail: 1}
}
func (s *MemoryStore) Create(ctx context.Context, a Account, state State) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.accounts[a.Username]; ok {
		return ErrDuplicate
	}
	s.accounts[a.Username] = a
	s.states[a.ID] = state.clone()
	s.mails[a.ID] = []Mail{{
		ID: strconv.FormatUint(s.nextMail, 10), Title: WelcomeMailTitle,
		Content: WelcomeMailContent, CreatedAtMS: time.Now().UnixMilli(),
	}}
	s.nextMail++
	return nil
}
func (s *MemoryStore) Find(ctx context.Context, name string) (Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.accounts[name]
	if !ok {
		return Account{}, ErrCredentials
	}
	return a, nil
}
func (s *MemoryStore) Read(ctx context.Context, id string) (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, ok := s.states[id]
	if !ok {
		return State{}, ErrUnauthenticated
	}
	return state.clone(), nil
}
func (s *MemoryStore) Update(ctx context.Context, id string, fn func(*State) error) (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// 修改副本成功后才覆盖原状态，业务失败不会留下部分修改。
	state, ok := s.states[id]
	if !ok {
		return State{}, ErrUnauthenticated
	}
	state = state.clone()
	if err := fn(&state); err != nil {
		return State{}, err
	}
	s.states[id] = state
	return state.clone(), nil
}

func (s *MemoryStore) ListMails(ctx context.Context, playerID string) ([]Mail, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Mail(nil), s.mails[playerID]...), nil
}

func (s *MemoryStore) MarkMailRead(ctx context.Context, playerID, mailID string) ([]Mail, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// 玩家身份由连接确定；必须同时匹配玩家和邮件，防止读取他人的邮件。
	for i := range s.mails[playerID] {
		if s.mails[playerID][i].ID == mailID {
			s.mails[playerID][i].IsRead = true
			return append([]Mail(nil), s.mails[playerID]...), nil
		}
	}
	return nil, ErrMailNotFound
}
