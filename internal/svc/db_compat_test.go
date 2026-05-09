package svc

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

func TestEnsureMessageTableForwardCompatibility_RemovesDuplicateMessages(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "compat.db")
	dataSourceName := "file:" + dbPath + "?mode=rwc&_fk=1"

	db, err := sql.Open("sqlite3", dataSourceName)
	if err != nil {
		t.Fatalf("open sqlite failed: %v", err)
	}
	defer db.Close()

	_, err = db.Exec(`
CREATE TABLE messages (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	create_time DATETIME NOT NULL,
	update_time DATETIME NOT NULL,
	message_id INTEGER NOT NULL,
	chat_id INTEGER NOT NULL,
	sender_id INTEGER NOT NULL,
	sender_name TEXT NOT NULL,
	sender_username TEXT,
	text TEXT NOT NULL,
	sent_at DATETIME NOT NULL
)
`)
	if err != nil {
		t.Fatalf("create messages table failed: %v", err)
	}

	now := time.Date(2026, 5, 9, 0, 0, 0, 0, time.UTC)
	for _, row := range []struct {
		messageID int64
		chatID    int64
		text      string
	}{
		{messageID: 1001, chatID: -2001, text: "first"},
		{messageID: 1001, chatID: -2001, text: "duplicate"},
		{messageID: 1002, chatID: -2001, text: "second"},
	} {
		_, err = db.Exec(`
INSERT INTO messages (create_time, update_time, message_id, chat_id, sender_id, sender_name, text, sent_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
`, now, now, row.messageID, row.chatID, 42, "alice", row.text, now)
		if err != nil {
			t.Fatalf("insert row failed: %v", err)
		}
	}

	if err := ensureMessageTableForwardCompatibility(context.Background(), "sqlite3", dataSourceName); err != nil {
		t.Fatalf("compat step failed: %v", err)
	}

	var rowCount int
	err = db.QueryRow(`SELECT COUNT(1) FROM messages`).Scan(&rowCount)
	if err != nil {
		t.Fatalf("count rows failed: %v", err)
	}
	if rowCount != 2 {
		t.Fatalf("expected 2 rows after dedupe, got %d", rowCount)
	}

	var duplicateGroups int
	err = db.QueryRow(`
SELECT COUNT(1)
FROM (
	SELECT chat_id, message_id
	FROM messages
	GROUP BY chat_id, message_id
	HAVING COUNT(1) > 1
)
`).Scan(&duplicateGroups)
	if err != nil {
		t.Fatalf("count duplicate groups failed: %v", err)
	}
	if duplicateGroups != 0 {
		t.Fatalf("expected no duplicate groups, got %d", duplicateGroups)
	}
}
