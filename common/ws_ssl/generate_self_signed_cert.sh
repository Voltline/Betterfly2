#!/bin/bash

# 获取脚本所在目录
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# 项目根目录
ROOT_DIR="$SCRIPT_DIR/../.."

# 目标目录：services/dataForwardingService/certs
TARGET_DIR="$ROOT_DIR/services/dataForwardingService/certs"

# 创建目录（如果不存在）
mkdir -p "$TARGET_DIR"

# 生成自签名证书和私钥
openssl req -x509 -newkey rsa:4096 \
  -keyout "$TARGET_DIR/key.pem" \
  -out "$TARGET_DIR/cert.pem" \
  -days 365 -nodes \
  -subj "/CN=localhost"

# 输出结果
echo "✅ 自签名 SSL 证书已生成："
echo "   - 证书路径: $TARGET_DIR/cert.pem"
echo "   - 私钥路径: $TARGET_DIR/key.pem"
echo ""
echo "📌 请在你的 Go 服务中使用以下路径配置（相对于 dataForwardingService）："
echo "   certFile := \"./certs/cert.pem\""
echo "   keyFile  := \"./certs/key.pem\""