// Package friend owns mutual friend relationships and share codes.
package friend

import (
	"context"
	"errors"
	"time"
)

const (
	CodeTTL     = 24 * time.Hour
	FriendLimit = 100
)

var (
	ErrCodeNotFound = errors.New("friend code not found")
	ErrCodeExpired  = errors.New("friend code expired")
	ErrCannotSelf   = errors.New("cannot friend self")
	ErrFriendLimit  = errors.New("friend limit reached")
)

type Code struct {
	Value     string
	CreatedAt time.Time
	ExpiresAt time.Time
}

type View struct {
	PlayerID    uint64
	AccountName string
	CreatedAt   time.Time
}

type RedeemResult struct {
	Friend       View
	NewlyCreated bool
}

type Store interface {
	CreateCode(context.Context, uint64, time.Time) (Code, error)
	RedeemCode(context.Context, uint64, string, time.Time) (RedeemResult, error)
	List(context.Context, uint64) ([]View, error)
	CheckMutual(context.Context, uint64, uint64) (bool, error)
}
