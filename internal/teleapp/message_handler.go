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

	// 只跳过本客户端自己发出的消息（IsOutgoing=true，如自动回复/摘要），
	// 同账号其它设备发来的入站消息（IsOutgoing=false）正常处理，否则无法用 bot 自身账号测试命令。
	if senderID == app.user.Id && message.IsOutgoing {
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
	isDeleteCommand := false
	isStartCommand := false
	isHelpCommand := false
	isPrivateReply := false
	hasMention := false

	if !isGroupChat {
		if strings.Contains(messageText, "抄底") {
			shouldRespond = true
		}
		// 私聊可直接使用 /start、/help，无需 @
		fields := strings.Fields(strings.TrimSpace(messageText))
		if len(fields) > 0 {
			switch fields[0] {
			case "/start":
				isStartCommand = true
			case "/help":
				isHelpCommand = true
			}
		}
	} else {
		mentionPattern := app.user.FirstName
		if botUsername != "" {
			mentionPattern = "@" + botUsername
		}
		hasMention = strings.Contains(strings.ToLower(messageText), mentionPattern)

		trimmedText := strings.TrimSpace(messageText)
		if hasMention {
			trimmedText = strings.TrimPrefix(trimmedText, mentionPattern)
			trimmedText = strings.TrimSpace(trimmedText)
		}

		if hasMention && strings.Contains(messageText, "抄底") {
			shouldRespond = true
		}

		bareCmd := trimmedText
		if fields := strings.Fields(trimmedText); len(fields) > 0 {
			bareCmd = fields[0]
			for _, f := range fields[1:] {
				if f == "-p" || f == "--private" {
					isPrivateReply = true
				}
			}
		}

		if hasMention && strings.HasPrefix(bareCmd, "/getuserid") {
			isGetUserIDCommand = true
		}

		if hasMention && (strings.HasPrefix(bareCmd, "/sum") || strings.HasPrefix(bareCmd, "/summary")) {
			isSummaryCommand = true
		}

		if hasMention && bareCmd == "/profile" {
			isHiCommand = true
		}

		if hasMention && bareCmd == "/delete" {
			isDeleteCommand = true
		}

		if hasMention && bareCmd == "/start" {
			isStartCommand = true
		}

		if hasMention && bareCmd == "/help" {
			isHelpCommand = true
		}
	}

	if isGetUserIDCommand {
		replyText, err := app.buildGetUserIDReply(ctx, message)
		if err != nil {
			logger.Errorf("[TeleApp] 处理 /getuserid 失败: %v", err)
			replyText = "获取被回复用户 ID 失败，请稍后再试。"
		}
		targetChatID := message.ChatId
		replyToID := message.Id
		if isPrivateReply {
			targetChatID = senderID
			replyToID = 0
		}
		if err := app.sendMessage(ctx, targetChatID, replyToID, replyText); err != nil {
			logger.Errorf("[TeleApp] 发送 /getuserid 结果失败: %v", err)
		} else {
			logger.Infof("[TeleApp] 已回复 /getuserid, chat=%d, requester=%d, message=%d", message.ChatId, senderID, message.Id)
		}
	}

	if isSummaryCommand && app.summaryHandler != nil {
		if app.isAdminForChat(message.ChatId, senderID) {
			if !app.processMu.TryLock() {
				_ = app.sendMessage(ctx, message.ChatId, message.Id, "有一个请求正在处理中，请稍后再试。")
			} else {
				logger.Infof("[TeleApp] 用户 %d 在群组 %d 请求手动摘要", senderID, message.ChatId)
				go func(chatID int64) {
					defer app.processMu.Unlock()
					if err := app.summaryHandler(ctx, chatID); err != nil {
						logger.Errorf("[TeleApp] 手动摘要失败: %v", err)
					}
				}(message.ChatId)
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
		if app.isAdminForChat(message.ChatId, senderID) {
			if !app.processMu.TryLock() {
				_ = app.sendMessage(ctx, message.ChatId, message.Id, "有一个请求正在处理中，请稍后再试。")
			} else {
				go func() {
					defer app.processMu.Unlock()
					app.handleHiCommand(ctx, message, senderID, isPrivateReply)
				}()
			}
		} else {
			_ = app.sendMessage(ctx, message.ChatId, message.Id, "你不在白名单中，不能使用该功能。")
		}
	}

	if isDeleteCommand {
		if !app.isAdminForChat(message.ChatId, senderID) {
			_ = app.sendMessage(ctx, message.ChatId, message.Id, "你没有权限使用该命令。")
		} else {
			replyTo, ok := message.ReplyTo.(*client.MessageReplyToMessage)
			if !ok || replyTo == nil || replyTo.MessageId == 0 {
				_ = app.sendMessage(ctx, message.ChatId, message.Id, "请回复 bot 发送的一条消息后使用 /delete。")
			} else {
				repliedMessage, err := app.tdClient.GetMessage(&client.GetMessageRequest{
					ChatId:    message.ChatId,
					MessageId: replyTo.MessageId,
				})
				if err != nil {
					_ = app.sendMessage(ctx, message.ChatId, message.Id, fmt.Sprintf("获取被回复消息失败: %v", err))
				} else if repliedMessage.SenderId == nil {
					_ = app.sendMessage(ctx, message.ChatId, message.Id, "只能删除 bot 发送的消息。")
				} else if _, ok := repliedMessage.SenderId.(*client.MessageSenderUser); !ok {
					_ = app.sendMessage(ctx, message.ChatId, message.Id, "只能删除 bot 发送的消息。")
				} else if repliedMessage.SenderId.(*client.MessageSenderUser).UserId != app.user.Id {
					_ = app.sendMessage(ctx, message.ChatId, message.Id, "只能删除 bot 发送的消息。")
				} else {
					_, err = app.tdClient.DeleteMessages(&client.DeleteMessagesRequest{
						ChatId:     message.ChatId,
						MessageIds: []int64{replyTo.MessageId, message.Id},
						Revoke:     true,
					})
					if err != nil {
						logger.Errorf("[TeleApp] 删除消息失败: %v", err)
					} else {
						logger.Infof("[TeleApp] 已删除消息, chat=%d, messages=%v", message.ChatId, []int64{replyTo.MessageId, message.Id})
					}
				}
			}
		}
	}

	if isStartCommand {
		if err := app.sendMessage(ctx, message.ChatId, message.Id, app.buildStartText()); err != nil {
			logger.Errorf("[TeleApp] 发送自我介绍失败: %v", err)
		}
	}

	if isHelpCommand {
		if err := app.sendMessage(ctx, message.ChatId, message.Id, app.buildHelpText()); err != nil {
			logger.Errorf("[TeleApp] 发送命令说明失败: %v", err)
		}
	}

	// 未识别的消息：私聊每次都提示；群聊需明确 @ 才提示。
	// 回复我们自己的消息不接话（避免 bot 互聊死循环）；同一聊 30 秒内限频一次。
	if !shouldRespond && !isSummaryCommand && !isGetUserIDCommand && !isHiCommand &&
		!isDeleteCommand && !isStartCommand && !isHelpCommand && !app.isReplyToOwnMessage(message) {
		if !isGroupChat {
			if app.maybeHint(message.ChatId) {
				_ = app.sendMessage(ctx, message.ChatId, message.Id, "👋 你好！发送 /start 查看介绍，发送 /help 查看可用命令。")
			}
		} else if hasMention {
			if app.maybeHint(message.ChatId) {
				_ = app.sendMessage(ctx, message.ChatId, message.Id, "🤔 你好！在群里 @我 发送 /help 查看可用命令。")
			}
		}
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

// isGroupAdmin 查询群管理员列表，判断用户是否为该群管理员。
func (app *TeleApp) isGroupAdmin(chatID, userID int64) bool {
	admins, err := app.tdClient.GetChatAdministrators(&client.GetChatAdministratorsRequest{ChatId: chatID})
	if err != nil {
		logger.Warnf("[TeleApp] 获取群管理员失败 chat=%d: %v", chatID, err)
		return false
	}
	for _, a := range admins.Administrators {
		if a.UserId == userID {
			return true
		}
	}
	return false
}

// isAdminForChat 判断用户是否有权使用管理员命令：配置文件管理员 或 当前群管理员。
func (app *TeleApp) isAdminForChat(chatID, senderID int64) bool {
	for _, adminID := range app.adminUserIds {
		if adminID == senderID {
			return true
		}
	}
	return app.isGroupAdmin(chatID, senderID)
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
func (app *TeleApp) handleHiCommand(ctx context.Context, message *client.Message, senderID int64, isPrivate bool) {
	targetChatID := message.ChatId
	replyToID := message.Id
	if isPrivate {
		targetChatID = senderID
		replyToID = 0
	}

	replyTo, ok := message.ReplyTo.(*client.MessageReplyToMessage)
	if !ok || replyTo == nil || replyTo.MessageId == 0 {
		_ = app.sendMessage(ctx, targetChatID, replyToID, "请先回复目标用户的一条消息，再发送 @机器人 /profile。")
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
		_ = app.sendMessage(ctx, targetChatID, replyToID, fmt.Sprintf("获取被回复消息失败: %v", err))
		return
	}

	targetID, targetName, _ := app.resolveMessageSender(repliedMessage)
	if targetID == 0 {
		_ = app.sendMessage(ctx, targetChatID, replyToID, "被回复的消息不是普通用户发送。")
		return
	}

	thinking := fmt.Sprintf("🤔 正在分析 %s 的性格，请稍候……", targetName)
	_ = app.sendMessage(ctx, targetChatID, replyToID, thinking)

	msgs, err := app.svcCtx.MessageModel.GetBySenderAndChat(ctx, message.ChatId, targetID)
	if err != nil {
		_ = app.sendMessage(ctx, targetChatID, replyToID, fmt.Sprintf("查询聊天记录失败: %v", err))
		return
	}

	if len(msgs) == 0 {
		_ = app.sendMessage(ctx, targetChatID, replyToID, fmt.Sprintf("未找到 %s 的聊天记录。", targetName))
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

	profile, err := app.svcCtx.LLMClient.AnalyzePersonality(ctx, chatMsgs)
	if err != nil {
		_ = app.sendMessage(ctx, targetChatID, replyToID, fmt.Sprintf("性格分析失败: %v", err))
		return
	}

	replyText := formatHiReply(targetName, targetID, profile, len(msgs))
	_ = app.sendMessage(ctx, targetChatID, replyToID, replyText)
}

func formatHiReply(targetName string, targetID int64, profile *llm.PersonalityProfile, msgCount int) string {
	var b strings.Builder

	fmt.Fprintf(&b, "🧑 性格分析：%s (ID: %d)\n\n", targetName, targetID)
	fmt.Fprintf(&b, "📋 概况\n%s\n\n", profile.Summary)

	if len(profile.PersonalityTraits) > 0 {
		fmt.Fprintln(&b, "🔹 性格特征")
		for _, t := range profile.PersonalityTraits {
			if t.Explanation != "" {
				fmt.Fprintf(&b, "   %s：%s\n\n", t.Trait, t.Explanation)
			} else {
				fmt.Fprintf(&b, "   • %s\n", t.Trait)
			}
		}
		fmt.Fprintln(&b)
	}

	if len(profile.CommunicationStyle) > 0 {
		fmt.Fprintln(&b, "💬 沟通风格")
		for _, s := range profile.CommunicationStyle {
			if s.Explanation != "" {
				fmt.Fprintf(&b, "   %s：%s\n\n", s.Trait, s.Explanation)
			} else {
				fmt.Fprintf(&b, "   • %s\n", s.Trait)
			}
		}
		fmt.Fprintln(&b)
	}

	if len(profile.Interests) > 0 {
		fmt.Fprintln(&b, "🎯 兴趣爱好和关注领域")
		for _, i := range profile.Interests {
			if i.Explanation != "" {
				fmt.Fprintf(&b, "   %s：%s\n\n", i.Trait, i.Explanation)
			} else {
				fmt.Fprintf(&b, "   • %s\n", i.Trait)
			}
		}
		fmt.Fprintln(&b)
	}

	if len(profile.BehaviorPatterns) > 0 {
		fmt.Fprintln(&b, "🔄 行为模式")
		for _, p := range profile.BehaviorPatterns {
			if p.Explanation != "" {
				fmt.Fprintf(&b, "   %s：%s\n\n", p.Trait, p.Explanation)
			} else {
				fmt.Fprintf(&b, "   • %s\n", p.Trait)
			}
		}
		fmt.Fprintln(&b)
	}

	fmt.Fprintf(&b, "📝 综合评价\n%s\n\n", profile.OverallAssessment)
	fmt.Fprintf(&b, "📊 基于 %d 条聊天记录分析", msgCount)

	return b.String()
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

// botDisplayName 返回 bot 用户的 Telegram 显示名（FirstName + LastName）。
func (app *TeleApp) botDisplayName() string {
	if app.user == nil {
		return "Chat Summary Bot"
	}
	name := strings.TrimSpace(strings.Join([]string{app.user.FirstName, app.user.LastName}, " "))
	if name == "" {
		return "Chat Summary Bot"
	}
	return name
}

// buildStartText 返回机器人自我介绍。
func (app *TeleApp) buildStartText() string {
	return fmt.Sprintf("👋 你好，我是 %s！\n\n", app.botDisplayName()) +
		"拉我进群，我会自动记录群聊消息，并提供：\n" +
		"• 📝 AI 群聊摘要\n" +
		"• 🧑 成员性格画像\n" +
		"• 📊 BTC 市场指标\n" +
		"• 🚨 跨平台告警转发\n\n" +
		"在群聊中 @我 发送 /help 查看命令说明。"
}

// buildHelpText 返回命令使用说明。
func (app *TeleApp) buildHelpText() string {
	return fmt.Sprintf("🤖 %s 命令说明\n\n", app.botDisplayName()) +
		"📊 BTC 指标\n" +
		"  @我 抄底          发送最新 BTC 抄底指标\n\n" +
		"🔍 用户信息\n" +
		"  @我 /getuserid    回复目标用户后发送，获取其 ID/昵称（可加 -p 私发）\n\n" +
		"📝 群聊摘要\n" +
		"  @我 /sum          手动生成最近 24 小时摘要（仅管理员）\n\n" +
		"🧑 性格分析\n" +
		"  @我 /profile      回复目标用户后发送，分析其性格（仅管理员，可加 -p 私发）\n\n" +
		"🗑️ 删除消息\n" +
		"  @我 /delete       回复 bot 发送的消息后发送，删除该消息（仅管理员）\n\n" +
		"💡 群聊中使用请先 @我；私聊中可直接发送 /start 或 /help。"
}
