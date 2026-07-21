# Web 管理面板 + Docker 部署计划

## 架构

单二进制同时运行 Bot 服务和 Web 管理面板，前后端通过 `//go:embed` 打包，零构建步骤。

```
┌─────────────────────────────────┐
│         chat-summary-bot         │
│  ┌──────────┐  ┌────────────┐   │
│  │  Bot 服务  │  │ Web 管理面板 │   │
│  │ (现有代码)  │  │ (新增)      │   │
│  └────┬─────┘  └─────┬──────┘   │
│       │               │          │
│       └───────┬───────┘          │
│               │                   │
│         ┌─────▼──────┐           │
│         │  SQLite DB  │           │
│         └────────────┘           │
└─────────────────────────────────┘
         Port 8080 (Web UI)
```

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

## MVP 功能（按优先级排序）

### P0 — 核心（第一版必须有）

| 功能 | 说明 |
|------|------|
| Dashboard 概览 | 显示已加入群聊数量、今日消息数、最近摘要 |
| 摘要历史浏览 | 按群聊和日期查看历史摘要内容 |
| 群聊列表 | 显示所有已保存消息的群聊，支持白名单/黑名单标记 |
| 手动触发 /sum | 点击按钮触发指定群聊的即时摘要 |
| 日志查看 | 实时显示最近日志（调试用） |

### P1 — 增强（第二版）

| 功能 | 说明 |
|------|------|
| 在线配置编辑 | 修改 NotifyMode、RetentionDays 等运行时配置 |
| 性格分析记录 | 查看 /profile 的历史分析结果 |
| 消息搜索 | 按关键词搜索聊天记录 |
| 实时日志流 | WebSocket 推送 bot 日志 |

### P2 — 未来

| 功能 | 说明 |
|------|------|
| 多语言 | 中文/英文切换 |
| 摘要导出 | 导出为 Markdown/PDF |
| Webhook 通知 | 支持通过 Webhook 推送摘要 |

---

## 新增文件

```
internal/web/
├── server.go            # HTTP 服务器 + 路由注册
├── handler_dashboard.go # Dashboard 页面
├── handler_chats.go     # 群聊列表 + 管理
├── handler_summaries.go # 摘要历史
├── handler_logs.go      # 日志查看
├── handler_api.go       # REST API（前端 fetch 调用）
├── static/              # 通过 //go:embed 嵌入
│   ├── index.html
│   ├── style.css
│   └── app.js
```

---

## 被修改的文件

| 文件 | 改动 |
|------|------|
| `main.go` | 启动 bot 后另外启动 HTTP 服务器（goroutine） |
| `internal/config/config.go` | Config 新增 Web 配置段（端口、Token） |
| `etc/config.yaml.sample` | 新增 Web 配置示例 |
| `go.mod` | 新增 chi 依赖 |

---

## 配置新增

```yaml
Web:
  Enable: true           # 是否启用 Web 面板
  Port: 8080             # HTTP 端口
  Token: ""              # 认证 Token，为空时不开启认证
```

---

## Docker 部署

```dockerfile
# Dockerfile: 多阶段构建
# Stage 1: 编译 TDLib + Go 二进制
# Stage 2: 仅运行二进制（无编译依赖）
```

```yaml
# docker-compose.yml
services:
  bot:
    build: .
    ports:
      - "8080:8080"
    volumes:
      - ./etc/config.yaml:/app/etc/config.yaml
      - ./data:/app/data
    restart: unless-stopped
```

---

## 路由设计

```
GET  /                    → Dashboard 页面
GET  /chats               → 群聊列表页面
GET  /summaries           → 摘要历史页面
GET  /logs                → 日志页面
GET  /api/chats           → REST: 获取群聊列表
POST /api/chats/:id       → REST: 更新群聊设置
POST /api/summaries/:id   → REST: 触发手动摘要
GET  /api/summaries       → REST: 获取摘要列表
GET  /api/logs            → REST: 获取日志
```

认证方式：简单 Bearer Token 中间件。Token 为空时跳过认证（方便局域网使用）。

与 bot 的交互：Web handler 通过 svcCtx 访问 MessageModel、SummaryModel、TaskModel，直接读 DB + 触发任务。

---

## 实施工作量预估

| 阶段 | 内容 | 时间 |
|------|------|------|
| Phase 1 | 项目结构、路由、embed 模板 | 0.5 天 |
| Phase 2 | Dashboard + 群聊列表 + REST API | 1 天 |
| Phase 3 | 摘要历史 + 手动触发 | 0.5 天 |
| Phase 4 | Docker 化 + 文档 | 0.5 天 |
| **合计** | **MVP** | **2.5 天** |
