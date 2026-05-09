package svc

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/fachebot/chat-summary-bot/internal/ent/message"
	"github.com/fachebot/chat-summary-bot/internal/logger"
)

func ensureMessageTableForwardCompatibility(ctx context.Context, driverName, dataSourceName string) error {
	db, err := sql.Open(driverName, dataSourceName)
	if err != nil {
		return err
	}
	defer db.Close()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	exists, err := sqliteTableExists(ctx, tx, message.Table)
	if err != nil {
		return err
	}
	if !exists {
		return tx.Commit()
	}

	result, err := tx.ExecContext(ctx, fmt.Sprintf(`
DELETE FROM %s
WHERE id IN (
	SELECT duplicate_rows.id
	FROM %s AS duplicate_rows
	JOIN (
		SELECT chat_id, message_id, MIN(id) AS keep_id
		FROM %s
		GROUP BY chat_id, message_id
		HAVING COUNT(*) > 1
	) AS grouped
		ON grouped.chat_id = duplicate_rows.chat_id
		AND grouped.message_id = duplicate_rows.message_id
	WHERE duplicate_rows.id <> grouped.keep_id
)
`, message.Table, message.Table, message.Table))
	if err != nil {
		return err
	}

	deletedRows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if deletedRows > 0 {
		logger.Warnf("[ServiceContext] 启动前清理重复消息完成: 删除 %d 条重复记录", deletedRows)
	}

	return tx.Commit()
}

func sqliteTableExists(ctx context.Context, tx *sql.Tx, tableName string) (bool, error) {
	var count int
	err := tx.QueryRowContext(ctx, `
SELECT COUNT(1)
FROM sqlite_master
WHERE type = 'table' AND name = ?
`, tableName).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}
