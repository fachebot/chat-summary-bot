package teleapp

import (
	"context"
	"path/filepath"
	"strings"
	"sync"

	"github.com/fachebot/chat-summary-bot/internal/lark"
	"github.com/fachebot/chat-summary-bot/internal/logger"
	"github.com/fachebot/chat-summary-bot/internal/market_indicators"
	"github.com/fachebot/chat-summary-bot/internal/svc"

	"github.com/zelenin/go-tdlib/client"
)

type TeleApp struct {
	svcCtx           *svc.ServiceContext
	user             *client.User
	tdClient         *client.Client
	listener         *client.Listener
	parameters       *client.SetTdlibParametersRequest
	usersMu          sync.RWMutex
	usersCache       map[int64]*client.User
	chatsMu          sync.RWMutex
	chatsCache       map[int64]*client.Chat
	ctx              context.Context
	cancel           context.CancelFunc
	ctxMu            sync.Mutex
	marketIndicators *market_indicators.MarketIndicators
	summaryHandler   func(ctx context.Context, chatID int64) error
	adminUserIds     []int64
	larkForwarder    *lark.Client
	processMu        sync.Mutex
}

func NewApp(svcCtx *svc.ServiceContext, apiId int32, apiHash, dataDir string, marketIndicators *market_indicators.MarketIndicators) *TeleApp {
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
		svcCtx:           svcCtx,
		parameters:       parameters,
		chatsCache:       make(map[int64]*client.Chat),
		usersCache:       make(map[int64]*client.User),
		marketIndicators: marketIndicators,
	}
	return app
}

func (app *TeleApp) SetSummaryHandler(handler func(ctx context.Context, chatID int64) error, adminUserIds []int64) {
	app.summaryHandler = handler
	app.adminUserIds = adminUserIds
}

func (app *TeleApp) SetLarkForwarder(forwarder *lark.Client) {
	app.larkForwarder = forwarder
}

func (app *TeleApp) User() *client.User {
	return app.user
}

func (app *TeleApp) TdlibParameters() *client.SetTdlibParametersRequest {
	return app.parameters
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

	return app.postLogin(tdlibClient)
}

func (app *TeleApp) LoginAsync(handler client.AuthorizationStateHandler, options ...client.Option) error {
	if app.user != nil {
		return nil
	}

	tdlibClient, err := client.NewClient(handler, options...)
	if err != nil {
		return err
	}

	app.tdClient = tdlibClient
	return nil
}

func (app *TeleApp) WaitForAuth() (*client.User, error) {
	if app.user != nil {
		return app.user, nil
	}
	return app.postLogin(app.tdClient)
}

func (app *TeleApp) postLogin(tdlibClient *client.Client) (*client.User, error) {
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
	baseCtx := app.ctx
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	app.ctx, app.cancel = context.WithCancel(baseCtx)
	snapshotCtx := app.ctx
	app.ctxMu.Unlock()

	historyCheckpoints, err := app.svcCtx.MessageModel.GetLatestMessageIDsByChat(snapshotCtx)
	if err != nil {
		logger.Warnf("[TeleApp] 获取历史补拉快照失败，将仅依赖实时更新: %v", err)
		historyCheckpoints = nil
	}

	go app.getUpdates(listener)
	app.startHistoryCatchUp(historyCheckpoints)

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

	botUsername := ""
	if app.user != nil && app.user.Usernames != nil && len(app.user.Usernames.ActiveUsernames) > 0 {
		botUsername = strings.ToLower(app.user.Usernames.ActiveUsernames[0])
	}

	for listener.IsActive() {
		select {
		case <-ctx.Done():
			logger.Infof("[TeleApp] 更新循环已取消，退出")
			return
		case update := <-listener.Updates:
			if update.GetType() != "updateNewMessage" {
				continue
			}

			updateNewMessage := update.(*client.UpdateNewMessage)
			app.handleIncomingMessage(ctx, updateNewMessage.Message, botUsername)
		}
	}
}

func (app *TeleApp) sendMessage(ctx context.Context, chatID int64, replyToMessageID int64, content string, parseMode ...client.TextParseMode) error {
	if content == "" {
		return nil
	}

	var err error
	var formattedText *client.FormattedText
	if len(parseMode) == 0 {
		formattedText = &client.FormattedText{
			Text: content,
		}
	} else {
		formattedText, err = client.ParseTextEntities(&client.ParseTextEntitiesRequest{
			Text:      content,
			ParseMode: parseMode[0],
		})
		if err != nil {
			return err
		}
	}

	req := &client.SendMessageRequest{
		ChatId: chatID,
		InputMessageContent: &client.InputMessageText{
			Text: formattedText,
			LinkPreviewOptions: &client.LinkPreviewOptions{
				IsDisabled: true,
			},
		},
	}

	if replyToMessageID > 0 {
		req.ReplyTo = &client.InputMessageReplyToMessage{
			MessageId: replyToMessageID,
		}
	}

	_, err = app.tdClient.SendMessage(req)
	return err
}
