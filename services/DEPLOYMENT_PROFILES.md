# 可裁剪部署

Betterfly2 使用 Docker Compose Profiles 将基础聊天链路与可选能力分离。推荐通过
`deploy_docker_compose.sh` 部署，避免手工维护服务列表。

## 部署预设

| 预设 | 包含能力 | 不包含能力 |
| --- | --- | --- |
| `minimal` | Redis、双 Kafka、单 DataForwarding、Auth、Friend | 消息持久化与完整发送链路、文件、APNs、通话、AB 实验、监控、Kafka UI、第二个 DataForwarding |
| `standard` | 核心链路及文件、APNs、通话、AB 实验 | Prometheus、Grafana、Kafka UI、第二个 DataForwarding |
| `full` | 原有全部服务 | LiveKit（需显式启用 group-calls） |

`minimal` 适合验证认证、WebSocket 和好友/群组控制面。当前消息发送依赖 Storage Service，因而 `minimal` 不是完整聊天部署；需要收发和同步消息时应使用 `standard`，或在 `minimal` 上追加 `storage`。

Kafka 当前采用两个 KRaft broker 组成 controller quorum，因此即使在 `minimal` 中也不能只删除
`kafka2`。如果后续需要进一步压缩内存，应单独提供单 broker Kafka 配置，而不是直接停掉一个 broker。

```bash
cd services

# 低配机器
./deploy_docker_compose.sh minimal

# 日常完整业务功能，不运行监控和开发工具
./deploy_docker_compose.sh standard

# 全量测试部署，包含 LiveKit 群通话（先配置 .env，见 ../docs/GROUP_CALLS.md）
./build_docker_compose.sh
```

全量脚本仅在证书缺失时生成证书，并默认复用未变化的容器。证书地址发生变化时可显式追加
`--cert`；排查容器生命周期问题时才建议追加 `--force-recreate`。
该脚本默认追加 `group-calls`；`deploy_docker_compose.sh standard/full` 仍保持可裁剪，
不会自动追加群通话。只部署一对一通话时使用后者，且保持四个 `LIVEKIT_*` 业务配置为空。

## 按能力组合

可在预设上追加任意 Profile：

```bash
# 核心聊天 + 文件服务
./deploy_docker_compose.sh minimal --enable storage

# 核心聊天 + APNs 推送
./deploy_docker_compose.sh minimal --enable notifications

# 标准部署临时启用监控
./deploy_docker_compose.sh standard --enable observability
```

| Profile | 服务 |
| --- | --- |
| `storage` | RustFS、StorageService |
| `notifications` | PushService |
| `calls` | CallService、PushService、Coturn |
| `group-calls` | LiveKit SFU、CallService、PushService（需要配置 LIVEKIT_*，见 ../docs/GROUP_CALLS.md） |
| `experiments` | ABTestService |
| `observability` | Prometheus、Grafana |
| `tools` | Kafka UI |
| `redundancy` | 第二个 DataForwarding Pod |

`minimal` 自动设置 `METRICS_ENABLED=false` 和默认 `LOG_LEVEL=info`；追加 `observability` 时会自动重新
启用指标端点。也可以直接通过环境变量覆盖：

```bash
METRICS_ENABLED=false LOG_LEVEL=warn ./deploy_docker_compose.sh standard
```

切换到更小的预设时，脚本默认使用 `--remove-orphans` 删除不再属于当前组合的容器，但不会删除
RustFS 或 Grafana 的持久卷。使用 `--keep-orphans` 可以保留这些容器。
