set -e

sudo apt update
sudo apt install postgresql postgresql-contrib -y
sudo systemctl start postgresql
sudo systemctl enable postgresql      # 开机自启
sudo systemctl status postgresql      # 查看状态

# 1. 找到配置文件路径（自动识别 PostgreSQL 版本）
PG_CONF=$(find /etc/postgresql -name postgresql.conf | head -n 1)
HBA_CONF=$(find /etc/postgresql -name pg_hba.conf | head -n 1)

if [[ -z "$PG_CONF" || -z "$HBA_CONF" ]]; then
  echo "❌ PostgreSQL 配置文件未找到，请确认是否已安装 PostgreSQL。"
  exit 1
fi

echo "✅ 找到 postgresql.conf: $PG_CONF"
echo "✅ 找到 pg_hba.conf: $HBA_CONF"

# 2. 修改监听地址
echo "🎯 修改 listen_addresses 为 '*'"
sudo sed -i "s/^#\?listen_addresses\s*=.*/listen_addresses = '*'/g" "$PG_CONF"

# 3. 添加远程访问权限（如已存在则跳过）
RULE="host    all             all             0.0.0.0/0               md5"
if ! sudo grep -Fxq "$RULE" "$HBA_CONF"; then
  echo "🎯 添加 pg_hba.conf 访问控制规则"
  echo "$RULE" | sudo tee -a "$HBA_CONF" > /dev/null
else
  echo "✅ pg_hba.conf 中已存在远程访问规则"
fi

# 4. 重启 PostgreSQL 服务
echo "🔁 重启 PostgreSQL..."
sudo systemctl restart postgresql

sudo ufw allow 5432/tcp
