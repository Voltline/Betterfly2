#!/bin/bash

# 1. 定义路径
SSL_DIR="/etc/postgres_ssl"
mkdir -p "$SSL_DIR"

# 2. 创建 OpenSSL 配置文件（包含 SAN）
cat > "$SSL_DIR/openssl.cnf" <<EOF
[req]
default_bits       = 2048
distinguished_name = req_distinguished_name
x509_extensions    = v3_req
prompt             = no

[req_distinguished_name]
C  = CN
ST = Shanghai
L  = Shanghai
O  = SpecterSquad
CN = 132.232.203.215

[v3_req]
subjectAltName = @alt_names

[alt_names]
IP.1 = 132.232.203.215
EOF

# 3. 生成自签名证书（有效期10年，带 subjectAltName）
openssl req -new -x509 -days 3650 -nodes \
  -out "$SSL_DIR/server.crt" \
  -keyout "$SSL_DIR/server.key" \
  -config "$SSL_DIR/openssl.cnf"

# 4. 设置权限（PostgreSQL 强制要求 key 权限）
chmod 600 "$SSL_DIR/server.key"
chown postgres:postgres "$SSL_DIR/server.key" "$SSL_DIR/server.crt"

# 5. 配置 postgresql.conf
CONF_PATH=$(find /etc -name postgresql.conf | head -n 1)
if [[ -f "$CONF_PATH" ]]; then
  echo "ssl = on" >> "$CONF_PATH"
  echo "ssl_cert_file = '$SSL_DIR/server.crt'" >> "$CONF_PATH"
  echo "ssl_key_file = '$SSL_DIR/server.key'" >> "$CONF_PATH"
else
  echo "postgresql.conf 未找到，请手动设置 ssl 参数"
fi

# 6. 重启 PostgreSQL
echo "🔄 重启 PostgreSQL..."
systemctl restart postgresql || service postgresql restart

echo "✅ 自签名证书（含 SAN）已配置并启用 SSL"
