# Chat Summary Bot

Telegram 群聊消息摘要 Bot — 使用你的 Telegram 账号，自动记录群聊、生成 AI 摘要、分析成员性格、监控 BTC 指标、跨平台告警转发。

<img width="663" height="1010" alt="image" src="https://github.com/user-attachments/assets/110748f2-1cc5-4f04-8e7b-afb8027edcb3" />

## 项目简介

日常加了太多 Telegram 群聊，消息太多看不过来？这个 Bot 用你的 Telegram 账号登录后，自动记录你所有群聊的消息，通过 LLM 生成结构化摘要、分析群成员性格特征，并提供跨平台实时告警转发。

**一句话**：你聊你的，Bot 帮你读、帮你总结、帮你分析。

---

## 核心功能

### 📝 AI 群聊摘要

自动保存所有群聊消息到 SQLite 数据库，每天按 cron 时间（如每天 UTC 0 点）执行 AI 摘要，按话题聚合生成结构化报告：

- 支持私信通知（群员不知晓）、群内通知、或两者同时
- 支持为不同群聊单独设置通知方式
- 消息过多时自动分块处理，识别 5-15 个核心话题
- 每次总结完成后自动清理过期消息（保留天数可配置）
- 服务崩溃后自动恢复未完成的摘要任务

```
📊 群组总结
🏠 XXX 交流群
📅 2026-04-07 至 2026-04-08 (UTC)

1. BTC 走势讨论
   - 张三: 昨晚鲍威尔讲话后BTC跌破65000，短期可能继续盘整 [link]
   - 李四: 觉得可以在64000附近抄底，止损设在62000 [link]

2. 新人入门问题
   - 王五: 请问在哪里可以看到Pi Cycle指标？ [link]
```

**手动触发** — 在群聊中 `@机器人 /sum` 立即生成最近 24 小时摘要：

```
@bot /sum
```

> ⚠️ 仅 `AdminUserIds` 白名单中的用户有权使用。

---

### 🧑 成员性格画像

回复某人的消息并发送 `@机器人 /profile`，Bot 从数据库中查询该用户的所有聊天记录，通过 LLM 分析其性格特征、沟通风格、行为模式等。聊天记录过多时自动分块分析并汇总。

```
@bot /profile
@bot /profile -p          # 结果私信发送
```

分析结果：

```
🧑 性格分析：张三 (ID: 123456789)

[分析内容...]

📊 基于 128 条聊天记录分析
```

> ⚠️ 仅 `AdminUserIds` 白名单中的用户有权使用。`/profile` 与 `/sum` 同一时间只能处理一个请求，有其他请求在处理时会提示稍后再试。

---

### 🔍 用户信息查询

回复某人的消息并发送 `@机器人 /getuserid`，快速获取对方的 Telegram ID、昵称和用户名：

```
@bot /getuserid
@bot /getuserid -p          # 结果私信发送
```

Bot 回复：

```
ID: 123456789
昵称: 张三
用户名: @zhangsan
```

---

### 📊 BTC 市场指标

内置三个 BTC 抄底信号指标，定时广播到群聊：

| 指标 | 说明 |
|------|------|
| **Pi Cycle Top/Bottom** | 基于 SMA(471)×0.745 / EMA(150)×1.0，检测市场顶底信号 |
| **MVRV Z-Score** | 市值与已实现价值的偏离程度，判断估值高低 |
| **Bitcoin CVDD** | 累计价值销毁天数，识别历史底部区域 |

在群聊中发送 `@机器人 抄底` 可手动触发最新指标数据。

---

### 🚨 Lark 实时告警

监控指定 Telegram 用户的消息，命中后实时转发到 Lark（飞书）私聊，并对告警消息发送**应用内加急**（App-level urgent notification）。

- 支持文本、图片、文件、视频、音频、语音、动画消息
- 图片优先按图片发送，失败时自动降级为文件
- 每位已配置的 Lark 用户都会收到机器人私聊 + 应用内加急

> 配置 `LarkForward` 后自动运行，无需手动触发。

---

## 适用场景

- 💰 **交易群**：币圈、股票群，快速掌握交易信号和讨论重点
- 📚 **学习群**：技术讨论群，快速了解每天的讨论精华
- 💼 **工作群**：不遗漏重要决策和讨论
- 🏠 **社区运营**：了解群友关心什么，优化运营
- 📢 **公告群**：重要公告不遗漏

---

## 快速开始

### 系统要求

- Linux 系统（推荐使用 WSL2）
- Go 1.24+
- TDLib 库（Telegram 官方库）
- SQLite3

### 安装与编译

**使用 WSL2（推荐）：**

```bash
# 安装所有依赖（包括 Go 和 TDLib）
chmod +x install_deps.sh
./install_deps.sh

# 编译项目
chmod +x build.sh
./build.sh
```

详细说明请参考 [BUILD_WSL2.md](BUILD_WSL2.md)

**手动编译：**

```bash
# 1. 安装 TDLib
# Ubuntu/Debian
sudo apt-get install -y build-essential cmake gperf libssl-dev zlib1g-dev libreadline-dev
git clone https://github.com/tdlib/td.git
cd td
mkdir build && cd build
cmake -DCMAKE_BUILD_TYPE=Release ..
cmake --build . -j$(nproc)
sudo cmake --install .

# 2. 安装 Go 依赖并编译
go mod download
go build -o chat-summary-bot .
```

### 配置

```bash
cp etc/config.yaml.sample etc/config.yaml
```

编辑 `etc/config.yaml`，配置以下内容：

| 配置项 | 说明 |
|--------|------|
| `TelegramApp` | Telegram API ID 和 Hash（从 https://my.telegram.org 获取） |
| `LLM` | LLM API 端点和密钥（支持 OpenAI / DeepSeek / Qwen 等） |
| `Summary` | 总结 cron 时间、消息保留天数、通知方式 |
| `LarkForward` | （可选）Lark 转发凭证和监控用户 |

### 运行

```bash
./chat-summary-bot -f etc/config.yaml
```

首次运行需要登录 Telegram，按照提示输入验证码。

---

## 配置说明

### TelegramApp

- `ApiId`：Telegram API ID
- `ApiHash`：Telegram API Hash

### LLM

- `BaseURL`：LLM API 端点，支持 OpenAI 兼容格式
  - OpenAI：`https://api.openai.com/v1`
  - DeepSeek：`https://api.deepseek.com/v1`
  - Qwen：`https://dashscope.aliyuncs.com/compatible-mode/v1`
- `APIKey`：API 密钥
- `Model`：模型名称（如 `gpt-4o`、`deepseek-chat`、`qwen-plus`）
- `MaxTokens`：模型上下文窗口大小
- `MaxOutputTokens`：单次请求最大输出 tokens，未设置时自动推导

对于超长上下文模型，建议分开配置输出预算。例如 DeepSeek V4：

```yaml
LLM:
  BaseURL: https://api.deepseek.com/v1
  APIKey: your-api-key-here
  Model: deepseek-v4
  MaxTokens: 1000000
  MaxOutputTokens: 384000
```

### Summary

| 字段 | 说明 |
|------|------|
| `Cron` | Cron 表达式，如 `"0 0 * * *"` 每天 UTC 0 点 |
| `RetentionDays` | 消息保留天数，过期的消息会在每日总结后自动清理 |
| `RangeDays` | 总结天数，1=仅昨天，7=最近 7 天 |
| `NotifyMode` | 通知模式：`private`（私信）/ `group`（群内）/ `both`（两者） |
| `NotifyUserIds` | 私信通知的目标用户 ID 列表 |
| `ChatNotifyModes` | 按群聊单独覆盖通知方式，key=群聊 ID，value=`private`/`group`/`both` |
| `Whitelist` | 白名单群组 ID，设置后只保存和总结白名单群组（与黑名单互斥） |
| `Blacklist` | 黑名单群组 ID，设置后跳过这些群组 |
| `AdminUserIds` | 有权使用 `/sum` 和 `/profile` 命令的用户 ID 列表 |

### LarkForward

- `Enable`：是否启用 Telegram → Lark 实时转发
- `AppID` / `AppSecret`：Lark 自建应用凭证
- `UrgentUserIDType`：用户 ID 类型（`open_id` / `union_id` / `user_id`）
- `UrgentUserIDs`：接收私聊告警并触发应用内加急的 Lark 用户 ID 列表
- `MonitorTelegramUserIDs` / `MonitorTelegramUsernames`：要监控的 Telegram 用户

建议做法：

1. 在 Lark 开发者后台开启机器人能力，确保具备"发送消息""上传文件""发送应用内加急"等权限
2. 确保机器人对 `UrgentUserIDs` 中的用户有私聊可用性
3. 图片限制 10MB、文件限制 30MB，超限时保留文本告警并提示

### MarketIndicator

- `Enable`：是否启用 BTC 市场指标广播
- `Cron`：cron 表达式，如 `"0 1 * * *"` 每天 UTC 1 点
- `Whitelist` / `Blacklist`：群组白名单/黑名单

---

## 注意事项

- 首次运行需要登录 Telegram，按提示输入验证码
- 确保 LLM API 密钥有效且有足够额度
- 如果启用 Lark 转发，需发布带有发送应用内加急权限的新版本应用
- 消息清理在每日总结生成后执行，确保不会误删当日数据
- Telegram 消息长度限制为 4096 字符，超出会自动拆分
- `/sum` 和 `/profile` 有并发限制，同一时间只能处理一个请求

---

## 测试

### 运行单元测试

```bash
# 运行所有测试
go test ./...

# 运行 LLM 模块测试
go test ./internal/llm -v

# 查看测试覆盖率
go test ./internal/llm -cover
```

### 运行集成测试

集成测试需要真实的 LLM API key（可选）：

```bash
export LLM_API_KEY="your-api-key"
export LLM_BASE_URL="https://api.openai.com/v1"
export LLM_MODEL="gpt-3.5-turbo"

go test -tags=integration ./internal/llm -v
```

详细测试说明请参考 [internal/llm/README_TEST.md](internal/llm/README_TEST.md)

---

## License

See LICENSE file for details.
