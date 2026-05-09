package teleapp

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/fachebot/chat-summary-bot/internal/logger"
	"github.com/fachebot/chat-summary-bot/internal/model"

	"github.com/zelenin/go-tdlib/client"
)

const historySyncPageSize int32 = 100

type historyCatchUpPlan struct {
	ChatID     int64
	ChatTitle  string
	Checkpoint int64
}

type historyCatchUpResult struct {
	Plan              historyCatchUpPlan
	CandidateCount    int
	SavedCount        int
	FirstNewMessageID int64
	LastNewMessageID  int64
	Duration          time.Duration
}

type historyCatchUpFailure struct {
	Plan   historyCatchUpPlan
	Reason string
}

func (app *TeleApp) startHistoryCatchUp(historyCheckpoints map[int64]int64) {
	if app.tdClient == nil || app.svcCtx == nil || app.svcCtx.MessageModel == nil {
		return
	}

	app.ctxMu.Lock()
	ctx := app.ctx
	app.ctxMu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	if len(historyCheckpoints) == 0 {
		logger.Infof("[TeleApp] 启动历史补拉跳过: 数据库中没有可用 checkpoint")
		return
	}

	go func() {
		rawChatIDs := mapKeys(historyCheckpoints)
		trackedChatIDs := filterCatchUpChatIDs(rawChatIDs, app.svcCtx.Config.Summary.ShouldSaveMessage)
		if len(trackedChatIDs) == 0 {
			logger.Infof("[TeleApp] 启动历史补拉跳过: snapshot 群组 %d 个，但均被白名单/黑名单过滤", len(rawChatIDs))
			return
		}

		sort.Slice(trackedChatIDs, func(i, j int) bool {
			return trackedChatIDs[i] < trackedChatIDs[j]
		})

		plans := app.buildHistoryCatchUpPlans(trackedChatIDs, historyCheckpoints)
		logger.Infof("[TeleApp] 启动历史补拉\n%s", formatHistoryCatchUpPlanPanel(len(rawChatIDs), plans))

		totalSaved := 0
		successCount := 0
		failedCount := 0
		results := make([]historyCatchUpResult, 0, len(plans))
		failures := make([]historyCatchUpFailure, 0)

		for index, plan := range plans {
			if ctx.Err() != nil {
				logger.Warnf("[TeleApp] 历史补拉中断: 已处理 %d/%d 个群组", index, len(plans))
				return
			}

			logger.Infof(
				"[TeleApp] 历史补拉开始 (%d/%d): %s[%d], checkpoint=%d",
				index+1,
				len(plans),
				plan.ChatTitle,
				plan.ChatID,
				plan.Checkpoint,
			)

			result, err := app.syncMissedMessagesForChat(ctx, plan)
			if err != nil {
				failedCount++
				reason := formatCatchUpError(err)
				failures = append(failures, historyCatchUpFailure{
					Plan:   plan,
					Reason: reason,
				})
				logger.Warnf(
					"[TeleApp] 历史补拉失败 (%d/%d): %s[%d], checkpoint=%d, err=%v",
					index+1,
					len(plans),
					plan.ChatTitle,
					plan.ChatID,
					plan.Checkpoint,
					err,
				)
				continue
			}

			successCount++
			totalSaved += result.SavedCount
			results = append(results, result)
			logger.Infof(
				"[TeleApp] 历史补拉完成 (%d/%d): %s[%d], checkpoint=%d, 候选=%d, 入库=%d, 新消息范围=%s, 耗时=%s",
				index+1,
				len(plans),
				result.Plan.ChatTitle,
				result.Plan.ChatID,
				result.Plan.Checkpoint,
				result.CandidateCount,
				result.SavedCount,
				formatCatchUpMessageRange(result.FirstNewMessageID, result.LastNewMessageID),
				result.Duration.Round(time.Millisecond),
			)
		}

		logger.Infof(
			"[TeleApp] 历史补拉总览\n%s",
			formatHistoryCatchUpSummaryPanel(len(rawChatIDs), len(plans), successCount, failedCount, totalSaved, results, failures),
		)
	}()
}

func (app *TeleApp) buildHistoryCatchUpPlans(chatIDs []int64, historyCheckpoints map[int64]int64) []historyCatchUpPlan {
	plans := make([]historyCatchUpPlan, 0, len(chatIDs))
	for _, chatID := range chatIDs {
		plan := historyCatchUpPlan{
			ChatID:     chatID,
			ChatTitle:  app.historyCatchUpChatTitle(chatID),
			Checkpoint: historyCheckpoints[chatID],
		}
		plans = append(plans, plan)
	}
	return plans
}

func (app *TeleApp) historyCatchUpChatTitle(chatID int64) string {
	chat, err := app.getChat(chatID)
	if err != nil {
		logger.Warnf("[TeleApp] 获取历史补拉群组标题失败, id: %d, %v", chatID, err)
		return "<unknown>"
	}
	if chat == nil || chat.Title == "" {
		return "<unknown>"
	}
	return chat.Title
}

func mapKeys(values map[int64]int64) []int64 {
	keys := make([]int64, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}

func filterCatchUpChatIDs(chatIDs []int64, shouldSave func(chatID int64) bool) []int64 {
	filtered := make([]int64, 0, len(chatIDs))
	seen := make(map[int64]struct{}, len(chatIDs))
	for _, chatID := range chatIDs {
		if !shouldSave(chatID) {
			continue
		}
		if _, exists := seen[chatID]; exists {
			continue
		}
		seen[chatID] = struct{}{}
		filtered = append(filtered, chatID)
	}
	return filtered
}

func (app *TeleApp) syncMissedMessagesForChat(ctx context.Context, plan historyCatchUpPlan) (historyCatchUpResult, error) {
	result := historyCatchUpResult{Plan: plan}
	if plan.Checkpoint == 0 {
		return result, nil
	}

	startedAt := time.Now()
	if _, err := app.tdClient.OpenChat(&client.OpenChatRequest{ChatId: plan.ChatID}); err != nil {
		logger.Warnf("[TeleApp] 打开群组 %d 进行补拉失败，继续尝试读取历史: %v", plan.ChatID, err)
	}

	missedMessages, err := app.loadMessagesAfter(ctx, plan.ChatID, plan.Checkpoint)
	if err != nil {
		return result, err
	}
	result.CandidateCount = len(missedMessages)
	if len(missedMessages) == 0 {
		result.Duration = time.Since(startedAt)
		return result, nil
	}

	result.FirstNewMessageID, result.LastNewMessageID = messageIDRange(missedMessages)

	storedCount := 0
	for i := len(missedMessages) - 1; i >= 0; i-- {
		saved, err := app.saveGroupTextMessage(ctx, missedMessages[i], false)
		if err != nil {
			return result, err
		}
		if saved {
			storedCount++
		}
	}

	result.SavedCount = storedCount
	result.Duration = time.Since(startedAt)
	return result, nil
}

func (app *TeleApp) loadMessagesAfter(ctx context.Context, chatID, lastSavedMessageID int64) ([]*client.Message, error) {
	fromMessageID := int64(0)
	collected := make([]*client.Message, 0)

	for {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		history, err := app.tdClient.GetChatHistory(&client.GetChatHistoryRequest{
			ChatId:        chatID,
			FromMessageId: fromMessageID,
			Offset:        0,
			Limit:         historySyncPageSize,
			OnlyLocal:     false,
		})
		if err != nil {
			return nil, err
		}
		if history == nil || len(history.Messages) == 0 {
			break
		}

		batch := history.Messages
		if fromMessageID != 0 && len(batch) > 0 && batch[0].Id == fromMessageID {
			batch = batch[1:]
		}
		if len(batch) == 0 {
			break
		}

		reachedBoundary := false
		for _, message := range batch {
			if message.Id <= lastSavedMessageID {
				reachedBoundary = true
				continue
			}
			collected = append(collected, message)
		}

		oldestMessageID := batch[len(batch)-1].Id
		if reachedBoundary || len(batch) < int(historySyncPageSize) {
			break
		}
		fromMessageID = oldestMessageID
	}

	return collected, nil
}

func (app *TeleApp) saveGroupTextMessage(ctx context.Context, message *client.Message, logSaved bool) (bool, error) {
	if message == nil || message.Content == nil || message.Content.MessageContentType() != "messageText" {
		return false, nil
	}

	chat, err := app.getChat(message.ChatId)
	if err != nil {
		return false, err
	}

	switch chat.Type.ChatTypeType() {
	case client.TypeChatTypeBasicGroup, client.TypeChatTypeSupergroup:
	default:
		return false, nil
	}

	if !app.svcCtx.Config.Summary.ShouldSaveMessage(message.ChatId) {
		return false, nil
	}

	text := message.Content.(*client.MessageText)
	if text.Text == nil || text.Text.Text == "" {
		return false, nil
	}

	senderID, senderName, senderUsername := app.resolveMessageSender(message)
	msgData := &model.MessageData{
		MessageID:      message.Id,
		ChatID:         message.ChatId,
		SenderID:       senderID,
		SenderName:     senderName,
		SenderUsername: senderUsername,
		Text:           text.Text.Text,
		SentAt:         time.Unix(int64(message.Date), 0).UTC(),
	}

	_, err = app.svcCtx.MessageModel.Create(ctx, msgData)
	if err != nil {
		return false, err
	}

	if logSaved {
		logger.Debugf("[TeleApp] 保存消息: %s[%d] -> %s: %s", chat.Title, chat.Id, senderName, text.Text.Text)
	}
	return true, nil
}

func (app *TeleApp) resolveMessageSender(message *client.Message) (int64, string, *string) {
	if message == nil || message.SenderId == nil {
		return 0, "", nil
	}

	sender, ok := message.SenderId.(*client.MessageSenderUser)
	if !ok {
		return 0, "", nil
	}

	user, err := app.getUser(sender.UserId)
	if err != nil {
		logger.Warnf("[TeleApp] 获取用户信息失败, id: %d, %v", sender.UserId, err)
		return sender.UserId, "", nil
	}

	senderName := user.FirstName
	if user.LastName != "" {
		senderName += " " + user.LastName
	}

	var senderUsername *string
	if user.Usernames != nil && len(user.Usernames.ActiveUsernames) > 0 {
		username := "@" + user.Usernames.ActiveUsernames[0]
		senderUsername = &username
	}

	return sender.UserId, senderName, senderUsername
}

func formatHistoryCatchUpPlanPanel(snapshotCount int, plans []historyCatchUpPlan) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "  snapshot 群组数: %d\n", snapshotCount)
	fmt.Fprintf(&builder, "  待补拉群组数: %d\n", len(plans))
	for index, plan := range plans {
		fmt.Fprintf(
			&builder,
			"  %d. %s[%d] checkpoint=%d\n",
			index+1,
			plan.ChatTitle,
			plan.ChatID,
			plan.Checkpoint,
		)
	}
	return strings.TrimRight(builder.String(), "\n")
}

func formatHistoryCatchUpSummaryPanel(snapshotCount, trackedCount, successCount, failedCount, totalSaved int, results []historyCatchUpResult, failures []historyCatchUpFailure) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "  snapshot 群组数: %d\n", snapshotCount)
	fmt.Fprintf(&builder, "  实际补拉群组数: %d\n", trackedCount)
	fmt.Fprintf(&builder, "  成功: %d\n", successCount)
	fmt.Fprintf(&builder, "  失败: %d\n", failedCount)
	fmt.Fprintf(&builder, "  总入库消息数: %d\n", totalSaved)
	for index, result := range results {
		fmt.Fprintf(
			&builder,
			"  %d. %s[%d] checkpoint=%d 候选=%d 入库=%d 范围=%s 耗时=%s\n",
			index+1,
			result.Plan.ChatTitle,
			result.Plan.ChatID,
			result.Plan.Checkpoint,
			result.CandidateCount,
			result.SavedCount,
			formatCatchUpMessageRange(result.FirstNewMessageID, result.LastNewMessageID),
			result.Duration.Round(time.Millisecond),
		)
	}
	for index, failure := range failures {
		fmt.Fprintf(
			&builder,
			"  fail-%d. %s[%d] checkpoint=%d err=%s\n",
			index+1,
			failure.Plan.ChatTitle,
			failure.Plan.ChatID,
			failure.Plan.Checkpoint,
			failure.Reason,
		)
	}
	return strings.TrimRight(builder.String(), "\n")
}

func formatCatchUpError(err error) string {
	if err == nil {
		return "-"
	}

	replacer := strings.NewReplacer("\r", " ", "\n", " ")
	return strings.TrimSpace(replacer.Replace(err.Error()))
}

func formatCatchUpMessageRange(firstMessageID, lastMessageID int64) string {
	if firstMessageID == 0 || lastMessageID == 0 {
		return "-"
	}
	if firstMessageID == lastMessageID {
		return fmt.Sprintf("%d", firstMessageID)
	}
	return fmt.Sprintf("%d..%d", firstMessageID, lastMessageID)
}

func messageIDRange(messages []*client.Message) (int64, int64) {
	if len(messages) == 0 {
		return 0, 0
	}

	minID := messages[0].Id
	maxID := messages[0].Id
	for _, message := range messages[1:] {
		if message.Id < minID {
			minID = message.Id
		}
		if message.Id > maxID {
			maxID = message.Id
		}
	}
	return minID, maxID
}
