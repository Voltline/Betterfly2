# RustFS 配置说明

本文档说明如何在 Betterfly2 项目中使用 RustFS 作为对象存储服务。

## Docker Compose 配置

RustFS 服务已添加到 `docker-compose.yml` 中，配置如下：

### RustFS 服务配置

- **镜像**: `rustfs/rustfs:latest`
- **容器名**: `rustfs`
- **端口映射**:
  - `9000:9000` - S3 API 端点
  - `9001:9001` - 控制台端口
- **数据卷**: `rustfs-data` (持久化存储)
- **默认访问密钥**: 
  - Access Key: `rustfsadmin`
  - Secret Key: `rustfsadmin`

### 环境变量配置

可以通过 `.env` 文件或环境变量自定义以下配置：

```bash
# RustFS 访问密钥（生产环境请修改为强密码）
RUSTFS_ACCESS_KEY=rustfsadmin
RUSTFS_SECRET_KEY=rustfsadmin

# RustFS 区域（可选，默认: cn-east-1）
RUSTFS_REGION=cn-east-1

# RustFS 存储桶名称（可选，默认: betterfly-files）
RUSTFS_BUCKET=betterfly-files

# 存储服务 HTTP 端口（可选，默认: 8080）
HTTP_PORT=8080
```

### 存储服务环境变量

存储服务会自动从环境变量读取 RustFS 配置：

- `RUSTFS_REGION`: RustFS 区域
- `RUSTFS_ACCESS_KEY_ID`: 访问密钥ID（与 RUSTFS_ACCESS_KEY 相同）
- `RUSTFS_SECRET_ACCESS_KEY`: 秘密访问密钥（与 RUSTFS_SECRET_KEY 相同）
- `RUSTFS_ENDPOINT_URL`: RustFS 端点URL（容器内使用 `http://localhost:9000`）
- `RUSTFS_BUCKET`: 存储桶名称

## 启动服务

### 1. 启动所有服务

```bash
cd services
docker compose up -d
```

### 2. 仅启动 RustFS

```bash
cd services
docker compose up -d rustfs
```

### 3. 查看 RustFS 日志

```bash
docker logs rustfs
```

### 4. 访问 RustFS 控制台

启动后，可以通过以下地址访问 RustFS 控制台：

- **本地访问**: http://localhost:9001
- **默认账号**: `rustfsadmin` / `rustfsadmin`

## 验证部署

### 1. 检查 RustFS 健康状态

```bash
# 检查容器状态
docker ps | grep rustfs

# 检查健康检查状态
docker inspect rustfs | grep -A 10 Health
```

### 2. 测试 S3 API

使用 `mc` (MinIO Client) 测试：

```bash
# 安装 mc（如果未安装）
# macOS: brew install minio/stable/mc
# Linux: wget https://dl.min.io/client/mc/release/linux-amd64/mc

# 配置 RustFS 别名
mc alias set rustfs http://localhost:9000 rustfsadmin rustfsadmin

# 创建存储桶
mc mb rustfs/betterfly-files

# 列出存储桶
mc ls rustfs

# 测试上传
echo "test" > test.txt
mc cp test.txt rustfs/betterfly-files/

# 测试下载
mc cp rustfs/betterfly-files/test.txt downloaded.txt
```

### 3. 使用 curl 测试

```bash
# 检查健康状态
curl http://localhost:9000/health

# 列出存储桶（需要签名，这里仅作示例）
curl -X GET http://localhost:9000/
```

## 生产环境建议

### 1. 修改默认密钥

**重要**: 生产环境必须修改默认访问密钥！

在 `.env` 文件中设置：

```bash
RUSTFS_ACCESS_KEY=your_strong_access_key_here
RUSTFS_SECRET_KEY=your_strong_secret_key_here
```

### 2. 启用 TLS/HTTPS

参考 [RustFS TLS 配置文档](https://docs.rustfs.com.cn/integration/tls/) 配置 HTTPS。

### 3. 数据备份

定期备份 `rustfs-data` 数据卷：

```bash
# 备份数据卷
docker run --rm -v rustfs-data:/data -v $(pwd):/backup ubuntu tar czf /backup/rustfs-backup-$(date +%Y%m%d).tar.gz /data

# 恢复数据卷
docker run --rm -v rustfs-data:/data -v $(pwd):/backup ubuntu tar xzf /backup/rustfs-backup-YYYYMMDD.tar.gz -C /
```

### 4. 监控和日志

- 使用 Prometheus 和 Grafana 监控 RustFS 性能
- 配置日志轮转策略
- 设置告警规则

### 5. 多节点部署

对于生产环境，建议使用多节点多盘（MNMD）部署模式，参考 [RustFS 多机多盘安装文档](https://docs.rustfs.com.cn/installation/multi-node-multi-disk/)。

## 故障排查

### 问题1: 容器无法启动

```bash
# 查看容器日志
docker logs rustfs

# 检查端口占用
lsof -i :9000
lsof -i :9001
```

### 问题2: 存储服务无法连接 RustFS

1. 检查 RustFS 容器是否运行：
   ```bash
   docker ps | grep rustfs
   ```

2. 检查网络连接：
   ```bash
   docker exec storageService ping rustfs
   ```

3. 检查环境变量：
   ```bash
   docker exec storageService env | grep RUSTFS
   ```

### 问题3: 文件上传失败

1. 检查存储桶是否存在（会自动创建）
2. 检查访问密钥是否正确
3. 查看存储服务日志：
   ```bash
   docker logs storageService
   ```

## 参考文档

- [RustFS 官方文档](https://docs.rustfs.com.cn/)
- [RustFS Docker 安装指南](https://docs.rustfs.com.cn/installation/docker/)
- [RustFS Golang SDK](https://docs.rustfs.com.cn/developer/sdk/go.html)

