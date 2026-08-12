package teleapp

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fachebot/chat-summary-bot/internal/lark"
	"github.com/fachebot/chat-summary-bot/internal/logger"
	"github.com/fachebot/chat-summary-bot/internal/market_indicators"
	"github.com/fachebot/chat-summary-bot/internal/model"
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
	authResult       chan error

	connMu    sync.RWMutex
	connState string

	chatListMu    sync.Mutex
	chatListCache groupChatsCacheEntry
}

// GroupChatInfo 群聊列表项。
type GroupChatInfo struct {
	ID    int64
	Title string
}

type groupChatsCacheEntry struct {
	chats     []GroupChatInfo
	expiresAt time.Time
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

	// 预创建 TDLib 数据库/文件目录，避免 binlog 初始化因目录不存在而报错
	EnsureTdlibDirs(parameters)

	app := &TeleApp{
		svcCtx:           svcCtx,
		parameters:       parameters,
		chatsCache:       make(map[int64]*client.Chat),
		usersCache:       make(map[int64]*client.User),
		marketIndicators: marketIndicators,
	}
	return app
}

// EnsureTdlibDirs 确保 TDLib 的数据库目录与文件目录存在（含父目录）。
// 在客户端启动前及登录流程重启（Destroy 清理后）时调用。
func EnsureTdlibDirs(parameters *client.SetTdlibParametersRequest) {
	if parameters == nil {
		return
	}
	for _, dir := range []string{parameters.DatabaseDirectory, parameters.FilesDirectory} {
		if dir == "" {
			continue
		}
		if err := os.MkdirAll(dir, 0755); err != nil {
			logger.Warnf("[TeleApp] 创建目录失败 %s: %v", dir, err)
		}
	}
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

// LoginAsync 异步发起登录。返回的 done 通道在本次登录流程完全结束
// （客户端就绪或已关闭）时关闭；重启登录时需等待旧流程的 done 再启动新客户端，
// 避免新旧 TDLib 进程在同一个数据目录上冲突。
func (app *TeleApp) LoginAsync(handler client.AuthorizationStateHandler, options ...client.Option) (<-chan struct{}, error) {
	if app.user != nil {
		return nil, nil
	}

	// 每次登录创建一个独立的结果通道；goroutine 捕获本次通道，
	// 避免登录流程重启时旧 goroutine 的退出错误写进新通道。
	app.authResult = make(chan error, 1)
	result := app.authResult
	done := make(chan struct{})
	go func() {
		defer close(done)
		tdlibClient, err := client.NewClient(handler, options...)
		if err != nil {
			result <- err
			return
		}
		app.tdClient = tdlibClient
		result <- nil
	}()

	return done, nil
}

// ResetUpdates 重置更新监听状态，供 Web 登录流程重启（修改手机号）时使用，
// 使新客户端能重新挂载监听并捕获连接状态。
func (app *TeleApp) ResetUpdates() {
	app.ctxMu.Lock()
	if app.cancel != nil {
		app.cancel()
		app.cancel = nil
	}
	app.ctx = nil
	if app.listener != nil {
		app.listener.Close()
		app.listener = nil
	}
	app.ctxMu.Unlock()

	app.connMu.Lock()
	app.connState = ""
	app.connMu.Unlock()
}

func (app *TeleApp) WaitForAuth() (*client.User, error) {
	if app.user != nil {
		return app.user, nil
	}
	if app.authResult != nil {
		if err := <-app.authResult; err != nil {
			return nil, err
		}
	}
	return app.postLogin(app.tdClient)
}

func (app *TeleApp) postLogin(tdlibClient *client.Client) (*client.User, error) {
	// 先挂载更新监听，捕获连接状态（含登录期间与 GetMe 阶段的 Connecting→Ready）
	app.StartUpdates(tdlibClient)

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

	app.ctxMu.Lock()
	snapshotCtx := app.ctx
	app.ctxMu.Unlock()

	historyCheckpoints, err := app.svcCtx.MessageModel.GetLatestMessageIDsByChat(snapshotCtx)
	if err != nil {
		logger.Warnf("[TeleApp] 获取历史补拉快照失败，将仅依赖实时更新: %v", err)
		historyCheckpoints = nil
	}

	app.startHistoryCatchUp(historyCheckpoints)

	return me, nil
}

// StartUpdates 挂载更新监听并启动更新循环。幂等：已启动时重复调用不生效。
// 应在客户端创建后尽早调用，以便捕获连接状态变化。
func (app *TeleApp) StartUpdates(c *client.Client) {
	if c == nil {
		return
	}
	app.ctxMu.Lock()
	defer app.ctxMu.Unlock()
	if app.cancel != nil {
		return // 已启动
	}
	app.ctx, app.cancel = context.WithCancel(context.Background())
	app.listener = c.GetListener()
	go app.getUpdates(app.listener)
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
			switch u := update.(type) {
			case *client.UpdateConnectionState:
				app.connMu.Lock()
				app.connState = u.State.ConnectionStateType()
				app.connMu.Unlock()
			case *client.UpdateNewMessage:
				// 登录未完成时忽略消息，避免 app.user 未就绪导致的问题
				if app.user == nil {
					continue
				}
				botUsername := ""
				if app.user.Usernames != nil && len(app.user.Usernames.ActiveUsernames) > 0 {
					botUsername = strings.ToLower(app.user.Usernames.ActiveUsernames[0])
				}
				app.handleIncomingMessage(ctx, u.Message, botUsername)
			}
		}
	}
}

// ConnectionState 返回最近一次 updateConnectionState 的状态类型（如 connectionStateReady），
// 尚未建立监听时返回空字符串。
func (app *TeleApp) ConnectionState() string {
	app.connMu.RLock()
	defer app.connMu.RUnlock()
	return app.connState
}

// SummaryModel 返回摘要数据模型（可能为 nil）。
func (app *TeleApp) SummaryModel() *model.SummaryModel {
	if app.svcCtx == nil {
		return nil
	}
	return app.svcCtx.SummaryModel
}

// MessageModel 返回消息数据模型（可能为 nil）。
func (app *TeleApp) MessageModel() *model.MessageModel {
	if app.svcCtx == nil {
		return nil
	}
	return app.svcCtx.MessageModel
}

// ListGroupChats 返回当前所有群聊（实时 TDLib 主列表 + 数据库有消息记录的群，合并去重，按标题排序）。
// 结果带 30 秒 TTL 缓存，避免翻页时反复请求 TDLib。
func (app *TeleApp) ListGroupChats() ([]GroupChatInfo, error) {
	app.chatListMu.Lock()
	if !app.chatListCache.expiresAt.IsZero() && time.Now().Before(app.chatListCache.expiresAt) {
		chats := app.chatListCache.chats
		app.chatListMu.Unlock()
		return chats, nil
	}
	app.chatListMu.Unlock()

	seen := make(map[int64]struct{})
	chats := make([]GroupChatInfo, 0)

	if app.tdClient != nil {
		// 1. TDLib 实时主聊天列表
		if res, err := app.tdClient.GetChats(&client.GetChatsRequest{Limit: 100}); err != nil {
			logger.Warnf("[TeleApp] 获取实时群聊列表失败: %v", err)
		} else {
			for _, id := range res.ChatIds {
				chat, err := app.getChat(id)
				if err != nil || !isSupportedGroupChat(chat) {
					continue
				}
				if _, ok := seen[id]; ok {
					continue
				}
				seen[id] = struct{}{}
				chats = append(chats, GroupChatInfo{ID: id, Title: chat.Title})
			}
		}

		// 2. 数据库补充：有消息记录的群（避免实时列表前 100 之外被遗漏）
		if mm := app.MessageModel(); mm != nil {
			if ids, err := mm.GetAllChatIDs(context.Background()); err != nil {
				logger.Warnf("[TeleApp] 获取数据库群聊列表失败: %v", err)
			} else {
				for _, id := range ids {
					if _, ok := seen[id]; ok {
						continue
					}
					chat, err := app.getChat(id)
					if err != nil || !isSupportedGroupChat(chat) {
						continue
					}
					seen[id] = struct{}{}
					chats = append(chats, GroupChatInfo{ID: id, Title: chat.Title})
				}
			}
		}
	}

	sort.Slice(chats, func(i, j int) bool {
		return strings.ToLower(chats[i].Title) < strings.ToLower(chats[j].Title)
	})

	app.chatListMu.Lock()
	app.chatListCache = groupChatsCacheEntry{chats: chats, expiresAt: time.Now().Add(30 * time.Second)}
	app.chatListMu.Unlock()

	return chats, nil
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
