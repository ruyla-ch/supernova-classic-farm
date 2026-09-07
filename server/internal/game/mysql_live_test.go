package game

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"
)

// Opt-in: touches only a unique test account in the class_mid_* tables.
func TestLiveMySQLRecovery(t *testing.T) {
	dsn := os.Getenv("CLASS_MID_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("CLASS_MID_TEST_MYSQL_DSN not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal("open database failed")
	}
	defer db.Close()
	store := &MySQLStore{DB: db}
	if err = store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	id, err := randomHex(16)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = db.Exec("DELETE FROM class_mid_mails WHERE player_id = ?", id)
		_, _ = db.Exec("DELETE FROM class_mid_states WHERE player_id = ?", id)
		_, _ = db.Exec("DELETE FROM class_mid_accounts WHERE player_id = ?", id)
	}()
	if err = store.Create(ctx, Account{ID: id, Username: "test_" + id[:20], PasswordHash: "test-only"}, NewState(id)); err != nil {
		t.Fatal(err)
	}
	engine := Engine{Store: store, Now: time.Now}
	command := Command{RequestID: "persisted-purchase", Action: "BUY_SEEDS", Data: Args{Quantity: 3}}
	if _, err = engine.Execute(ctx, id, command); err != nil {
		t.Fatal(err)
	}
	// New connection and engine simulate loss of all process memory.
	db2, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	restarted := Engine{Store: &MySQLStore{DB: db2}, Now: time.Now}
	state, err := restarted.Execute(ctx, id, command)
	if err != nil || state.Coins != 4 || state.Seeds != 3 || state.Version != 2 {
		t.Fatalf("recovery/dedup failed: %+v %v", state, err)
	}
	mails, err := restarted.Store.ListMails(ctx, id)
	if err != nil || len(mails) != 1 || mails[0].Title != WelcomeMailTitle || mails[0].IsRead {
		t.Fatalf("welcome mail recovery failed: %+v %v", mails, err)
	}
	mails, err = restarted.Store.MarkMailRead(ctx, id, mails[0].ID)
	if err != nil || len(mails) != 1 || !mails[0].IsRead {
		t.Fatalf("mail read persistence failed: %+v %v", mails, err)
	}
}
