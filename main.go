//go:build linux
// +build linux

package main

import (
	"flag"
	"os"
	"os/signal"
	"syscall"

	"github.com/fachebot/talk-trace-bot/internal/config"
	"github.com/fachebot/talk-trace-bot/internal/logger"
	"github.com/fachebot/talk-trace-bot/internal/market_indicators"
	"github.com/fachebot/talk-trace-bot/internal/notify"
	"github.com/fachebot/talk-trace-bot/internal/scheduler"
	"github.com/fachebot/talk-trace-bot/internal/summarizer"
	"github.com/fachebot/talk-trace-bot/internal/svc"
	"github.com/fachebot/talk-trace-bot/internal/teleapp"

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

	// 运行Telegram App
	options := make([]client.Option, 0)
	if c.Sock5Proxy.Enable {
		options = append(options, client.WithProxy(&client.AddProxyRequest{
			Server: c.Sock5Proxy.Host,
			Port:   c.Sock5Proxy.Port,
			Enable: c.Sock5Proxy.Enable,
			Type:   &client.ProxyTypeSocks5{},
		}))
	}

	// 创建TeleApp
	app := teleapp.NewApp(svcCtx, c.TelegramApp.ApiId, c.TelegramApp.ApiHash, "data", marketIndicators)
	user, err := app.Login(options...)
	if err != nil {
		logger.Fatalf("[TeleApp] 用户登录失败, %s", err)
	}
	logger.Infof("[TeleApp] 用户 <%s %s>(%d) 登录成功", user.FirstName, user.LastName, user.Id)

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

	// 等待程序退出
	ch := make(chan os.Signal, 2)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	<-ch

	// 优雅关闭
	logger.Infof("正在关闭服务...")
	schedulerInstance.Stop()
	err = app.Close()
	if err != nil {
		logger.Infof("[TeleApp] 关闭失败, %v", err)
	}
	marketIndicators.Stop()
	svcCtx.Close()
	logger.Infof("服务已停止")
}
