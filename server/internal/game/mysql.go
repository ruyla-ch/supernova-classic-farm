package game

// 本文件把 Store 接口映射到三张 class_mid_* MySQL 表。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/go-sql-driver/mysql"
	"strconv"
	"time"
)

// MySQLStore 使用 database/sql 连接池持久化账号、存档和邮件。
type MySQLStore struct{ DB *sql.DB }

// Init 只创建 class_mid 独立表，不修改 main 分支遗留表和数据。
func (s *MySQLStore) Init(ctx context.Context) error {
	for _, query := range []string{
		`CREATE TABLE IF NOT EXISTS class_mid_accounts (player_id VARCHAR(32) PRIMARY KEY, username VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL UNIQUE, password_hash VARCHAR(255) NOT NULL) ENGINE=InnoDB`,
		`CREATE TABLE IF NOT EXISTS class_mid_states (player_id VARCHAR(32) PRIMARY KEY, state_json JSON NOT NULL, FOREIGN KEY (player_id) REFERENCES class_mid_accounts(player_id)) ENGINE=InnoDB`,
		`CREATE TABLE IF NOT EXISTS class_mid_mails (mail_id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY, player_id VARCHAR(32) NOT NULL, title VARCHAR(100) NOT NULL, content TEXT NOT NULL, is_read BOOLEAN NOT NULL DEFAULT FALSE, created_at_ms BIGINT NOT NULL, INDEX idx_class_mid_mails_player (player_id, mail_id), FOREIGN KEY (player_id) REFERENCES class_mid_accounts(player_id) ON DELETE CASCADE) ENGINE=InnoDB`,
	} {
		if _, err := s.DB.ExecContext(ctx, query); err != nil {
			return err
		}
	}
	return nil
}
func (s *MySQLStore) Create(ctx context.Context, a Account, state State) error {
	// 账号、初始农场和欢迎邮件必须在同一事务中成功，避免出现残缺的新玩家。
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "INSERT INTO class_mid_accounts (player_id, username, password_hash) VALUES (?, ?, ?)", a.ID, a.Username, a.PasswordHash); err != nil {
		var mysqlErr *mysql.MySQLError
		if errors.As(err, &mysqlErr) && mysqlErr.Number == 1062 {
			return ErrDuplicate
		}
		return err
	}
	body, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO class_mid_states (player_id, state_json) VALUES (?, ?)", a.ID, body); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO class_mid_mails (player_id, title, content, created_at_ms) VALUES (?, ?, ?, ?)", a.ID, WelcomeMailTitle, WelcomeMailContent, time.Now().UnixMilli()); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *MySQLStore) Find(ctx context.Context, name string) (Account, error) {
	var a Account
	err := s.DB.QueryRowContext(ctx, "SELECT player_id, username, password_hash FROM class_mid_accounts WHERE username = ?", name).Scan(&a.ID, &a.Username, &a.PasswordHash)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrCredentials
	}
	return a, err
}
func (s *MySQLStore) Read(ctx context.Context, id string) (State, error) {
	var body []byte
	var s1 State
	err := s.DB.QueryRowContext(ctx, "SELECT state_json FROM class_mid_states WHERE player_id = ?", id).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return s1, ErrUnauthenticated
	}
	if err != nil {
		return s1, err
	}
	err = json.Unmarshal(body, &s1)
	return s1, err
}
func (s *MySQLStore) Update(ctx context.Context, id string, fn func(*State) error) (State, error) {
	var state State
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return state, err
	}
	defer tx.Rollback()
	var body []byte
	// 行锁让同一玩家的写操作依次执行，不同玩家仍可并行。
	err = tx.QueryRowContext(ctx, "SELECT state_json FROM class_mid_states WHERE player_id = ? FOR UPDATE", id).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return state, ErrUnauthenticated
	}
	if err != nil {
		return state, err
	}
	if err = json.Unmarshal(body, &state); err != nil {
		return State{}, err
	}
	if err = fn(&state); err != nil {
		return State{}, err
	}
	body, err = json.Marshal(state)
	if err != nil {
		return State{}, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE class_mid_states SET state_json = ? WHERE player_id = ?", body, id); err != nil {
		return State{}, err
	}
	if err = tx.Commit(); err != nil {
		return State{}, err
	}
	return state, nil
}

func (s *MySQLStore) ListMails(ctx context.Context, playerID string) ([]Mail, error) {
	// playerID 来自认证连接，查询不会返回其他玩家的邮件。
	rows, err := s.DB.QueryContext(ctx, `SELECT mail_id, title, content, is_read, created_at_ms
FROM class_mid_mails
WHERE player_id = ?
ORDER BY mail_id DESC`, playerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var mails []Mail
	for rows.Next() {
		var mail Mail
		var id uint64
		if err := rows.Scan(&id, &mail.Title, &mail.Content, &mail.IsRead, &mail.CreatedAtMS); err != nil {
			return nil, err
		}
		mail.ID = strconv.FormatUint(id, 10)
		mails = append(mails, mail)
	}
	return mails, rows.Err()
}

func (s *MySQLStore) MarkMailRead(ctx context.Context, playerID, mailID string) ([]Mail, error) {
	// 同时校验 mail_id 与 player_id，数据库层也不允许玩家操作他人的邮件。
	result, err := s.DB.ExecContext(ctx, `UPDATE class_mid_mails
SET is_read = TRUE
WHERE mail_id = ? AND player_id = ?`, mailID, playerID)
	if err != nil {
		return nil, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if count != 1 {
		return nil, ErrMailNotFound
	}
	return s.ListMails(ctx, playerID)
}
