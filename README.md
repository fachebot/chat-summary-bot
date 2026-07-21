# Chat Summary Bot

Telegram 群聊消息摘要 Bot - 使用你的账号，自动记录群聊并生成 AI 摘要

<img width="663" height="1010" alt="image" src="https://github.com/user-attachments/assets/110748f2-1cc5-4f04-8e7b-afb8027edcb3" />

## 项目介绍

在 Telegram 群聊中，你是否经常遇到以下情况：

- 💬 **消息太多**：群聊消息几千条，根本爬不完
- ⏰ **时间有限**：上班忙工作，下班想休息，没时间一条条看
- 🤔 **怕错过重点**：关键信息淹没在大量闲聊中

**Chat Summary Bot** 就是为了解决这些问题而生的：

1. 📊 **自动记录**：在你加入的群聊中自动保存所有消息
2. 🧠 **AI 智能摘要**：按话题聚合，生成有结构的摘要
3. 🔗 **一键跳转**：每条摘要可点击链接，直接跳转到原始消息
4. 🔒 **私密查看**：摘要发送到你的私信，群成员完全不知晓
5. 🚨 **Lark 应用内加急**：实时监控指定 Telegram 用户，命中后立即转发到 Lark，并对该告警消息发送应用内加急

### 摘要示例

```
📊 群组总结
🏠 XXX 交流群
📅 2026-04-07 至 2026-04-08 (UTC)

1. BTC 走势讨论
   - 张三: 昨晚鲍威尔讲话后BTC跌破65000，短期可能继续盘整 [link]
   - 李四: 觉得可以在64000附近抄底，止损设在62000 [link]
   ...

2. 新人入门问题
   - 王五: 请问在哪里可以看到Pi Cycle指标？ [link]
   ...
```

### 适用场景

- 💰 **交易群**：币圈、股票群，快速掌握交易信号和讨论
- 📚 **学习群**：技术讨论群，快速了解今天的讨论重点
- 💼 **工作群**：不遗漏重要的决策和讨论
- 🏠 **社区运营**：了解群友关心什么，优化运营
- 📢 **公告群**：重要公告不遗漏

## 特点

- 🔓 **无审核加群**：使用你的 Telegram 账号，直接记录你已加入的群聊，无需群主审核
- 🔒 **私密通知**：摘要发送到你的私信，群成员完全不知晓
- 📊 **话题聚合**：AI 识别讨论话题，生成更有价值的摘要
- 💾 **崩溃恢复**：服务重启后可自动恢复未完成的摘要任务
- 📈 **BTC 抄底指标**：内置 Pi Cycle、MVRV、CVDD 信号

## 功能特性

- 📝 **消息存储**：自动保存所有群聊消息到 SQLite 数据库
- 🤖 **AI 总结**：使用 LLM 每日自动总结每位群成员的聊天记录
- 🧹 **自动清理**：定时清理过期消息，保持数据库精简
- 📢 **智能通知**：支持私信、群发或两者，自动处理消息长度限制
- 🚨 **跨平台告警转发**：支持把指定 Telegram 用户的实时消息转发到 Lark，文本、图片、文件可应用内加急
- 🔌 **多 LLM 支持**：支持 OpenAI、Azure、DeepSeek、Qwen 等多种 LLM 模型
- ⚡ **Token 管理**：自动处理 token 超限，智能拆分长文本
- 🧑 **性格分析**：回复用户消息并发送 `@机器人 /profile`，AI 基于聊天记录分析该用户的性格特征

## 系统要求

- Linux 系统（推荐使用 WSL2）
- Go 1.24+
- TDLib 库（Telegram 官方库）
- SQLite3

## 编译步骤

### WSL2 快速编译（推荐）

如果你使用 WSL2，可以使用自动化脚本：

```bash
# 1. 安装所有依赖（包括 Go 和 TDLib）
chmod +x install_deps.sh
./install_deps.sh

# 2. 编译项目
chmod +x build.sh
./build.sh
```

详细说明请参考 [BUILD_WSL2.md](BUILD_WSL2.md)

### 手动编译步骤

#### 1. 安装 TDLib

在 WSL2/Linux 中安装 TDLib：

```bash
# Ubuntu/Debian
sudo apt-get update
sudo apt-get install -y build-essential cmake gperf libssl-dev zlib1g-dev libreadline-dev

# 下载并编译 TDLib
git clone https://github.com/tdlib/td.git
cd td
mkdir build
cd build
cmake -DCMAKE_BUILD_TYPE=Release ..
cmake --build . -j$(nproc)
sudo cmake --install .
```

#### 2. 安装 Go 依赖

```bash
go mod download
```

#### 3. 编译项目

```bash
# 使用提供的编译脚本
chmod +x build.sh
./build.sh

# 或手动编译
go build -o chat-summary-bot .
```

## 配置

1. 复制配置文件模板：

```bash
cp etc/config.yaml.sample etc/config.yaml
```

2. 编辑 `etc/config.yaml`，配置以下内容：

- **TelegramApp**: 配置 Telegram API ID 和 Hash（从 https://my.telegram.org 获取）
- **LLM**: 配置 LLM API 端点和密钥
- **Summary**: 配置总结时间、保留天数和通知方式
- **LarkForward**: 配置 Lark 实时转发、自建应用凭证和要监控的 Telegram 用户

## 运行

```bash
./chat-summary-bot -f etc/config.yaml
```

## 命令

在群聊中 `@机器人` 并发送以下命令触发相应功能：

### `/sum` / `/summary`

手动触发群聊摘要，立即生成最近 24 小时的讨论总结。

> ⚠️ 仅 `AdminUserIds` 白名单中的用户有权使用。

```
@bot /sum
```

### `/getuserid`

获取目标用户的 Telegram ID。需要**回复**该用户的一条消息后发送：

```
@bot /getuserid
```

Bot 会回复被回复用户的 ID、昵称和用户名。

### `/profile`

AI 性格分析。需要**回复**目标用户的一条消息后发送，Bot 会从数据库中查询该用户的所有聊天记录，通过 LLM 分析性格特征、沟通风格、行为模式等。

```
@bot /profile
```

分析结果格式：
```
🧑 性格分析：张三 (ID: 123456789)

[分析内容...]

📊 基于 128 条聊天记录分析
```

> 聊天记录过多时会自动分块处理，再汇总为完整报告。

## 配置说明

### TelegramApp

- `ApiId`: Telegram API ID
- `ApiHash`: Telegram API Hash

### LLM

- `BaseURL`: LLM API 端点（支持 OpenAI 兼容的 API）
  - OpenAI: `https://api.openai.com/v1`
  - DeepSeek: `https://api.deepseek.com/v1`
  - Qwen: `https://dashscope.aliyuncs.com/compatible-mode/v1`
- `APIKey`: API 密钥
- `Model`: 模型名称（如 `gpt-4o`, `deepseek-chat`, `qwen-plus`）
- `MaxTokens`: 模型上下文窗口大小
- `MaxOutputTokens`: 单次请求允许的最大输出 tokens，未设置时会按上下文窗口自动推导

如果你使用支持超长上下文的模型，建议把输入窗口和输出窗口分开配置。例如 DeepSeek V4 可配置为：

```yaml
LLM:
  BaseURL: https://api.deepseek.com/v1
  APIKey: your-api-key-here
  Model: deepseek-v4
  MaxTokens: 1000000
  MaxOutputTokens: 384000
```

### Summary

- `Cron`: Cron 表达式，定义总结执行时间（如 `"0 23 * * *"` 表示每天 23:00）
- `RetentionDays`: 消息保留天数
- `RangeDays`: 总结天数，1=仅昨天，7=最近7天
- `NotifyMode`: 通知模式
  - `private`: 仅私信通知
  - `group`: 仅群内通知
  - `both`: 两者都通知
- `NotifyUserIds`: 私信通知的目标用户 ID 列表
- `ChatNotifyModes`: 按群聊单独覆盖通知方式，key=群聊ID，value=`private`/`group`/`both`；不配置则使用全局 `NotifyMode`
- `Whitelist`: 白名单群组 ID 列表，设置后只保存和总结白名单群组（与黑名单互斥，优先使用白名单）
- `Blacklist`: 黑名单群组 ID 列表，设置后不保存和总结黑名单群组（白名单为空时生效）

> ⚠️ 白名单和黑名单互斥，优先使用白名单。设置白名单后只处理白名单中的群组；白名单为空时使用黑名单过滤。

### LarkForward

- `Enable`: 是否启用 Telegram -> Lark 实时转发
- `AppID`: Lark 自建应用 App ID
- `AppSecret`: Lark 自建应用 App Secret
- `UrgentUserIDType`: 直发私聊与应用内加急使用的用户 ID 类型，支持 `open_id`、`union_id`、`user_id`
- `UrgentUserIDs`: 接收私聊告警并执行应用内加急的 Lark 用户 ID 列表
- `MonitorTelegramUserIDs`: 要监控的 Telegram 数字用户 ID 列表
- `MonitorTelegramUsernames`: 要监控的 Telegram 用户名列表，支持带 `@`

建议做法：

1. 在 Lark 开发者后台开启机器人能力，并确保应用具备“发送消息”“上传图片或文件资源”“发送应用内加急消息”等权限。
2. 确保机器人对 `UrgentUserIDs` 中的用户具备可用性，否则无法主动发起私聊。
3. 将需要接收私聊告警并触发应用内加急的用户 ID 填到 `UrgentUserIDs`。

当前实时转发默认覆盖文本、图片、文件、视频、音频、语音、动画消息；其中图片会优先按图片发送，失败时自动降级为文件。每个命中的 Lark 用户都会收到一条机器人私聊消息，再对这条私聊消息触发应用内加急。Lark 侧限制为图片不超过 10MB、文件不超过 30MB，超限时会保留文本告警并提示附件发送失败。

## 工作流程

1. 使用你的 Telegram 账号登录（而非 Bot Token）
2. App 自动监听并保存你所在群聊的消息
3. 所有消息自动保存到 SQLite 数据库
4. 如果启用了 `LarkForward`，命中的指定用户消息会被实时转发到配置用户的 Lark 私聊，并对对应私聊消息触发应用内加急
5. 按配置的 cron 时间执行每日总结：
   - 生成每位成员的聊天摘要
   - 保存摘要到数据库
   - 发送通知（私信/群发，由 NotifyMode 控制）
     - 清理过期消息（保留 RetentionDays + 1 天）
6. **命令支持**：在群聊中 `@机器人` 可发送 `/sum`、`/profile`、`/getuserid` 等命令，详情见[命令](#命令)章节

## 注意事项

- 首次运行需要登录 Telegram，按照提示输入验证码
- 确保 LLM API 密钥有效且有足够额度
- 如果启用 Lark 转发，需确保机器人对目标用户有私聊可用性，并发布带有发送应用内加急权限的新版本应用
- 消息清理会在摘要生成后执行，确保不会误删当日数据
- Telegram 消息长度限制为 4096 字符，超出会自动拆分

## 测试

项目包含完整的单元测试和集成测试。

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
export LLM_BASE_URL="https://api.openai.com/v1"  # 可选
export LLM_MODEL="gpt-3.5-turbo"  # 可选

go test -tags=integration ./internal/llm -v
```

详细测试说明请参考 [internal/llm/README_TEST.md](internal/llm/README_TEST.md)

## License

See LICENSE file for details.
