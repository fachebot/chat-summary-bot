package svc

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"

	"github.com/fachebot/talk-trace-bot/internal/coinank"
	"github.com/fachebot/talk-trace-bot/internal/config"
	"github.com/fachebot/talk-trace-bot/internal/ent"
	"github.com/fachebot/talk-trace-bot/internal/llm"
	"github.com/fachebot/talk-trace-bot/internal/logger"
	"github.com/fachebot/talk-trace-bot/internal/model"
	"github.com/fachebot/talk-trace-bot/internal/pi_cycle"

	_ "github.com/mattn/go-sqlite3"
	"golang.org/x/net/proxy"
)

type ServiceContext struct {
	Config         *config.Config
	DbClient       *ent.Client
	TransportProxy *http.Transport
	MessageModel   *model.MessageModel
	SummaryModel   *model.SummaryModel
	TaskModel      *model.TaskModel
	DailyRunModel  *model.DailyRunModel
	LLMClient      *llm.Client
	PiCycleClient  *pi_cycle.Client
	CoinankClient  *coinank.Client
}

func NewServiceContext(c *config.Config) *ServiceContext {
	// 创建数据库连接
	client, err := ent.Open("sqlite3", "file:data/sqlite.db?mode=rwc&_journal_mode=WAL&_fk=1")
	if err != nil {
		logger.Fatalf("打开数据库失败, %v", err)
	}
	if err := client.Schema.Create(context.Background()); err != nil {
		logger.Fatalf("创建数据库Schema失败, %v", err)
	}

	// 创建SOCKS5代理
	var transportProxy *http.Transport
	if c.Sock5Proxy.Enable {
		socks5Proxy := fmt.Sprintf("%s:%d", c.Sock5Proxy.Host, c.Sock5Proxy.Port)
		dialer, err := proxy.SOCKS5("tcp", socks5Proxy, nil, proxy.Direct)
		if err != nil {
			logger.Fatalf("创建SOCKS5代理失败, %v", err)
		}

		transportProxy = &http.Transport{
			Dial:            dialer.Dial,
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		}
	}

	piCycleClient, err := pi_cycle.NewClient(c.Sock5Proxy)
	if err != nil {
		logger.Fatalf("创建PiCycle客户端失败, %v", err)
	}

	coinankClient, err := coinank.NewClient(c.Sock5Proxy)
	if err != nil {
		logger.Fatalf("创建Coinank客户端失败, %v", err)
	}

	svcCtx := &ServiceContext{
		Config:         c,
		DbClient:       client,
		TransportProxy: transportProxy,
		MessageModel:   model.NewMessageModel(client.Message),
		SummaryModel:   model.NewSummaryModel(client.Summary),
		TaskModel:      model.NewTaskModel(client.Task),
		DailyRunModel:  model.NewDailyRunModel(client.DailyRun),
		LLMClient:      llm.NewClient(&c.LLM),
		PiCycleClient:  piCycleClient,
		CoinankClient:  coinankClient,
	}
	return svcCtx
}

func (svcCtx *ServiceContext) Close() {
	if err := svcCtx.DbClient.Close(); err != nil {
		logger.Errorf("关闭数据库失败, %v", err)
	}
}
