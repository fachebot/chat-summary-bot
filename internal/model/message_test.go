package model

import (
	"context"
	"testing"
	"time"

	"github.com/fachebot/chat-summary-bot/internal/ent/enttest"
	"github.com/fachebot/chat-summary-bot/internal/ent/message"
	_ "github.com/mattn/go-sqlite3"
)

func TestMessageModelCreate_IsIdempotent(t *testing.T) {
	t.Parallel()

	client := enttest.Open(t, "sqlite3", "file:message-model-idempotent?mode=memory&cache=shared&_fk=1")
	defer client.Close()

	model := NewMessageModel(client.Message)
	ctx := context.Background()
	sentAt := time.Date(2026, 5, 9, 0, 0, 0, 0, time.UTC)
	data := &MessageData{
		MessageID:  1001,
		ChatID:     -2001,
		SenderID:   42,
		SenderName: "alice",
		Text:       "hello",
		SentAt:     sentAt,
	}

	first, err := model.Create(ctx, data)
	if err != nil {
		t.Fatalf("first create failed: %v", err)
	}

	second, err := model.Create(ctx, data)
	if err != nil {
		t.Fatalf("second create failed: %v", err)
	}

	count, err := client.Message.Query().Count(ctx)
	if err != nil {
		t.Fatalf("count failed: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 row, got %d", count)
	}
	if first.ID != second.ID {
		t.Fatalf("expected the same row to be returned, got %d and %d", first.ID, second.ID)
	}

	stored, err := client.Message.Query().
		Where(
			message.ChatIDEQ(data.ChatID),
			message.MessageIDEQ(data.MessageID),
		).
		Only(ctx)
	if err != nil {
		t.Fatalf("query stored message failed: %v", err)
	}
	if stored.Text != data.Text {
		t.Fatalf("expected stored text %q, got %q", data.Text, stored.Text)
	}
}

func TestMessageModelGetLatestMessageIDsByChat(t *testing.T) {
	t.Parallel()

	client := enttest.Open(t, "sqlite3", "file:message-model-latest-snapshot?mode=memory&cache=shared&_fk=1")
	defer client.Close()

	model := NewMessageModel(client.Message)
	ctx := context.Background()
	baseTime := time.Date(2026, 5, 9, 0, 0, 0, 0, time.UTC)

	for _, data := range []*MessageData{
		{MessageID: 1001, ChatID: -2001, SenderID: 1, SenderName: "alice", Text: "a", SentAt: baseTime},
		{MessageID: 1003, ChatID: -2001, SenderID: 1, SenderName: "alice", Text: "b", SentAt: baseTime.Add(time.Minute)},
		{MessageID: 1002, ChatID: -2001, SenderID: 1, SenderName: "alice", Text: "c", SentAt: baseTime.Add(2 * time.Minute)},
		{MessageID: 2005, ChatID: -2002, SenderID: 2, SenderName: "bob", Text: "d", SentAt: baseTime},
		{MessageID: 2007, ChatID: -2002, SenderID: 2, SenderName: "bob", Text: "e", SentAt: baseTime.Add(time.Minute)},
	} {
		if _, err := model.Create(ctx, data); err != nil {
			t.Fatalf("create failed: %v", err)
		}
	}

	latestByChat, err := model.GetLatestMessageIDsByChat(ctx)
	if err != nil {
		t.Fatalf("GetLatestMessageIDsByChat failed: %v", err)
	}

	if got := latestByChat[-2001]; got != 1003 {
		t.Fatalf("expected latest message for -2001 to be 1003, got %d", got)
	}
	if got := latestByChat[-2002]; got != 2007 {
		t.Fatalf("expected latest message for -2002 to be 2007, got %d", got)
	}
}
