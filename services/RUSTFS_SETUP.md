# RustFS 配置

Storage Service 使用 RustFS 作为 S3 兼容对象存储。服务端只负责预签名、元数据和 SHA-512 校验，文件内容由客户端直接上传或下载。

## Compose 配置

RustFS 和 Storage Service 位于 `storage` profile：

```bash
cd services
./deploy_docker_compose.sh standard

# 只在基础控制面上追加文件与消息存储
./deploy_docker_compose.sh minimal --enable storage
```

默认端口和凭据：

| 配置 | Compose 默认值 | 说明 |
| --- | --- | --- |
| RustFS S3 API | `9000` | 客户端使用预签名 URL 直传 |
| RustFS Console | `9001` | 对象存储管理页面 |
| Storage HTTP | `8081` | 文件控制面；进程脱离 Compose 运行时默认 `8080` |
| Access key | `rustfsadmin` | 仅适合本地开发 |
| Secret key | `rustfsadmin` | 生产环境必须替换 |
| Bucket | `betterfly-files` | 服务启动时自动确认或创建 |

推荐在被 Git 忽略的 `services/.env` 中配置：

```env
RUSTFS_ACCESS_KEY=replace-me
RUSTFS_SECRET_KEY=replace-me
RUSTFS_REGION=cn-east-1
RUSTFS_BUCKET=betterfly-files
```

Compose 内部固定通过 `http://rustfs:9000` 访问 RustFS，不应写成 `localhost:9000`。

## 预签名外部地址

预签名 URL 的 host 必须能被客户端访问。生产环境推荐显式设置完整地址：

```env
RUSTFS_EXTERNAL_ENDPOINT_URL=https://files.example.com
```

未设置完整地址时，Storage Service 按以下方式推导：

- 协议优先使用 `RUSTFS_EXTERNAL_SCHEME`，其次使用 `X-Forwarded-Proto` 或当前请求协议。
- 主机优先使用 `RUSTFS_EXTERNAL_HOST`，其次使用当前 HTTP 请求的 host。
- 端口使用 `RUSTFS_EXTERNAL_PORT`，默认 `9000`。

例如直接通过公网 IP 联调：

```env
RUSTFS_EXTERNAL_SCHEME=http
RUSTFS_EXTERNAL_HOST=203.0.113.10
RUSTFS_EXTERNAL_PORT=9000
```

反向代理部署时应转发正确的 `Host` 和 `X-Forwarded-Proto`。不要把容器内地址 `http://rustfs:9000` 作为外部 endpoint，否则客户端无法使用返回的 URL。

## 上传与校验

1. 客户端计算完整的 128 位十六进制 SHA-512。
2. 调用 `POST /storage_service/upload` 获取预签名 PUT URL。
3. 客户端直接向 RustFS 上传原始字节。
4. 调用 `POST /storage_service/upload/verify`。
5. Storage Service 从 RustFS 读取对象并重新计算 SHA-512；匹配后才把元数据标记为已验证。

对象 key 当前为 `{hash前2位}/{完整hash}`。未验证文件不会被下载接口或内部存在性查询视为可用文件。完整请求格式见根目录的 [API 文档](../API_DOCUMENTATION.md)。

## 验证与排障

```bash
docker compose --profile storage ps
docker logs rustfs
docker logs storageService
curl http://localhost:9000/minio/health/live
curl http://localhost:8081/health
curl http://localhost:8081/ready
```

`/health` 只表示 Storage Service 进程存活；`/ready` 会检查 PostgreSQL 和 RustFS bucket。常见故障：

- URL 返回 `localhost:9000`：配置 `RUSTFS_EXTERNAL_ENDPOINT_URL` 或外部 scheme/host/port。
- 上传后一直未验证：确认客户端在 PUT 成功后调用了 `/upload/verify`，并检查对象内容是否与声明哈希一致。
- 容器无法连接 RustFS：确认内部 endpoint 为 `http://rustfs:9000`，并检查两个服务是否位于同一 Compose network。
- 客户端无法上传：除 Storage HTTP 端口外，还必须让客户端能够访问预签名 URL 对应的 RustFS S3 端口。

生产环境还应使用强凭据、TLS、持久卷备份和适当的对象存储高可用方案。
