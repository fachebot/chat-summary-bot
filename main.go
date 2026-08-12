//go:build linux
// +build linux

package main

import (
	"flag"
	"os"
	"os/signal"
	"syscall"

	"github.com/fachebot/chat-summary-bot/internal/config"
	"github.com/fachebot/chat-summary-bot/internal/lark"
	"github.com/fachebot/chat-summary-bot/internal/logger"
	"github.com/fachebot/chat-summary-bot/internal/market_indicators"
	"github.com/fachebot/chat-summary-bot/internal/notify"
	"github.com/fachebot/chat-summary-bot/internal/scheduler"
	"github.com/fachebot/chat-summary-bot/internal/summarizer"
	"github.com/fachebot/chat-summary-bot/internal/svc"
	"github.com/fachebot/chat-summary-bot/internal/teleapp"
	"github.com/fachebot/chat-summary-bot/internal/web"

	"github.com/zelenin/go-tdlib/client"
)

var configFile = flag.String("f", "etc/config.yaml", "the config file")

func main() {
	flag.Parse()

	// 读取配置文件
	c, err := config.LoadFromFile(*configFile)
	if err != nil {
		logger.Fatalf("读取配置文件失败, %s", err)
	}

	// 创建数据目录
	if _, err := os.Stat("data"); os.IsNotExist(err) {
		err := os.Mkdir("data", 0755)
		if err != nil {
			logger.Fatalf("创建数据目录失败, %s", err)
		}
	}

	// 创建服务上下文
	svcCtx := svc.NewServiceContext(c)

	// 创建市场指标
	marketIndicators := market_indicators.New(svcCtx)

	// 创建TeleApp
	app := teleapp.NewApp(svcCtx, c.TelegramApp.ApiId, c.TelegramApp.ApiHash, "data", marketIndicators)
	app.SetLarkForwarder(lark.NewClient(&c.LarkForward, svcCtx.TransportProxy))

	var webServer *web.Server
	var schedulerInstance *scheduler.Scheduler

	if c.Web.Enable {
		// ---- Web 模式 ----
		// 代理在 WebAuthorizer.Handle 中同步（启动时清空 tdlib 旧代理并按配置重建），
		// Web 面板保存的新代理也会立即同步，因此无需通过 options 传入
		webAuth := web.NewWebAuthorizer(app.TdlibParameters(), &c.Sock5Proxy, app.StartUpdates)
		webServer = web.NewServer(&c.Web, c, webAuth, *configFile, app.ConnectionState, app)
		if err := webServer.Start(); err != nil {
			logger.Fatalf("[Web] 启动管理面板失败, %s", err)
		}

		logger.Infof("[TeleApp] 等待 Web 面板登录...")
		go func() {
			for {
				user, err := app.WaitForAuth()
				// 修改手机号触发重启：webServer 已重新发起登录，继续等待
				if webServer.IsRestarting() {
					webServer.MarkRestartConsumed()
					continue
				}
				if err != nil {
					logger.Fatalf("[TeleApp] 用户登录失败, %s", err)
				}
				webServer.SetUser(user)
				logger.Infof("[TeleApp] 用户 <%s %s>(%d) 登录成功", user.FirstName, user.LastName, user.Id)

				startBotServices(svcCtx, app, c, marketIndicators, user, &schedulerInstance)
				break
			}
		}()
	} else {
		// ---- CLI 模式（现有行为） ----
		// 每次启动清除 tdlib 中所有代理，然后按配置文件重建
		options := []client.Option{teleapp.ProxyOption(&c.Sock5Proxy)}
		user, err := app.Login(options...)
		if err != nil {
			logger.Fatalf("[TeleApp] 用户登录失败, %s", err)
		}
		logger.Infof("[TeleApp] 用户 <%s %s>(%d) 登录成功", user.FirstName, user.LastName, user.Id)

		schedulerInstance = startBotServices(svcCtx, app, c, marketIndicators, user, nil)
	}

	// 等待程序退出
	ch := make(chan os.Signal, 2)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	<-ch

	// 优雅关闭
	logger.Infof("正在关闭服务...")
	if schedulerInstance != nil {
		schedulerInstance.Stop()
	}
	if webServer != nil {
		webServer.Stop()
	}
	err = app.Close()
	if err != nil {
		logger.Infof("[TeleApp] 关闭失败, %v", err)
	}
	marketIndicators.Stop()
	svcCtx.Close()
	logger.Infof("服务已停止")
}

func startBotServices(svcCtx *svc.ServiceContext, app *teleapp.TeleApp, c *config.Config, marketIndicators *market_indicators.MarketIndicators, user *client.User, schedulerPtr **scheduler.Scheduler) *scheduler.Scheduler {
	// 运行市场指标
	marketIndicators.Start()

	// 创建总结器和通知器
	summarizerInstance := summarizer.NewSummarizer(
		svcCtx.LLMClient,
		svcCtx.MessageModel,
		user.Id)
	notifierInstance := notify.NewNotifier(
		app.Client(),
		&c.Summary,
	)

	// 创建并启动调度器
	schedulerInstance := scheduler.NewScheduler(
		summarizerInstance,
		notifierInstance,
		app.Client(),
		svcCtx.MessageModel,
		svcCtx.SummaryModel,
		svcCtx.TaskModel,
		svcCtx.DailyRunModel,
		&c.Summary,
		marketIndicators,
		&c.MarketIndicator,
	)
	if err := schedulerInstance.Start(); err != nil {
		logger.Fatalf("[Scheduler] 启动调度器失败: %s", err)
	}

	app.SetSummaryHandler(schedulerInstance.TriggerSummaryManual, c.Summary.AdminUserIds)

	if schedulerPtr != nil {
		*schedulerPtr = schedulerInstance
	}

	return schedulerInstance
}
