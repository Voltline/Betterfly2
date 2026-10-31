# AIChatService

托管 Betterfly AI 助手账号：消费机器人平台转发的 `aichat-service` 触发，读取会话上下文和最近的图片，调用 OpenAI 兼容的 `/chat/completions` 接口，再以机器人身份经 `storage-service` 写回回复。设计、配置和客户端接入见 [AI 助手](../../docs/AICHAT.md)。

## 结构

| 路径 | 作用 |
| --- | --- |
| `aichatMain.go` | 读取配置，确保机器人账号并登记为平台内置机器人，启动 Kafka 消费与内网 HTTP |
| `internal/config` | 环境变量与校验 |
| `internal/bot` | 触发校验、限流、上下文与图片、回复拆分与入库请求 |
| `internal/llm` | OpenAI 兼容客户端，支持图片内容，带重试与 `Retry-After` |
| `internal/media` | 从 RustFS 读取图片（含 HEIC），按 EXIF 方向转正，缩放转码为 `data:` URL |
| `internal/consumer` | Kafka 消费，解析失败进死信 |
| `internal/publisher` | 按会话 key 发布到 Kafka |

## 本地运行测试

```bash
cd services/aichatService
go test ./...
# 账号、上下文与图片路径查询的 PostgreSQL 测试
BETTERFLY_TEST_POSTGRES_DSN="host=127.0.0.1 port=5432 user=postgres password=... dbname=postgres sslmode=disable" go test ./internal/bot
```

HTTP `8089`：`/healthz` 存活、`/ready` 检查数据库、`/metrics` Prometheus 指标，只在内网暴露。
