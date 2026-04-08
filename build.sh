#!/bin/bash

# Chat Summary Bot 编译脚本

set -e

echo "🚀 开始编译 Chat Summary Bot..."

# 检查 Go 环境
if ! command -v go &> /dev/null; then
    echo "❌ 错误: 未找到 Go 环境，请先安装 Go 1.24+"
    exit 1
fi

echo "✅ Go 版本: $(go version)"

# 检查 TDLib
if ! pkg-config --exists tdlib; then
    echo "⚠️  警告: 未找到 TDLib，编译可能失败"
    echo "   请参考 README.md 安装 TDLib"
fi

# 创建数据目录
mkdir -p data

# 下载依赖
echo "📦 下载 Go 依赖..."
go mod download

# 编译
echo "🔨 编译中..."
go build -o chat-summary-bot .

if [ -f "chat-summary-bot" ]; then
    echo "✅ 编译成功！"
    echo "📁 可执行文件: ./chat-summary-bot"
    echo ""
    echo "运行方式:"
    echo "  ./chat-summary-bot -f etc/config.yaml"
else
    echo "❌ 编译失败"
    exit 1
fi