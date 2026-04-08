#!/bin/bash
# Chat Summary Bot 依赖安装脚本 (WSL2/Ubuntu)

set -e

echo "🚀 开始安装 Chat Summary Bot 依赖..."

# 检查是否为 root
if [ "$EUID" -eq 0 ]; then 
   echo "❌ 请不要使用 root 用户运行此脚本"
   exit 1
fi

echo "📦 安装系统依赖..."
sudo apt-get update
sudo apt-get install -y \
    build-essential \
    cmake \
    gperf \
    libssl-dev \
    zlib1g-dev \
    libreadline-dev \
    libc++-dev \
    libc++abi-dev \
    pkg-config \
    wget \
    git

echo "🔧 检查并安装 Go..."
if ! command -v go &> /dev/null; then
    echo "   下载 Go 1.24.0..."
    wget -q https://go.dev/dl/go1.24.0.linux-amd64.tar.gz
    
    echo "   安装 Go..."
    sudo rm -rf /usr/local/go
    sudo tar -C /usr/local -xzf go1.24.0.linux-amd64.tar.gz
    
    # 添加到 PATH
    if ! grep -q '/usr/local/go/bin' ~/.bashrc; then
        echo 'export PATH=$PATH:/usr/local/go/bin' >> ~/.bashrc
    fi
    
    export PATH=$PATH:/usr/local/go/bin
    rm go1.24.0.linux-amd64.tar.gz
    echo "✅ Go 安装完成: $(go version)"
else
    echo "✅ Go 已安装: $(go version)"
fi

echo "📚 检查并安装 TDLib..."
if ! pkg-config --exists tdlib 2>/dev/null; then
    echo "   克隆 TDLib 仓库..."
    cd ~
    if [ ! -d "td" ]; then
        git clone --depth 1 https://github.com/tdlib/td.git
    fi
    
    echo "   编译 TDLib（这可能需要几分钟）..."
    cd td
    mkdir -p build
    cd build
    cmake -DCMAKE_BUILD_TYPE=Release -DCMAKE_INSTALL_PREFIX=/usr/local ..
    cmake --build . -j$(nproc)
    
    echo "   安装 TDLib..."
    sudo cmake --install .
    sudo ldconfig
    
    echo "✅ TDLib 安装完成"
else
    echo "✅ TDLib 已安装: $(pkg-config --modversion tdlib)"
fi

echo ""
echo "🎉 所有依赖安装完成！"
echo ""
echo "下一步："
echo "  1. 运行编译脚本: ./build.sh"
echo "  2. 配置 config.yaml: cp etc/config.yaml.sample etc/config.yaml"
echo "  3. 运行 Bot: ./chat-summary-bot -f etc/config.yaml"
