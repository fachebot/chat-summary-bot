package teleapp

import (
	"context"
	"path/filepath"
	"sync"
	"time"

	"github.com/fachebot/talk-trace-bot/internal/logger"
	"github.com/fachebot/talk-trace-bot/internal/model"
	"github.com/fachebot/talk-trace-bot/internal/svc"

	"github.com/zelenin/go-tdlib/client"
)

type TeleApp struct {
	svcCtx     *svc.ServiceContext
	user       *client.User
	tdClient   *client.Client
	listener   *client.Listener
	parameters *client.SetTdlibParametersRequest
	usersMu    sync.RWMutex
	usersCache map[int64]*client.User
	chatsMu    sync.RWMutex
	chatsCache map[int64]*client.Chat
	ctx        context.Context
	cancel     context.CancelFunc
	ctxMu      sync.Mutex
}

func NewApp(svcCtx *svc.ServiceContext, apiId int32, apiHash, dataDir string) *TeleApp {
	_, err := client.SetLogVerbosityLevel(&client.SetLogVerbosityLevelRequest{
		NewVerbosityLevel: 1,
	})
	if err != nil {
		logger.Fatalf("[TeleApp] 设置日志级别错误, %s", err)
	}

	parameters := &client.SetTdlibParametersRequest{
		UseTestDc:           false,
		DatabaseDirectory:   filepath.Join(dataDir, ".tdlib", "database"),
		FilesDirectory:      filepath.Join(dataDir, ".tdlib", "files"),
		UseFileDatabase:     true,
		UseChatInfoDatabase: true,
		UseMessageDatabase:  true,
		UseSecretChats:      false,
		ApiId:               apiId,
		ApiHash:             apiHash,
		SystemLanguageCode:  "en",
		DeviceModel:         "Server",
		SystemVersion:       "1.0.0",
		ApplicationVersion:  "1.0.0",
	}

	app := &TeleApp{
		svcCtx:     svcCtx,
		parameters: parameters,
		chatsCache: make(map[int64]*client.Chat),
		usersCache: make(map[int64]*client.User),
	}
	return app
}

func (app *TeleApp) Login(options ...client.Option) (*client.User, error) {
	if app.user != nil {
		return app.user, nil
	}

	authorizer := client.ClientAuthorizer(app.parameters)
	go client.CliInteractor(authorizer)

	tdlibClient, err := client.NewClient(authorizer, options...)
	if err != nil {
		return nil, err
	}

	me, err := tdlibClient.GetMe()
	if err != nil {
		return nil, err
	}

	app.user = me
	app.tdClient = tdlibClient

	chats, err := app.tdClient.GetChats(&client.GetChatsRequest{Limit: 100})
	if err != nil {
		logger.Warnf("[TeleApp] 获取聊天列表失败: %v", err)
	} else {
		for _, chatId := range chats.ChatIds {
			chat, err := app.tdClient.GetChat(&client.GetChatRequest{ChatId: chatId})
			if err != nil {
				logger.Warnf("[TeleApp] 获取聊天信息失败, id: %d, %v", chatId, err)
				continue
			}
			logger.Infof("[TeleApp] 聊天列表: %s[%d]", chat.Title, chat.Id)
		}
	}

	listener := tdlibClient.GetListener()
	app.listener = listener

	app.ctxMu.Lock()
	app.ctx, app.cancel = context.WithCancel(context.Background())
	app.ctxMu.Unlock()

	go app.getUpdates(listener)

	return me, nil
}

func (app *TeleApp) Client() *client.Client {
	return app.tdClient
}

func (app *TeleApp) Close() error {
	if app.tdClient == nil {
		return nil
	}

	app.ctxMu.Lock()
	if app.cancel != nil {
		app.cancel()
	}
	app.ctxMu.Unlock()

	if app.listener != nil {
		app.listener.Close()
	}

	_, err := app.tdClient.Close()
	return err
}

func (app *TeleApp) getChat(chatId int64) (*client.Chat, error) {
	// 先尝试读锁读取缓存
	app.chatsMu.RLock()
	chat, ok := app.chatsCache[chatId]
	app.chatsMu.RUnlock()
	if ok {
		return chat, nil
	}

	// 缓存未命中，获取数据
	chat, err := app.tdClient.GetChat(&client.GetChatRequest{ChatId: chatId})
	if err != nil {
		return nil, err
	}

	// 写锁更新缓存
	app.chatsMu.Lock()
	app.chatsCache[chatId] = chat
	app.chatsMu.Unlock()
	return chat, nil
}

func (app *TeleApp) getUser(userId int64) (*client.User, error) {
	// 先尝试读锁读取缓存
	app.usersMu.RLock()
	user, ok := app.usersCache[userId]
	app.usersMu.RUnlock()
	if ok {
		return user, nil
	}

	// 缓存未命中，获取数据
	user, err := app.tdClient.GetUser(&client.GetUserRequest{UserId: userId})
	if err != nil {
		return nil, err
	}

	// 写锁更新缓存
	app.usersMu.Lock()
	app.usersCache[userId] = user
	app.usersMu.Unlock()
	return user, nil
}

func (app *TeleApp) getUpdates(listener *client.Listener) {
	app.ctxMu.Lock()
	ctx := app.ctx
	app.ctxMu.Unlock()

	for listener.IsActive() {
		select {
		case <-ctx.Done():
			logger.Infof("[TeleApp] 更新循环已取消，退出")
			return
		case update := <-listener.Updates:
			if update.GetType() != "updateNewMessage" {
				continue
			}

			// 仅处理文本消息
			updateNewMessage := update.(*client.UpdateNewMessage)
			message := updateNewMessage.Message
			if message.Content.MessageContentType() != "messageText" {
				continue
			}

			text := message.Content.(*client.MessageText)
			if text.Text == nil || text.Text.Text == "" {
				continue
			}

			// 获取来源Chat信息
			chat, err := app.getChat(message.ChatId)
			if err != nil {
				logger.Warnf("[TeleApp] 获取聊天信息失败, id: %d, %v", message.ChatId, err)
				continue
			}

			logger.Debugf("[TeleApp] 接收消息: %s[%d] -> %s(%d)", chat.Title, chat.Id, text.Text.Text, message.Id)

			// 过滤私聊和密聊
			switch chat.Type.ChatTypeType() {
			case client.TypeChatTypePrivate, client.TypeChatTypeSecret:
				continue
			}

			// 获取发送者信息
			senderID := int64(0)
			var senderName string
			var senderUsername *string

			if message.SenderId != nil {
				switch sender := message.SenderId.(type) {
				case *client.MessageSenderUser:
					senderID = sender.UserId
					user, err := app.getUser(sender.UserId)
					if err != nil {
						logger.Warnf("[TeleApp] 获取用户信息失败, id: %d, %v", sender.UserId, err)
						continue
					}
					senderName = user.FirstName
					if user.LastName != "" {
						senderName += " " + user.LastName
					}
					if user.Usernames != nil && len(user.Usernames.ActiveUsernames) > 0 {
						username := "@" + user.Usernames.ActiveUsernames[0]
						senderUsername = &username
					}
				}
			}

			// 保存消息到数据库（时间统一使用 UTC）
			msgData := &model.MessageData{
				MessageID:      message.Id,
				ChatID:         message.ChatId,
				SenderID:       senderID,
				SenderName:     senderName,
				SenderUsername: senderUsername,
				Text:           text.Text.Text,
				SentAt:         time.Unix(int64(message.Date), 0).UTC(),
			}

			if !app.shouldSaveMessage(message.ChatId) {
				logger.Debugf("[TeleApp] 群组 %d 在白名单/黑名单中被过滤，跳过保存", message.ChatId)
				continue
			}

			_, err = app.svcCtx.MessageModel.Create(ctx, msgData)
			if err != nil {
				logger.Errorf("[TeleApp] 保存消息失败, %v", err)
				continue
			}

			logger.Debugf("[TeleApp] 保存消息: %s[%d] -> %s: %s", chat.Title, chat.Id, senderName, text.Text.Text)
		}
	}
}

// shouldSaveMessage 判断是否应该保存该群组的消息
func (app *TeleApp) shouldSaveMessage(chatID int64) bool {
	cfg := app.svcCtx.Config.Summary
	whitelist := cfg.Whitelist
	blacklist := cfg.Blacklist

	// 白名单优先
	if len(whitelist) > 0 {
		for _, id := range whitelist {
			if id == chatID {
				return true
			}
		}
		return false
	}

	// 黑名单检查
	if len(blacklist) > 0 {
		for _, id := range blacklist {
			if id == chatID {
				return false
			}
		}
	}

	return true
}
