# 可裁剪部署

日常保留完整产品能力，只裁剪冗余副本和诊断工具；不会合并业务服务或改变客户端协议。
使用脚本选择组合，不直接对正在运行的旧 Kafka 执行 `docker compose up`。

## 日常与分布式测试

| 预设 | 长期运行容器 | 能力 |
| --- | --- | --- |
| `standard`（默认） | 13 | 单 Kafka、单 DF，文件、推送、一对一和群通话、实验、好友动态全部保留 |
| `distributed` | 14 | standard 加第二个 DF，测试跨 Pod 投递、连接迁移及恢复 |
| `full` | 17 | distributed 加 Prometheus、Grafana 和 Kafka UI；Kafka 仍为单节点 |
| `minimal` | 5 | Redis、Kafka、DF、Auth、Friend；仅认证、连接和关系控制面 |

计数不包含执行后退出的 `db_migrate`、`kafka-init`，也不包含现有外部 PostgreSQL。
`minimal` 没有 Storage，因此不支持完整的消息发送、持久化和同步；不是全功能低配模式。
完整业务 Go 服务各一份，RustFS、Redis、Coturn、LiveKit 也各一份。

```bash
cd services

# 日常全功能：先配置 PGSQL/APNs/RustFS 和 LIVEKIT_*，沿用现有 .env
./build_docker_compose.sh --no-sudo

# 只打印计划，不执行任何 Docker 操作
./build_docker_compose.sh --no-sudo --dry-run

# 双 DF 共用一个 Kafka，不需要为了跨 Pod 通信再跑一个 broker
./build_docker_compose.sh distributed --no-sudo

# 临时采集监控或查看 Kafka
./deploy_docker_compose.sh standard --enable observability --no-sudo
./deploy_docker_compose.sh standard --enable tools --no-sudo

# 全部诊断工具和第二个 DF
./build_docker_compose.sh full --no-sudo

# 不测群通话时进一步省掉 LiveKit，保留一对一和 Coturn
./deploy_docker_compose.sh standard --without-group-calls --no-sudo
```

`standard/distributed/full` 默认开启群通话，需要现有有效 `LIVEKIT_*` 配置，媒体公网地址
与开放端口见 [群通话部署](../docs/GROUP_CALLS.md)。不启用 `group-calls` 时，脚本会清空
传给 Compose 的四个 LiveKit 地址/密钥，避免 CallService 宣告未启动的媒体服务可用。
显式 `--enable group-calls` 优先于 `--without-group-calls`。

## 资源与构建

- Kafka 固定为已验证的 `apache/kafka:4.2.0`，单 broker/controller，应用分区仍为三份。
- JVM 堆为 128–512 MiB，broker 容器内存上限 1 GiB，为非堆和运行开销留空间。
  这是开发/测试预算，不是吞吐承诺；更高负载需要评估并调整 Compose。
- 每个容器使用 Docker local 日志驱动，最多三份 10 MiB 文件；应用日志默认 `info`，
  可在 `.env` 或命令环境中显式 `LOG_LEVEL=debug`，不删除错误日志。
- 构建/Compose 并发默认 `COMPOSE_PARALLEL_LIMIT=2`，可覆盖为 1，保留已有 BuildKit 缓存。
- 日常不自动生成 Protobuf、不强制重建未变更的容器；协议变化追加 `--proto`，
  真正需要重建容器时追加 `--force-recreate`，证书缺失时 build 脚本仍自动生成。
- 不缩减 DLQ 七天保留期、Inbox/Outbox/Push 账本窗口，不修改业务 worker 并发、缓存、
  WebSocket ownership 和鉴权。图片解码和真实音视频的峰值要单独测，不能按空闲内存设硬限制。

默认不启动 Prometheus，也默认 `METRICS_ENABLED=false`；追加 observability 自动启用，
显式环境值仍优先。脚本使用自己的 profile 列表，忽略遗留 `COMPOSE_PROFILES`，避免意外
把全量环境拉起。不会修改 `.env`、Docker Desktop 资源额度或 Kubernetes 部署。

## 增量重建

```bash
./rebuild_docker_compose.sh --no-sudo dataforwarding  # 默认只处理 df
./rebuild_docker_compose.sh --no-sudo --preset distributed dataforwarding
./rebuild_docker_compose.sh --no-sudo df-all          # 明确处理两个 DF
./rebuild_docker_compose.sh --no-sudo --proto storage friend
./rebuild_docker_compose.sh --no-sudo --all           # standard，不再隐式启动 full
./rebuild_docker_compose.sh --no-sudo --preset full --all
```

单服务重建默认 `--no-deps`，不动 Kafka/数据库，不强制重建未变化容器。
`--with-deps`、`--remove-orphans`、`--all` 或直接操作 broker 时，会先执行 Kafka 切换检查。
`--all` 不与具体服务目标混用；使用 `--preset` / `--enable` 选择部署组合。

## Kafka 集群测试与旧环境保护

`--kafka-cluster` 显式叠加 `docker-compose.kafka-cluster.yml`，增加第二个 broker，
内部 Topic 和 DLQ 副本为二；日常模式内部 Topic 和 DLQ 副本为一、min ISR 为一。
业务 Kafka 请求、ACK、重试、DLQ、稳定事件 ID 和消费者语义没有变化。
单节点允许功能与跨 Pod 测试，但不能验证 broker 容灾。两个 controller 需要两台均存活，
同样不是可容忍单节点故障的 HA 集群；本轮不新增三节点生产部署。

```bash
# 只用于已准备好独立数据环境的集群测试
./deploy_docker_compose.sh distributed --kafka-cluster --no-sudo
```

覆盖文件要求 Compose >= 2.24.4。单节点使用 `kafka-single-data`，多节点使用独立
`kafka-cluster-1-data` / `kafka-cluster-2-data`；不共享 KRaft 元数据。所有新日志目录
显式设为 `/var/lib/kafka/data/betterfly`，位于命名卷内，容器重建不重置 offset。

**模式切换不是数据迁移。** 同一 Compose 项目发现旧版无 topology 标签的 broker，
或目标拓扑与当前 broker 不符时，脚本在重建/删除前退出；没有强制跳过检查的参数。
旧集群数据、offset 和 DLQ 不会被自动复制到新的命名卷，停止的旧 broker 也受保护。

本次选择保留旧 Kafka，首次切换按下面的人工维护流程处理，不自动执行：

1. 暂停入口和生产者，确认消费 lag、未完成 Inbox/Outbox/Push 任务、DLQ 重放需求。
2. 确认真实 Kafka `log.dir/log.dirs` 与 metadata 目录，备份数据、集群信息及 offset；
   旧配置没有显式命名 Kafka 数据卷，不能只备份 RustFS 或猜测镜像默认路径。
3. 保留旧集群用于排空或受控重放；新 Kafka 使用新测试数据环境。需要延续旧业务状态时，
   另行完成消息/offset/稳定操作 ID 的迁移，不能仅把双 controller 配置改成一个。
4. 确认数据处理完成后，再移除旧 broker 容器以允许新拓扑启动；不删除数据库和 RustFS。
   不执行 `down -v`，不复制双节点 KRaft 元数据到单节点卷，也不直接清空幂等账本。

一旦使用本轮标记的同拓扑环境，可直接在 standard/distributed/full 之间切换，Kafka
卷不变。缩小组合默认移除未选中的诊断/DF2 容器但保留持久卷；`--keep-orphans` 可保留
容器（保留的容器仍会占用资源）。不要绕过脚本的切换保护手动执行全量 `up`。

## 可选能力

| Profile | 服务 |
| --- | --- |
| `storage` | RustFS、StorageService |
| `notifications` | PushService |
| `calls` | CallService、PushService、Coturn |
| `group-calls` | LiveKit、CallService、PushService |
| `experiments` | ABTestService |
| `moments` | MomentsService、RustFS |
| `observability` | Prometheus、Grafana |
| `tools` | Kafka UI |
| `redundancy` | 第二个 DF |

验证命令与隔离 Kafka 持久化测试见 [回归测试文档](../docs/REGRESSION_TESTING.md)。
