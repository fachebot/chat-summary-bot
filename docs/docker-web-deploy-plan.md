# Docker + Web 管理面板部署计划

## 目标

用户只需 3 步就能跑起来：

```
Step 1: docker compose up -d
Step 2: 打开浏览器 → 输入手机号 + 验证码登录 Telegram
Step 3: 进入 Dashboard 管理
```

整个过程不需要安装 Go、不需要编译 TDLib、不需要打开终端交互、不需要碰 YAML。

---

## 架构

```
┌──────────────────────────────────────────┐
│           chat-summary-bot                │
│  ┌──────────────┐  ┌──────────────────┐  │
│  │  Bot 服务      │  │  Web 管理面板     │  │
│  │  (TDLib + 摘要) │  │  ├─ 登录页       │  │
│  │               │  │  ├─ Dashboard   │  │
│  │  Authorizer ──────  ├─ 设置页       │  │
│  │  (WebAuthorizer)│  │  └─ 日志页      │  │
│  └──────┬───────┘  └──────┬───────────┘  │
│         │                 │               │
│         └──────┬──────────┘               │
│                │                          │
│          ┌─────▼──────┐                   │
│          │  SQLite DB  │                   │
│          └────────────┘                   │
└──────────────────────────────────────────┘
```

---

## 关键设计：WebAuthorizer

### 问题

当前 TDLib 登录使用 `client.CliInteractor(authorizer)` 在终端交互，无法在 Web 页面完成。

### 解决方案

实现自定义的 `WebAuthorizer`，替代 `CliInteractor`：

```
TDLib 授权状态机：
  AuthorizationStateWaitPhoneNumber  ── 等待用户输入手机号
  AuthorizationStateWaitCode         ── 等待输入验证码
  AuthorizationStateWaitPassword     ── 等待输入 2FA 密码（如有）
  AuthorizationStateReady            ── 登录完成
```

```go
type WebAuthorizer struct {
    mu       sync.Mutex
    state    client.AuthorizationState
    phoneCh  chan string
    codeCh   chan string
    passCh   chan string
    done     chan struct{}
}
```

- Bot 启动后暂停在 `WaitPhoneNumber` 状态，不阻塞
- Web 页面轮询 `/api/auth/state` 获取当前状态
- 用户提交表单 → POST 到 `/api/auth/phone`、`/api/auth/code`、`/api/auth/password`
- WebAuthorizer 接收输入 → 继续 TDLib 授权流程
- 登录完成后 TDLib 全功能运行

---

## 技术选型

| 层 | 选型 | 理由 |
|---|------|------|
| HTTP 框架 | Go `net/http` + `chi` 路由 | 轻量，无外部依赖 |
| 前端 | 纯 HTML + CSS + JS（Go `embed`） | 零构建步骤，打包进二进制 |
| CSS 框架 | 不引入，手写简单样式 | 减少依赖 |
| JS 库 | 不引入，原生 `fetch` + DOM | MVP 够用 |
| 嵌入 | `//go:embed` | Go 1.16+ 原生支持 |
| 认证 | 静态 Token 配置在 config.yaml | 简单够用 |
| 部署 | Docker Compose | 一键启动 |

---

## 新增文件

```
internal/web/
├── server.go            # HTTP 服务器 + 路由注册
├── auth.go              # WebAuthorizer 实现
├── handler_login.go     # 登录页面 + API
├── handler_dashboard.go # Dashboard 页面
├── handler_settings.go  # 配置管理页面
├── handler_logs.go      # 日志查看页面
├── static/              # 通过 //go:embed 嵌入
│   ├── index.html
│   ├── login.html
│   ├── dashboard.html
│   ├── settings.html
│   ├── logs.html
│   ├── style.css
│   └── app.js

Dockerfile               # 多阶段构建
docker-compose.yml       # 一键启动
.env.sample              # 环境变量模板
.dockerignore            # 构建忽略
```

---

## 被修改的文件

| 文件 | 改动 |
|------|------|
| `main.go` | 启动 Bot 后另外启动 HTTP 服务器（goroutine） |
| `internal/config/config.go` | `Config` 新增 `Web` 配置段 + 支持 `${ENV_VAR}` 环境变量替换 |
| `internal/teleapp/teleapp.go` | `Login()` 接受自定义 `Authorizer`，启动 WebAuthorizer |
| `etc/config.yaml.sample` | 新增 Web 配置段 + 环境变量说明 |
| `README.md` | 更新为 Docker 优先的部署流程 |

---

## 配置

```yaml
# config.yaml 新增
Web:
  Enable: true           # 是否启用 Web 面板
  Port: 8080             # HTTP 端口
  Token: ""              # 管理面板登录密码，空=不开启

# config.yaml 支持环境变量
TelegramApp:
  ApiId: ${TELEGRAM_API_ID}
  ApiHash: ${TELEGRAM_API_HASH}
LLM:
  APIKey: ${LLM_API_KEY}
  BaseURL: ${LLM_BASE_URL}
  Model: ${LLM_MODEL}
```

```bash
# .env 示例
TELEGRAM_API_ID=1570912
TELEGRAM_API_HASH=6e5be26cb0623190c048adb6bb066be7
LLM_API_KEY=sk-xxxxx
LLM_BASE_URL=https://api.deepseek.com
LLM_MODEL=deepseek-v4-flash
```

---

## Docker 部署

```dockerfile
# Dockerfile 多阶段构建
FROM ubuntu:22.04 AS builder
# 安装 TDLib 依赖 → 编译 TDLib → 编译 Go 二进制

FROM ubuntu:22.04
# 仅复制 TDLib runtime + Go 二进制
COPY --from=builder /usr/local/lib/libtdjson.so /usr/local/lib/
COPY chat-summary-bot /app/
CMD ["/app/chat-summary-bot", "-f", "/app/etc/config.yaml"]
```

```yaml
# docker-compose.yml
services:
  bot:
    build: .
    ports:
      - "8080:8080"
    volumes:
      - ./data:/app/data
      - ./etc/config.yaml:/app/etc/config.yaml
    env_file:
      - .env
    restart: unless-stopped
```

---

## 路由设计

### 页面

```
GET  /login      → 登录页面（手机号 → 验证码 → 2FA）
GET  /           → Dashboard 概览
GET  /settings   → 配置管理
GET  /logs       → 日志查看
```

### API

```
GET  /api/auth/state      → 获取当前 TDLib 授权状态
POST /api/auth/phone      → 提交手机号
POST /api/auth/code       → 提交验证码
POST /api/auth/password   → 提交 2FA 密码
GET  /api/status          → Bot 运行状态
GET  /api/chats           → 群聊列表
GET  /api/summaries       → 摘要历史
POST /api/settings        → 更新配置
GET  /api/logs            → 获取日志
```

---

## 用户最终体验

```bash
# 1. 克隆
git clone https://github.com/fachebot/chat-summary-bot.git
cd chat-summary-bot

# 2. 配置
cp .env.sample .env
# 编辑 .env 填入 Telegram API ID/Hash 和 LLM API Key

# 3. 启动
docker compose up -d

# 4. 打开浏览器完成登录
open http://localhost:8080
# → 输入手机号 → 验证码 → 进入 Dashboard
# Done.
```

---

## 实施工作量

| 阶段 | 内容 | 时间 |
|------|------|------|
| 1 | Docker + 环境变量支持 | 0.5 天 |
| 2 | WebAuthorizer（Web TDLib 登录） | 1 天 |
| 3 | 登录页面前端 | 0.5 天 |
| 4 | Dashboard + 配置编辑 + 日志查看 | 1 天 |
| **合计** | **MVP** | **3 天** |
