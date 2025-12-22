#!/usr/bin/env bash
set -euo pipefail

# 定义产物存放目录
OUTPUT_DIR="output"
mkdir -p "$OUTPUT_DIR"

# 默认输出文件名
BINARY_NAME="chase-code"
OUTPUT_PATH="$OUTPUT_DIR/$BINARY_NAME"

# 获取第一个参数作为模式，默认为 "build" (正常编译)
MODE="${1:-build}"

if [ "$MODE" = "debug" ]; then
    echo ">>> Mode: DEBUG (Build with gcflags and start dlv)"
    
    # 检查并安装 dlv
    if ! command -v dlv >/dev/null 2>&1; then
        echo "dlv not found, installing..."
        go install github.com/go-delve/delve/cmd/dlv@latest
    fi

    # 编译带调试信息的版本
    echo "Building..."
    go build -gcflags "all=-N -l" -o "$OUTPUT_PATH" .

    # 启动调试器
    echo "Starting dlv debugger on :2346..."
    dlv --listen=:2346 --headless=true --api-version=2 --accept-multiclient exec "$OUTPUT_PATH"

else
    echo ">>> Mode: RELEASE (Normal build)"
    
    # 正常编译
    echo "Building..."
    go build -o "$OUTPUT_PATH" .
    
    echo "Build success!"
    echo "Binary location: $OUTPUT_PATH"
fi
