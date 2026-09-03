package friend

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestMySQLStoreRedeemReturnsExistingRelationIdempotently(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := NewMySQLStore(db)
	if err != nil {
		t.Fatal(err)
	}
	now := time.UnixMilli(1_800_000_000_000)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT player_id, expires_at_ms`).
		WithArgs("00112233445566778899aabbccddeeff").
		WillReturnRows(sqlmock.NewRows([]string{"player_id", "expires_at_ms"}).
			AddRow(uint64(9), now.Add(time.Hour).UnixMilli()))
	mock.ExpectQuery(`SELECT account_name`).
		WithArgs(uint64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"account_name"}).AddRow("owner"))
	mock.ExpectQuery(`SELECT created_at_ms`).
		WithArgs(uint64(7), uint64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"created_at_ms"}).AddRow(now.Add(-time.Hour).UnixMilli()))
	mock.ExpectCommit()

	result, err := store.RedeemCode(
		context.Background(), 7, "00112233445566778899AABBCCDDEEFF", now,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.NewlyCreated || result.Friend.PlayerID != 9 || result.Friend.AccountName != "owner" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMySQLStoreRedeemCreatesOneUnorderedRelation(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := NewMySQLStore(db)
	if err != nil {
		t.Fatal(err)
	}
	now := time.UnixMilli(1_800_000_000_000)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT player_id, expires_at_ms`).
		WillReturnRows(sqlmock.NewRows([]string{"player_id", "expires_at_ms"}).
			AddRow(uint64(3), now.Add(time.Hour).UnixMilli()))
	mock.ExpectQuery(`SELECT account_name`).
		WithArgs(uint64(3)).
		WillReturnRows(sqlmock.NewRows([]string{"account_name"}).AddRow("friend"))
	mock.ExpectQuery(`SELECT created_at_ms`).
		WithArgs(uint64(3), uint64(8)).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`SELECT COUNT\(\*\)`).
		WithArgs(uint64(3), uint64(3)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
	mock.ExpectQuery(`SELECT COUNT\(\*\)`).
		WithArgs(uint64(8), uint64(8)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(4))
	mock.ExpectExec(`INSERT INTO friend_relations`).
		WithArgs(uint64(3), uint64(8), now.UnixMilli(), now.UnixMilli()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	result, err := store.RedeemCode(
		context.Background(), 8, "00112233445566778899aabbccddeeff", now,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !result.NewlyCreated || result.Friend.PlayerID != 3 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMySQLStoreListOrdersAndProjectsPeer(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := NewMySQLStore(db)
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery(`SELECT\s+CASE WHEN r.player_low_id`).
		WithArgs(uint64(7), uint64(7), uint64(7), uint64(7), FriendLimit).
		WillReturnRows(sqlmock.NewRows([]string{"player_id", "account_name", "created_at_ms"}).
			AddRow(uint64(9), "nine", int64(10)).
			AddRow(uint64(3), "three", int64(20)))
	views, err := store.List(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 2 || views[0].PlayerID != 9 || views[1].AccountName != "three" {
		t.Fatalf("unexpected views: %+v", views)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
