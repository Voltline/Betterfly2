# ChatbotService

机器人平台：消费 DataForwarding 发来的 `chatbot-service` 触发与关系事件，把消息分发给各个机器人。内置机器人（AI 助手）转发到它自己的 Kafka topic；第三方机器人通过 HTTP Bot API 长轮询或 webhook 接收更新，再经 `storage-service` 以机器人身份发消息、收发文件。设计、接口和配置见 [机器人平台](../../docs/CHATBOT.md)。

## 结构

| 路径 | 作用 |
| --- | --- |
| `chatbotMain.go` | 读取配置，启动 Kafka 消费、webhook 投递、过期更新清理与 HTTP |
| `internal/config` | 环境变量与校验 |
| `internal/platform/router.go` | 触发分发（@、引用、命令）与关系事件（同意申请、事件更新） |
| `internal/platform/api.go` | Bot API：资料、更新、发消息、上传下载、webhook |
| `internal/platform/webhook.go` | 按机器人加租约顺序投递 webhook，签名与内网地址拦截 |
| `internal/platform/admin.go` | 管理接口与内嵌管理页面 `admin.html` |
| `internal/platform/store.go` | `bots`、`bot_updates` 与消息、权限、文件查询 |
| `internal/consumer` | Kafka 消费，解析失败进死信 |
| `internal/publisher` | 按 key 发布到 Kafka |

## 本地运行测试

```bash
cd services/chatbotService
go test ./...
# 建号、分发、发消息、文件、webhook 与管理接口的 PostgreSQL 测试
BETTERFLY_TEST_POSTGRES_DSN="host=127.0.0.1 port=5432 user=postgres password=... dbname=postgres sslmode=disable" go test ./internal/platform
```

HTTP `8088`（Compose 中经网关访问）：`/bot/v1/*` Bot API，`/admin/` 与 `/chatbot/admin/` 管理页面（需要 `CHATBOT_ADMIN_TOKEN`），`/healthz`、`/ready`、`/metrics`。
