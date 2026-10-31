<div align="center">
  <img src="others/betterfly-logo.jpg" alt="Betterfly2 Logo">
</div>

# Betterfly2

Betterfly2 是 [Betterfly](https://github.com/Voltline/Betterfly) 的 Go 微服务重构版本，提供即时消息、好友与群组、消息同步、文件传输、客户端实验、WebRTC 通话和 iOS APNs 推送能力。

![License](https://img.shields.io/github/license/Voltline/Betterfly2)
![Issues](https://img.shields.io/github/issues/Voltline/Betterfly2)
![Stars](https://img.shields.io/github/stars/Voltline/Betterfly2)

## 当前能力

- WebSocket + Protobuf 客户端长连接，Redis 保存跨 DataForwarding Pod 的路由状态。
- Kafka 解耦消息存储、好友与群组、通话信令及推送任务。
- [广播频道](docs/CHANNELS.md)：公开发现/订阅、私有邀请、管理员发布与完整历史分页。
- [会话功能](docs/CONVERSATION_FEATURES.md)：频道置顶、群聊／频道免打扰及引用回复。
- [频道讨论区](docs/CHANNEL_DISCUSSIONS.md)：关联普通群、按公告评论、入群后完整线程历史。
- [好友动态](docs/MOMENTS.md)：独立网页与 HTTP API、九图、点赞评论及好友可见性。
- [机器人平台](docs/CHATBOT.md)：第三方机器人通过 Bot API 长轮询或 webhook 收消息，收发文字、图片与文件，支持 @、命令与入群事件，管理页面创建机器人。
- [AI 助手](docs/AICHAT.md)：接入 OpenAI 兼容模型的内置机器人，支持单聊和群里 @，能看懂图片，回复照常入库与多端同步。
- PostgreSQL 持久化用户、关系、消息、文件元数据、实验和推送设备。
- Ristretto L1 + Redis L2 + PostgreSQL 的消息与资料查询缓存。
- RustFS 预签名直传、服务端 SHA-512 校验和鉴权下载。
- 一对一 WebRTC 语音/视频通话，支持 Coturn 中继与 PushKit 离线唤醒。
- APNs sandbox/production 双环境、普通消息通知和受保护的内网调试后台。
- 基于设备或其他主体稳定分流的 AB 实验，以及实验推全、撤回和例外分组。
- Docker Compose Profiles 可裁剪部署，以及面向单集群验证的 Kubernetes 清单。

## 架构与数据流

```mermaid
flowchart LR
    Client["iOS client / 浏览器"] -->|"HTTPS / WSS :54342"| GW["Gateway (Traefik)"]
    GW -->|"/ws + Protobuf，负载均衡"| DF["DataForwarding Service"]
    GW -->|"/storage_service/"| Storage["Storage Service"]
    GW -->|"/abtest/"| AB["ABTest Service"]
    GW -->|"/moments"| Moments["Moments Service"]
    GW -->|"/bot/v1/"| Chatbot
    GW -->|"预签名 URL"| RustFS
    Moments --> Auth
    Moments --> PG
    Moments --> RustFS
    DF -->|"gRPC"| Auth["Auth Service"]
    DF <--> Redis[(Redis)]
    DF <--> Kafka[(Kafka)]
    Kafka <--> Storage
    Kafka <--> Friend["Friend Service"]
    Kafka <--> Call["Call Service"]
    Kafka <--> Push["Push Service"]
    Kafka <--> Chatbot["Chatbot Service"]
    Chatbot --> PG
    Chatbot <-->|"Bot API / webhook"| Bots["第三方机器人"]
    Kafka <--> AIChat["AIChat Service"]
    AIChat --> PG
    AIChat --> RustFS
    AIChat --> LLM["OpenAI-compatible API"]
    Storage <--> Redis
    Storage <--> PG[(PostgreSQL)]
    Storage <--> RustFS[(RustFS)]
    Friend <--> PG
    AB <--> PG
    Push <--> PG
    Push --> APNs["Apple APNs"]
    Call <--> Redis
    Call -->|"group membership"| PG
    Call -->|"room control"| SFU["LiveKit SFU (optional)"]
    Client <-->|"group WebRTC media"| SFU
    Call --> Push
    Client <-->|"WebRTC media"| Peer["Peer client"]
    Client <-->|"TURN relay"| Coturn["Coturn"]
```

所有对外 HTTP、WebSocket 和后台页面都经[统一网关](docs/GATEWAY.md)的 `54342` 端口进入，按路径分发到各服务，根路径是导航页；后台路由只对内网来源开放。客户端业务请求经网关进入 DataForwarding Service。认证走 Auth gRPC；异步业务封装为 Protobuf Envelope 后进入固定 Kafka topic，由任一对应 Service Pod 消费，再根据请求中的 DataForwarding Pod topic 返回响应。客户端之间的实时消息最终仍由目标用户在 Redis 中登记的 Pod 路由送达。

文件上传和下载是例外：Storage Service 只签发 URL、维护元数据并校验哈希，文件内容由客户端直接与 RustFS 传输。WebRTC 媒体也不经过 Call Service，Call Service 只维护信令和通话状态。

## 服务清单

| 服务 | 客户端或内部入口 | 作用 |
| --- | --- | --- |
| Gateway | `https://host:54342` | 统一入口（Traefik），按路径分发、`/ws` 负载均衡、后台来源限制；根路径为导航页 |
| DataForwarding Service | `wss://host:54342/ws`（经网关） | WebSocket 会话、协议分发和跨 Pod 路由；`distributed` profile 增加 df2，自动加入负载均衡 |
| Auth Service | 内网 gRPC `50051` | 注册、登录、JWT 校验和登出失效 |
| Storage Service | Kafka `storage-service`；`/storage_service/`（旧端口 `8081`） | 消息与资料持久化、同步查询、文件控制面 |
| Friend Service | Kafka `friend-service`；HTTP `54401` | 好友关系、群组和群成员维护；HTTP 仅提供内网探针 |
| ABTest Service | `/abtest/`（旧端口 `8082`） | 实验配置获取、服务端求值和管理后台 |
| Moments Service | `/moments`（旧端口 `8087`） | 好友动态网页和原生共用 API，私有图片、点赞、评论 |
| Call Service | Kafka `call-service`；HTTP `8085` | 一对一及可选群会议控制面、探针与签名 SFU 回调 |
| Push Service | Kafka `push-service`；HTTP `8086` | PushKit/普通 APNs 推送；HTTP 提供探针和受保护后台 |
| Chatbot Service | Kafka `chatbot-service`；`/bot/v1/`、`/chatbot/admin/`（旧端口 `8088`） | 机器人平台：分发消息与关系事件，Bot API、webhook 与管理页面 |
| AIChat Service | Kafka `aichat-service`；内网 HTTP `8089` | AI 助手账号，读取文字与图片调用模型，以机器人身份回复 |

对外端口只有网关 `54342`，以及现有客户端仍在直连的旧端口 `8081`、`8082`、`8087`、`8088`、RustFS S3 `9000`，和不经网关的 LiveKit `7880-7882`、Coturn `3478/tcp+udp` 与 `49160-49200/udp`。Redis、Kafka、Auth、Friend、Call、Push、RustFS Console 等内部端口只绑定本机（`INTERNAL_BIND_ADDR`）。Kafka UI、Prometheus 和 Grafana 只在相应可选 profile 中启动，经网关的 `/kafka-ui`、`/prometheus`、`/grafana` 访问。PostgreSQL 当前由 `PGSQL_DSN` 指向外部实例，Compose 不负责创建数据库。

## 快速开始

### 前置条件

- Go 1.24 toolchain。个别基础模块声明了更低版本；AIChat Service 的 HEIC 解码库需要 Go 1.25，本地 Go 1.24 会按 `GOTOOLCHAIN=auto` 自动下载，镜像使用 `golang:1.25`。
- Docker 与 Docker Compose v2。
- 可用的 PostgreSQL 数据库。
- 仅在修改 Protobuf 时需要 `protoc`、`protoc-gen-go` 和 `protoc-gen-go-grpc`。

### 启动标准环境

```bash
git clone https://github.com/Voltline/Betterfly2.git
cd Betterfly2/services

export PGSQL_DSN="host=your_host user=your_user password=your_password dbname=betterfly port=5432 sslmode=disable"
export RUSTFS_ACCESS_KEY="your_access_key"
export RUSTFS_SECRET_KEY="your_secret_key"

./deploy_docker_compose.sh standard --cert
docker compose ps
```

`standard` 使用单 Kafka、单 DataForwarding，启动当前全部业务能力（包含 LiveKit 群通话），但不启动 Prometheus、Grafana、Kafka UI 和第二个 DataForwarding。`distributed` 仅追加第二个 DF；`full` 追加诊断工具。首次部署或证书地址发生变化时使用 `--cert`；修改协议后追加 `--proto`。现有双 Kafka 环境会被切换保护拦截，先按[可裁剪部署](services/DEPLOYMENT_PROFILES.md)处理数据，不能直接停掉 kafka2。

低资源环境可以使用 `minimal`，但它只包含认证、连接以及好友/群组控制面，不包含 Storage Service，因此不是完整的消息发送环境。完整 profile 语义见[可裁剪部署](services/DEPLOYMENT_PROFILES.md)。

### 增量重建

```bash
cd services
./rebuild_docker_compose.sh df
./rebuild_docker_compose.sh --proto storage friend
./rebuild_docker_compose.sh --list
```

`build_docker_compose.sh` 默认日常全功能 `standard`，需要先配置 `services/.env` 中的 LiveKit 地址和密钥（见[群通话部署](docs/GROUP_CALLS.md)）。它复用已有证书、Go 模块缓存和未变化的容器，不默认生成 Protobuf，构建并发默认两路。`./build_docker_compose.sh distributed` 开启双 DF；`full` 开启诊断工具。增量重建的 `dataforwarding` 默认只处理 DF，`df-all` 才明确处理两个实例；`--all` 也不再隐式启用 full。

应用镜像使用 BuildKit 共享 Go 模块与编译缓存。首次构建仍需下载和编译依赖；不要在日常开发中执行 `docker builder prune`，否则下一次会重新承担这部分冷启动成本。

### 生成协议与运行测试

```bash
make -C proto

cd services/dataForwardingService
go test ./...
```

仓库由多个 Go module 组成，没有可在根目录覆盖所有服务的单一 `go test ./...`。请在改动涉及的服务、`shared` 或具体 `proto` module 内分别运行测试；跨服务回归入口见[回归测试文档](docs/REGRESSION_TESTING.md)。

## 项目结构

```text
Betterfly2/
├── common/                    # Kafka/Redis 辅助配置与 WebSocket 证书脚本
├── deploy/k8s/                # 单集群验证用 Kubernetes manifests
├── proto/                     # DataForwarding、Storage、Friend、Call、Push 协议
├── services/                  # 十个业务服务、网关（gateway/）、Compose 与部署脚本
├── shared/                    # 数据库、日志、指标、MQ 和通用分发器
├── tool/                      # Protobuf 编译工具
└── docs/                     # 接口、功能契约、开发与回归测试文档
```

## 文档导航

| 文档 | 内容 |
| --- | --- |
| [统一网关](docs/GATEWAY.md) | 路由表、端口、后台来源限制、预签名 URL 与证书 |
| [API 文档](docs/API_DOCUMENTATION.md) | Storage、ABTest、Call、Push 的对外与内部协议 |
| [接口新增指南](docs/INTERFACE_DEVELOPMENT.md) | DataForwarding、Storage、Friend 模块自注册流程 |
| [回归测试](docs/REGRESSION_TESTING.md) | Docker Compose 跨 Pod 端到端测试 |
| [可裁剪部署](services/DEPLOYMENT_PROFILES.md) | 日常单节点、双 DF、诊断模式及 Kafka 切换保护 |
| [RustFS 配置](services/RUSTFS_SETUP.md) | 对象存储、外部预签名地址与故障排查 |
| [Call Service](services/callService/README.md) | WebRTC、Coturn 和 PushKit 唤醒 |
| [群通话](docs/GROUP_CALLS.md) | LiveKit 多人语音/视频、协议和可选部署 |
| [Push Service](services/pushService/README.md) | APNs 双环境、普通通知与内网调试后台 |
| [机器人平台](docs/CHATBOT.md) | 分发规则、Bot API、webhook、管理与配置 |
| [AI 助手](docs/AICHAT.md) | AIChatService 回复流程、识图、模型配置与客户端接入 |
| [Monitor 账号](services/dataForwardingService/MONITOR.md) | 仅用户 ID 1 可见的服务状态与管理指令 |
| [Kubernetes](deploy/k8s/README.md) | 当前单集群验证清单的范围与限制 |
| [数据库与可靠性运维](deploy/DATABASE_AND_RELIABILITY.md) | 独立迁移、连接池预算、Kafka 重试与 DLQ 语义 |
| [协议生成](proto/docs/README.md) | Protobuf 依赖、生成命令和目录说明 |

## 项目成员

- [Voltline](https://github.com/Voltline)
- [D_S_O_](https://github.com/DissipativeStructureObject)

## License

Betterfly2 使用 [MIT License](LICENSE)。
