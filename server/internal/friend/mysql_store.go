package friend

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

const operationTimeout = 5 * time.Second

type MySQLStore struct {
	db *sql.DB
}

func NewMySQLStore(db *sql.DB) (*MySQLStore, error) {
	if db == nil {
		return nil, errors.New("MySQL database is required")
	}
	return &MySQLStore{db: db}, nil
}

func (s *MySQLStore) CreateCode(parent context.Context, playerID uint64, now time.Time) (Code, error) {
	if playerID == 0 {
		return Code{}, errors.New("player_id is required")
	}
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return Code{}, fmt.Errorf("generate friend code: %w", err)
	}
	code := Code{
		Value: hex.EncodeToString(raw[:]), CreatedAt: now.UTC(),
		ExpiresAt: now.UTC().Add(CodeTTL),
	}
	ctx, cancel := context.WithTimeout(parent, operationTimeout)
	defer cancel()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO friend_codes (player_id, code, created_at_ms, expires_at_ms)
		VALUES (?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE
			code = VALUES(code),
			created_at_ms = VALUES(created_at_ms),
			expires_at_ms = VALUES(expires_at_ms)`,
		playerID, code.Value, code.CreatedAt.UnixMilli(), code.ExpiresAt.UnixMilli(),
	)
	if err != nil {
		return Code{}, fmt.Errorf("save friend code: %w", err)
	}
	return code, nil
}

func (s *MySQLStore) RedeemCode(
	parent context.Context,
	callerID uint64,
	rawCode string,
	now time.Time,
) (RedeemResult, error) {
	code := strings.ToLower(strings.TrimSpace(rawCode))
	if callerID == 0 || len(code) != 32 {
		return RedeemResult{}, ErrCodeNotFound
	}
	ctx, cancel := context.WithTimeout(parent, operationTimeout)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return RedeemResult{}, fmt.Errorf("begin redeem friend code: %w", err)
	}
	defer tx.Rollback()

	var ownerID uint64
	var expiresAtMS int64
	err = tx.QueryRowContext(ctx, `
		SELECT player_id, expires_at_ms
		FROM friend_codes
		WHERE code = ?
		FOR UPDATE`, code,
	).Scan(&ownerID, &expiresAtMS)
	if errors.Is(err, sql.ErrNoRows) {
		return RedeemResult{}, ErrCodeNotFound
	}
	if err != nil {
		return RedeemResult{}, fmt.Errorf("load friend code: %w", err)
	}
	if !now.Before(time.UnixMilli(expiresAtMS)) {
		return RedeemResult{}, ErrCodeExpired
	}
	if ownerID == callerID {
		return RedeemResult{}, ErrCannotSelf
	}
	low, high := orderedPair(callerID, ownerID)

	var friendName string
	err = tx.QueryRowContext(ctx, `
		SELECT account_name
		FROM accounts
		WHERE player_id = ? AND status = 'ACTIVE'
		FOR UPDATE`, ownerID,
	).Scan(&friendName)
	if errors.Is(err, sql.ErrNoRows) {
		return RedeemResult{}, ErrCodeNotFound
	}
	if err != nil {
		return RedeemResult{}, fmt.Errorf("load friend account: %w", err)
	}

	var createdAtMS int64
	err = tx.QueryRowContext(ctx, `
		SELECT created_at_ms
		FROM friend_relations
		WHERE player_low_id = ? AND player_high_id = ? AND status = 'ACTIVE'
		FOR UPDATE`, low, high,
	).Scan(&createdAtMS)
	if err == nil {
		if commitErr := tx.Commit(); commitErr != nil {
			return RedeemResult{}, fmt.Errorf("commit existing relation: %w", commitErr)
		}
		return RedeemResult{
			Friend: View{PlayerID: ownerID, AccountName: friendName, CreatedAt: time.UnixMilli(createdAtMS)},
		}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return RedeemResult{}, fmt.Errorf("load friend relation: %w", err)
	}

	for _, playerID := range []uint64{low, high} {
		var count uint32
		if err := tx.QueryRowContext(ctx, `
			SELECT COUNT(*)
			FROM friend_relations
			WHERE status = 'ACTIVE' AND (player_low_id = ? OR player_high_id = ?)
			FOR UPDATE`, playerID, playerID,
		).Scan(&count); err != nil {
			return RedeemResult{}, fmt.Errorf("count friends: %w", err)
		}
		if count >= FriendLimit {
			return RedeemResult{}, ErrFriendLimit
		}
	}
	createdAtMS = now.UnixMilli()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO friend_relations (
			player_low_id, player_high_id, status, created_at_ms, updated_at_ms
		) VALUES (?, ?, 'ACTIVE', ?, ?)`,
		low, high, createdAtMS, createdAtMS,
	); err != nil {
		return RedeemResult{}, fmt.Errorf("insert friend relation: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return RedeemResult{}, fmt.Errorf("commit friend relation: %w", err)
	}
	return RedeemResult{
		Friend:       View{PlayerID: ownerID, AccountName: friendName, CreatedAt: now.UTC()},
		NewlyCreated: true,
	}, nil
}

func (s *MySQLStore) List(parent context.Context, playerID uint64) ([]View, error) {
	if playerID == 0 {
		return nil, errors.New("player_id is required")
	}
	ctx, cancel := context.WithTimeout(parent, operationTimeout)
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `
		SELECT
			CASE WHEN r.player_low_id = ? THEN r.player_high_id ELSE r.player_low_id END,
			a.account_name,
			r.created_at_ms
		FROM friend_relations r
		JOIN accounts a ON a.player_id =
			CASE WHEN r.player_low_id = ? THEN r.player_high_id ELSE r.player_low_id END
		WHERE r.status = 'ACTIVE'
		  AND (r.player_low_id = ? OR r.player_high_id = ?)
		  AND a.status = 'ACTIVE'
		ORDER BY r.created_at_ms, a.player_id
		LIMIT ?`,
		playerID, playerID, playerID, playerID, FriendLimit,
	)
	if err != nil {
		return nil, fmt.Errorf("list friends: %w", err)
	}
	defer rows.Close()
	friends := make([]View, 0)
	for rows.Next() {
		var view View
		var createdAtMS int64
		if err := rows.Scan(&view.PlayerID, &view.AccountName, &createdAtMS); err != nil {
			return nil, fmt.Errorf("scan friend: %w", err)
		}
		view.CreatedAt = time.UnixMilli(createdAtMS)
		friends = append(friends, view)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate friends: %w", err)
	}
	return friends, nil
}

func (s *MySQLStore) CheckMutual(
	parent context.Context,
	firstPlayerID, secondPlayerID uint64,
) (bool, error) {
	if firstPlayerID == 0 || secondPlayerID == 0 || firstPlayerID == secondPlayerID {
		return false, nil
	}
	low, high := orderedPair(firstPlayerID, secondPlayerID)
	ctx, cancel := context.WithTimeout(parent, operationTimeout)
	defer cancel()
	var present uint8
	err := s.db.QueryRowContext(ctx, `
		SELECT 1
		FROM friend_relations
		WHERE player_low_id = ? AND player_high_id = ? AND status = 'ACTIVE'
		LIMIT 1`, low, high,
	).Scan(&present)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check mutual friend: %w", err)
	}
	return present == 1, nil
}

func orderedPair(a, b uint64) (uint64, uint64) {
	if a < b {
		return a, b
	}
	return b, a
}
