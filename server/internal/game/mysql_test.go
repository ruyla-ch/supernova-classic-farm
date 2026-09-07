package game

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/DATA-DOG/go-sqlmock"
	"testing"
)

func TestMySQLCreateWelcomeCommit(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := &MySQLStore{DB: db}
	account := Account{ID: "player-a", Username: "alice", PasswordHash: "hash"}
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO class_mid_accounts").WithArgs(account.ID, account.Username, account.PasswordHash).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO class_mid_states").WithArgs(account.ID, sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO class_mid_mails").WithArgs(account.ID, WelcomeMailTitle, WelcomeMailContent, sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	if err := store.Create(context.Background(), account, NewState(account.ID)); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMySQLCreateWelcomeRollback(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := &MySQLStore{DB: db}
	account := Account{ID: "player-a", Username: "alice", PasswordHash: "hash"}
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO class_mid_accounts").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO class_mid_states").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO class_mid_mails").WithArgs(account.ID, WelcomeMailTitle, WelcomeMailContent, sqlmock.AnyArg()).WillReturnError(errors.New("mail insert failed"))
	mock.ExpectRollback()
	if err := store.Create(context.Background(), account, NewState(account.ID)); err == nil {
		t.Fatal("mail failure should roll registration back")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMySQLMailboxOwnership(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := &MySQLStore{DB: db}
	rows := sqlmock.NewRows([]string{"mail_id", "title", "content", "is_read", "created_at_ms"}).AddRow(uint64(7), WelcomeMailTitle, WelcomeMailContent, false, int64(123))
	mock.ExpectQuery("SELECT mail_id, title, content, is_read, created_at_ms.*WHERE player_id = \\?.*ORDER BY mail_id DESC").WithArgs("player-a").WillReturnRows(rows)
	mails, err := store.ListMails(context.Background(), "player-a")
	if err != nil || len(mails) != 1 || mails[0].ID != "7" {
		t.Fatalf("list mismatch: %+v %v", mails, err)
	}
	mock.ExpectExec("UPDATE class_mid_mails.*WHERE mail_id = \\? AND player_id = \\?").WithArgs("7", "player-b").WillReturnResult(sqlmock.NewResult(0, 0))
	if _, err := store.MarkMailRead(context.Background(), "player-b", "7"); !errors.Is(err, ErrMailNotFound) {
		t.Fatalf("cross-player update returned %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMySQLTransactionRollbackAndCommit(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "commit", true: "rollback"}[fail], func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			store := &MySQLStore{DB: db}
			body, _ := json.Marshal(NewState("1"))
			mock.ExpectBegin()
			mock.ExpectQuery("SELECT state_json.*FOR UPDATE").WithArgs("1").WillReturnRows(sqlmock.NewRows([]string{"state_json"}).AddRow(body))
			if fail {
				mock.ExpectExec("UPDATE class_mid_states").WithArgs(sqlmock.AnyArg(), "1").WillReturnError(errors.New("disk full"))
				mock.ExpectRollback()
			} else {
				mock.ExpectExec("UPDATE class_mid_states").WithArgs(sqlmock.AnyArg(), "1").WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectCommit()
			}
			state, err := store.Update(context.Background(), "1", func(s *State) error { s.Coins = 4; return nil })
			if fail && err == nil {
				t.Fatal("reported success after failed persistence")
			}
			if !fail && (err != nil || state.Coins != 4) {
				t.Fatal(state, err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
