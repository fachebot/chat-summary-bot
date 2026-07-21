package teleapp

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/fachebot/chat-summary-bot/internal/lark"
	"github.com/fachebot/chat-summary-bot/internal/llm"
	"github.com/fachebot/chat-summary-bot/internal/logger"

	"github.com/zelenin/go-tdlib/client"
)

type relayPayload struct {
	messageType    string
	text           string
	attachmentType string
	attachmentFile *client.File
	attachmentName string
}

func (app *TeleApp) handleIncomingMessage(ctx context.Context, message *client.Message, botUsername string) {
	if message == nil || message.Content == nil {
		return
	}

	senderID, senderName, senderUsernamePtr := app.resolveMessageSender(message)
	senderUsername := ""
	if senderUsernamePtr != nil {
		senderUsername = *senderUsernamePtr
	}

	chat, err := app.getChat(message.ChatId)
	if err != nil {
		logger.Warnf("[TeleApp] 获取聊天信息失败, id: %d, %v", message.ChatId, err)
		return
	}

	isGroupChat := isSupportedGroupChat(chat)
	messageText := extractMessageText(message)
	if messageText != "" {
		logger.Debugf("[TeleApp] 接收消息: %s(%d) -> %s", senderName, senderID, messageText)
	}

	if isGroupChat {
		app.forwardIfMonitored(ctx, chat, message, senderID, senderName, senderUsername)
	}

	if senderID == app.user.Id {
		if isGroupChat {
			if _, err := app.saveGroupTextMessage(ctx, message, true); err != nil {
				logger.Errorf("[TeleApp] 保存消息失败, %v", err)
			}
		}
		return
	}

	if messageText == "" {
		if isGroupChat {
			if _, err := app.saveGroupTextMessage(ctx, message, true); err != nil {
				logger.Errorf("[TeleApp] 保存消息失败, %v", err)
			}
		}
		return
	}

	shouldRespond := false
	isSummaryCommand := false
	isGetUserIDCommand := false
	isHiCommand := false

	if !isGroupChat {
		if strings.Contains(messageText, "抄底") {
			shouldRespond = true
		}
	} else {
		mentionPattern := app.user.FirstName
		if botUsername != "" {
			mentionPattern = "@" + botUsername
		}
		hasMention := strings.Contains(strings.ToLower(messageText), mentionPattern)

		trimmedText := strings.TrimSpace(messageText)
		if hasMention {
			trimmedText = strings.TrimPrefix(trimmedText, mentionPattern)
			trimmedText = strings.TrimSpace(trimmedText)
		}

		if hasMention && strings.Contains(messageText, "抄底") {
			shouldRespond = true
		}

		if hasMention && strings.HasPrefix(trimmedText, "/getuserid") {
			isGetUserIDCommand = true
		}

		if hasMention && (strings.HasPrefix(trimmedText, "/sum") || strings.HasPrefix(trimmedText, "/summary")) {
			isSummaryCommand = true
		}

		if hasMention && trimmedText == "/profile" {
			isHiCommand = true
		}
	}

	if isGetUserIDCommand {
		replyText, err := app.buildGetUserIDReply(ctx, message)
		if err != nil {
			logger.Errorf("[TeleApp] 处理 /getuserid 失败: %v", err)
			replyText = "获取被回复用户 ID 失败，请稍后再试。"
		}
		if err := app.sendMessage(ctx, message.ChatId, message.Id, replyText); err != nil {
			logger.Errorf("[TeleApp] 发送 /getuserid 结果失败: %v", err)
		} else {
			logger.Infof("[TeleApp] 已回复 /getuserid, chat=%d, requester=%d, message=%d", message.ChatId, senderID, message.Id)
		}
	}

	if isSummaryCommand && app.summaryHandler != nil {
		isAdmin := false
		for _, adminID := range app.adminUserIds {
			if senderID == adminID {
				isAdmin = true
				break
			}
		}
		if isAdmin {
			logger.Infof("[TeleApp] 用户 %d 在群组 %d 请求手动摘要", senderID, message.ChatId)
			if err := app.summaryHandler(ctx, message.ChatId); err != nil {
				logger.Errorf("[TeleApp] 手动摘要失败: %v", err)
			}
		} else {
			logger.Warnf("[TeleApp] 用户 %d 不在白名单中，拒绝手动摘要请求", senderID)
			if err := app.sendMessage(ctx, message.ChatId, message.Id, "你不在白名单中，不能使用该功能。"); err != nil {
				logger.Errorf("[TeleApp] 发送白名单拒绝提示失败: %v", err)
			}
		}
	}

	if shouldRespond && app.marketIndicators != nil {
		indicatorText := app.marketIndicators.GetFormattedText()
		err := app.sendMessage(ctx, message.ChatId, message.Id, indicatorText, &client.TextParseModeHTML{})
		if err != nil {
			logger.Errorf("[TeleApp] 发送抄底指标失败: %v", err)
		} else {
			logger.Infof("[TeleApp] 已发送抄底指标到 %s", chat.Title)
		}
	}

	if isHiCommand {
		go app.handleHiCommand(ctx, message)
	}

	if isGroupChat {
		saved, err := app.saveGroupTextMessage(ctx, message, true)
		if err != nil {
			logger.Errorf("[TeleApp] 保存消息失败, %v", err)
			return
		}
		if !saved && !app.svcCtx.Config.Summary.ShouldSaveMessage(message.ChatId) {
			logger.Debugf("[TeleApp] 群组 %d 在白名单/黑名单中被过滤，跳过保存", message.ChatId)
		}
	}
}

func (app *TeleApp) buildGetUserIDReply(ctx context.Context, message *client.Message) (string, error) {
	if message == nil {
		return "请先回复目标用户的一条消息，再发送 @机器人 /getuserid。", nil
	}

	replyTo, ok := message.ReplyTo.(*client.MessageReplyToMessage)
	if !ok || replyTo == nil || replyTo.MessageId == 0 {
		return "请先回复目标用户的一条消息，再发送 @机器人 /getuserid。", nil
	}

	repliedChatID := replyTo.ChatId
	if repliedChatID == 0 {
		repliedChatID = message.ChatId
	}

	repliedMessage, err := app.tdClient.GetMessage(&client.GetMessageRequest{
		ChatId:    repliedChatID,
		MessageId: replyTo.MessageId,
	})
	if err != nil {
		return "", err
	}

	targetUserID, targetName, targetUsername := app.resolveMessageSender(repliedMessage)
	if targetUserID == 0 {
		return "被回复的消息不是普通用户发送，无法获取 Telegram 用户 ID。", nil
	}

	lines := []string{fmt.Sprintf("ID: %d", targetUserID)}
	if trimmedName := strings.TrimSpace(targetName); trimmedName != "" {
		lines = append(lines, fmt.Sprintf("昵称: %s", trimmedName))
	}
	if targetUsername != nil {
		if trimmedUsername := strings.TrimSpace(*targetUsername); trimmedUsername != "" {
			lines = append(lines, fmt.Sprintf("用户名: %s", trimmedUsername))
		}
	}

	return strings.Join(lines, "\n"), nil
}

// handleHiCommand 处理 @机器人 /profile 命令（需回复目标用户的消息）
func (app *TeleApp) handleHiCommand(ctx context.Context, message *client.Message) {
	replyTo, ok := message.ReplyTo.(*client.MessageReplyToMessage)
	if !ok || replyTo == nil || replyTo.MessageId == 0 {
		_ = app.sendMessage(ctx, message.ChatId, message.Id, "请先回复目标用户的一条消息，再发送 @机器人 /profile。")
		return
	}

	repliedChatID := replyTo.ChatId
	if repliedChatID == 0 {
		repliedChatID = message.ChatId
	}

	repliedMessage, err := app.tdClient.GetMessage(&client.GetMessageRequest{
		ChatId:    repliedChatID,
		MessageId: replyTo.MessageId,
	})
	if err != nil {
		_ = app.sendMessage(ctx, message.ChatId, message.Id, fmt.Sprintf("获取被回复消息失败: %v", err))
		return
	}

	targetID, targetName, _ := app.resolveMessageSender(repliedMessage)
	if targetID == 0 {
		_ = app.sendMessage(ctx, message.ChatId, message.Id, "被回复的消息不是普通用户发送。")
		return
	}

	thinking := fmt.Sprintf("🤔 正在分析 %s 的性格，请稍候……", targetName)
	_ = app.sendMessage(ctx, message.ChatId, message.Id, thinking)

	msgs, err := app.svcCtx.MessageModel.GetBySenderAndChat(ctx, message.ChatId, targetID)
	if err != nil {
		_ = app.sendMessage(ctx, message.ChatId, message.Id, fmt.Sprintf("查询聊天记录失败: %v", err))
		return
	}

	if len(msgs) == 0 {
		_ = app.sendMessage(ctx, message.ChatId, message.Id, fmt.Sprintf("未找到 %s 的聊天记录。", targetName))
		return
	}

	chatMsgs := make([]llm.ChatMessage, len(msgs))
	for i, msg := range msgs {
		chatMsgs[i] = llm.ChatMessage{
			MessageID:  msg.MessageID,
			SenderID:   msg.SenderID,
			SenderName: msg.SenderName,
			Text:       msg.Text,
		}
	}

	analysis, err := app.svcCtx.LLMClient.AnalyzePersonality(ctx, chatMsgs)
	if err != nil {
		_ = app.sendMessage(ctx, message.ChatId, message.Id, fmt.Sprintf("性格分析失败: %v", err))
		return
	}

	replyText := formatHiReply(targetName, targetID, analysis, len(msgs))
	_ = app.sendMessage(ctx, message.ChatId, message.Id, replyText)
}

func formatHiReply(targetName string, targetID int64, analysis string, msgCount int) string {
	return fmt.Sprintf("🧑 性格分析：%s (ID: %d)\n\n%s\n\n📊 基于 %d 条聊天记录分析", targetName, targetID, analysis, msgCount)
}

func isSupportedGroupChat(chat *client.Chat) bool {
	if chat == nil || chat.Type == nil {
		return false
	}

	switch chat.Type.ChatTypeType() {
	case client.TypeChatTypeBasicGroup, client.TypeChatTypeSupergroup:
		return true
	default:
		return false
	}
}

func extractMessageText(message *client.Message) string {
	if message == nil || message.Content == nil || message.Content.MessageContentType() != "messageText" {
		return ""
	}

	content, ok := message.Content.(*client.MessageText)
	if !ok || content.Text == nil {
		return ""
	}
	return content.Text.Text
}

func (app *TeleApp) forwardIfMonitored(ctx context.Context, chat *client.Chat, message *client.Message, senderID int64, senderName, senderUsername string) {
	if app.larkForwarder == nil {
		return
	}
	if !app.svcCtx.Config.LarkForward.ShouldMonitorUser(senderID, senderUsername) {
		return
	}

	alert, err := app.buildLarkAlert(ctx, chat, message, senderID, senderName, senderUsername)
	if err != nil {
		logger.Errorf("[TeleApp] 构造 Lark 转发消息失败: %v", err)
		return
	}
	if alert == nil {
		return
	}

	if err := app.larkForwarder.ForwardTelegramAlert(ctx, alert); err != nil {
		logger.Errorf("[TeleApp] Lark 转发失败, chat=%d, message=%d, err=%v", message.ChatId, message.Id, err)
		return
	}

	logger.Infof("[TeleApp] 已转发监控消息到 Lark: chat=%s[%d], sender=%s(%d), message=%d", chat.Title, chat.Id, senderName, senderID, message.Id)
}

func (app *TeleApp) buildLarkAlert(ctx context.Context, chat *client.Chat, message *client.Message, senderID int64, senderName, senderUsername string) (*lark.TelegramAlert, error) {
	payload, err := app.buildRelayPayload(message)
	if err != nil {
		return nil, err
	}
	if payload == nil {
		return nil, nil
	}

	alert := &lark.TelegramAlert{
		ChatID:         message.ChatId,
		ChatTitle:      chat.Title,
		MessageID:      message.Id,
		MessageType:    payload.messageType,
		SenderID:       senderID,
		SenderName:     senderName,
		SenderUsername: senderUsername,
		SentAt:         time.Unix(int64(message.Date), 0).UTC(),
		Text:           payload.text,
		AttachmentType: payload.attachmentType,
		AttachmentName: payload.attachmentName,
	}

	if payload.attachmentFile != nil {
		filePath, err := app.downloadTelegramFile(ctx, payload.attachmentFile)
		if err != nil {
			return nil, err
		}
		alert.AttachmentPath = filePath
		if alert.AttachmentName == "" {
			alert.AttachmentName = filepath.Base(filePath)
		}
	}

	return alert, nil
}

func (app *TeleApp) buildRelayPayload(message *client.Message) (*relayPayload, error) {
	if message == nil || message.Content == nil {
		return nil, nil
	}

	switch content := message.Content.(type) {
	case *client.MessageText:
		return &relayPayload{messageType: "messageText", text: formattedTextPlainText(content.Text)}, nil
	case *client.MessagePhoto:
		return &relayPayload{
			messageType:    "messagePhoto",
			text:           formattedTextPlainText(content.Caption),
			attachmentType: lark.AttachmentTypeImage,
			attachmentFile: largestPhotoFile(content.Photo),
			attachmentName: fmt.Sprintf("photo-%d.jpg", message.Id),
		}, nil
	case *client.MessageDocument:
		if content.Document == nil {
			return &relayPayload{messageType: "messageDocument", text: formattedTextPlainText(content.Caption)}, nil
		}
		return &relayPayload{
			messageType:    "messageDocument",
			text:           formattedTextPlainText(content.Caption),
			attachmentType: lark.AttachmentTypeFile,
			attachmentFile: content.Document.Document,
			attachmentName: fallbackFileName(content.Document.FileName, message.Id, "document"),
		}, nil
	case *client.MessageVideo:
		if content.Video == nil {
			return &relayPayload{messageType: "messageVideo", text: formattedTextPlainText(content.Caption)}, nil
		}
		return &relayPayload{
			messageType:    "messageVideo",
			text:           formattedTextPlainText(content.Caption),
			attachmentType: lark.AttachmentTypeFile,
			attachmentFile: content.Video.Video,
			attachmentName: fallbackFileName(content.Video.FileName, message.Id, "video"),
		}, nil
	case *client.MessageAudio:
		if content.Audio == nil {
			return &relayPayload{messageType: "messageAudio", text: formattedTextPlainText(content.Caption)}, nil
		}
		return &relayPayload{
			messageType:    "messageAudio",
			text:           formattedTextPlainText(content.Caption),
			attachmentType: lark.AttachmentTypeFile,
			attachmentFile: content.Audio.Audio,
			attachmentName: fallbackFileName(content.Audio.FileName, message.Id, "audio"),
		}, nil
	case *client.MessageVoiceNote:
		if content.VoiceNote == nil {
			return &relayPayload{messageType: "messageVoiceNote", text: formattedTextPlainText(content.Caption)}, nil
		}
		return &relayPayload{
			messageType:    "messageVoiceNote",
			text:           formattedTextPlainText(content.Caption),
			attachmentType: lark.AttachmentTypeFile,
			attachmentFile: content.VoiceNote.Voice,
			attachmentName: fmt.Sprintf("voice-note-%d.ogg", message.Id),
		}, nil
	case *client.MessageAnimation:
		if content.Animation == nil {
			return &relayPayload{messageType: "messageAnimation", text: formattedTextPlainText(content.Caption)}, nil
		}
		return &relayPayload{
			messageType:    "messageAnimation",
			text:           formattedTextPlainText(content.Caption),
			attachmentType: lark.AttachmentTypeFile,
			attachmentFile: content.Animation.Animation,
			attachmentName: fallbackFileName(content.Animation.FileName, message.Id, "animation"),
		}, nil
	default:
		if payload := buildSystemRelayPayload(content); payload != nil {
			return payload, nil
		}
		return &relayPayload{
			messageType: message.Content.MessageContentType(),
			text:        fmt.Sprintf("[暂未直接转发附件的消息类型] %s", message.Content.MessageContentType()),
		}, nil
	}
}

func buildSystemRelayPayload(content client.MessageContent) *relayPayload {
	if content == nil {
		return nil
	}

	switch systemMessage := content.(type) {
	case *client.MessageBasicGroupChatCreate:
		if title := strings.TrimSpace(systemMessage.Title); title != "" {
			return &relayPayload{messageType: "messageBasicGroupChatCreate", text: fmt.Sprintf("已创建群组：%s。", title)}
		}
		return &relayPayload{messageType: "messageBasicGroupChatCreate", text: "已创建新的群组。"}
	case *client.MessageSupergroupChatCreate:
		if title := strings.TrimSpace(systemMessage.Title); title != "" {
			return &relayPayload{messageType: "messageSupergroupChatCreate", text: fmt.Sprintf("已创建超级群组：%s。", title)}
		}
		return &relayPayload{messageType: "messageSupergroupChatCreate", text: "已创建新的超级群组。"}
	case *client.MessageChatChangeTitle:
		if title := strings.TrimSpace(systemMessage.Title); title != "" {
			return &relayPayload{messageType: "messageChatChangeTitle", text: fmt.Sprintf("群标题已修改为：%s。", title)}
		}
		return &relayPayload{messageType: "messageChatChangeTitle", text: "群标题已修改。"}
	case *client.MessageChatAddMembers:
		memberCount := len(systemMessage.MemberUserIds)
		if memberCount > 0 {
			return &relayPayload{messageType: "messageChatAddMembers", text: fmt.Sprintf("新增 %d 位成员加入群聊。", memberCount)}
		}
		return &relayPayload{messageType: "messageChatAddMembers", text: "有新成员加入群聊。"}
	case *client.MessageChatDeleteMember:
		return &relayPayload{messageType: "messageChatDeleteMember", text: "一名成员已被移出群聊。"}
	case *client.MessagePinMessage:
		return &relayPayload{messageType: "messagePinMessage", text: "一条消息已被置顶。"}
	case *client.MessageScreenshotTaken:
		return &relayPayload{messageType: "messageScreenshotTaken", text: "聊天内容被截图。"}
	case *client.MessageChatSetTheme:
		themeName := strings.TrimSpace(systemMessage.ThemeName)
		if themeName == "" {
			return &relayPayload{messageType: "systemChatThemeReset", text: "聊天主题已恢复默认。"}
		}
		return &relayPayload{messageType: "systemChatThemeChanged", text: fmt.Sprintf("聊天主题已切换为：%s。", themeName)}
	case *client.MessageChatSetMessageAutoDeleteTime:
		if systemMessage.MessageAutoDeleteTime <= 0 {
			return &relayPayload{messageType: "systemMessageAutoDeleteDisabled", text: "消息自动删除已关闭。"}
		}
		return &relayPayload{messageType: "systemMessageAutoDeleteChanged", text: fmt.Sprintf("消息自动删除时间已改为：%s。", formatMessageAutoDeleteDuration(systemMessage.MessageAutoDeleteTime))}
	case *client.MessageChatBoost:
		if systemMessage.BoostCount > 0 {
			return &relayPayload{messageType: "messageChatBoost", text: fmt.Sprintf("群组被助力 %d 次。", systemMessage.BoostCount)}
		}
		return &relayPayload{messageType: "messageChatBoost", text: "群组被助力。"}
	case *client.MessageForumTopicCreated:
		if name := strings.TrimSpace(systemMessage.Name); name != "" {
			return &relayPayload{messageType: "messageForumTopicCreated", text: fmt.Sprintf("已创建话题：%s。", name)}
		}
		return &relayPayload{messageType: "messageForumTopicCreated", text: "已创建新话题。"}
	case *client.MessageForumTopicEdited:
		if name := strings.TrimSpace(systemMessage.Name); name != "" {
			return &relayPayload{messageType: "messageForumTopicEdited", text: fmt.Sprintf("话题已编辑，名称更新为：%s。", name)}
		}
		return &relayPayload{messageType: "messageForumTopicEdited", text: "话题设置已更新。"}
	case *client.MessageForumTopicIsClosedToggled:
		if systemMessage.IsClosed {
			return &relayPayload{messageType: "systemForumTopicClosed", text: "该话题已关闭。"}
		}
		return &relayPayload{messageType: "systemForumTopicReopened", text: "该话题已重新打开。"}
	case *client.MessageForumTopicIsHiddenToggled:
		if systemMessage.IsHidden {
			return &relayPayload{messageType: "systemForumTopicHidden", text: "该话题已隐藏。"}
		}
		return &relayPayload{messageType: "systemForumTopicVisible", text: "该话题已重新显示。"}
	}

	return nil
}

func formatMessageAutoDeleteDuration(seconds int32) string {
	if seconds <= 0 {
		return "已关闭"
	}
	if seconds%86400 == 0 {
		days := seconds / 86400
		return fmt.Sprintf("%d 天", days)
	}
	if seconds%3600 == 0 {
		hours := seconds / 3600
		return fmt.Sprintf("%d 小时", hours)
	}
	if seconds%60 == 0 {
		minutes := seconds / 60
		return fmt.Sprintf("%d 分钟", minutes)
	}
	return fmt.Sprintf("%d 秒", seconds)
}

func formattedTextPlainText(text *client.FormattedText) string {
	if text == nil {
		return ""
	}
	return strings.TrimSpace(text.Text)
}

func largestPhotoFile(photo *client.Photo) *client.File {
	if photo == nil || len(photo.Sizes) == 0 {
		return nil
	}

	var selected *client.File
	maxArea := int64(-1)
	for _, size := range photo.Sizes {
		if size == nil || size.Photo == nil {
			continue
		}
		area := int64(size.Width) * int64(size.Height)
		if area >= maxArea {
			maxArea = area
			selected = size.Photo
		}
	}
	return selected
}

func fallbackFileName(original string, messageID int64, prefix string) string {
	trimmed := strings.TrimSpace(original)
	if trimmed != "" {
		return trimmed
	}
	return fmt.Sprintf("%s-%d", prefix, messageID)
}

func (app *TeleApp) downloadTelegramFile(ctx context.Context, file *client.File) (string, error) {
	if file == nil {
		return "", fmt.Errorf("telegram file is nil")
	}
	if file.Local != nil && file.Local.IsDownloadingCompleted && file.Local.Path != "" {
		return file.Local.Path, nil
	}

	downloaded, err := app.tdClient.DownloadFile(&client.DownloadFileRequest{
		FileId:      file.Id,
		Priority:    32,
		Offset:      0,
		Limit:       0,
		Synchronous: true,
	})
	if err != nil {
		return "", err
	}
	if downloaded == nil || downloaded.Local == nil || !downloaded.Local.IsDownloadingCompleted || downloaded.Local.Path == "" {
		return "", fmt.Errorf("telegram file download incomplete, file_id=%d", file.Id)
	}
	return downloaded.Local.Path, nil
}
