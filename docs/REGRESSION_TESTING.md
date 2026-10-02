# Betterfly2 回归测试

本文档记录当前可直接执行的回归测试入口，优先面向本地 `docker-compose` 联调环境。

## 消息 ID、撤回增量同步与投递重试

本轮回归测试分布在 `../shared/db`、Storage 的 `internal/handler`、DataForwarding 的 `internal/handlers` 和 `internal/consumer`，以及 `proto/data_forwarding`：

- 原消息早于普通同步游标，但其撤回仍能独立分页补收；空普通页不会吞掉撤回页。
- 撤回墓碑遮蔽正文和文件名，沿用当前成员及入群时间权限；游标/身份错误在查询前拒绝，数据库错误不得返回部分成功。
- 自己发送的单聊也参与同步；复合分页、默认上限和空页游标保持不变。
- 入库响应、实时 Post 和 ACK 使用同一服务端 ID/时间；重复 client ID 返回原数据库内容和时间。
- ACK/cache/实时投递的基础设施故障进入现有 Kafka 重试/DLQ 路径，DLQ 失败不跨过当前 offset；已存普通消息的明确离线等待同步，见文末收口回归。
- 副作用短期占位崩溃恢复、旧 owner 的完成/删除 fencing，以及旧 Redis ACK 格式兼容。
- 旧 Post/同步请求字节兼容，以及数据库 v4 拒绝、v5/更高版本接受。

各模块执行 `go test ./...`；关键模块执行 `go test -race ./...`。这些测试使用 sqlmock、miniredis 和 Kafka mock，不等同于真实 Docker/APNs 端到端验证。客户端配合测试和部署顺序见 [适配说明](CLIENT_MESSAGE_SYNC_ADAPTATION.md)。

2026-09-30 先前单元回归结果：全部 15 个 Go module 的 `go test ./...`、`go vet ./...` 通过；shared、DataForwarding、Storage、Push、`../proto/data_forwarding` 的 `go test -race ./...` 通过；`make -C proto` 与 `git diff --check` 通过。当时 `TestFriendServiceEndToEnd` 因未设置 `BETTERFLY_E2E=1` 跳过。随后真实 Docker 验收与容量结果见本文末尾；真实 APNs 和双设备客户端联调仍未执行。

## Friend/Group 端到端回归测试

当前好友与群聊主链路的端到端测试位于：

- [friend_service_e2e_test.go](../services/dataForwardingService/integration/friend_service_e2e_test.go)

### 覆盖范围

这条测试会真实连接 `df` / `df2` WebSocket，并串过 `auth_service`、`friend_service`、`storage_service`、Redis、Kafka 和 PostgreSQL，覆盖以下流程：

- 用户注册与登录
- 添加好友
- 查询好友列表
- 更新好友备注
- 更新好友通知开关
- 删除好友
- 创建群组
- 查询群信息
- 加入群组
- 查询群成员列表
- 更新群头像
- 跨 pod 群消息转发
- 用户退群
- 退群后再次发送群消息，确认离群成员不再收到

### 前置条件

1. 已正确配置本地 `../services/.env`，尤其是 `PGSQL_DSN`。该文件包含私密配置且被 Git 忽略，不应提交。
2. 已启动包含 Storage Service 和第二个 DataForwarding Pod 的本地环境：

```bash
cd services
./deploy_docker_compose.sh standard --enable redundancy
```

如果最近改过 `auth_service`、`df`、`df2`、`friend_service` 或共享代码，建议先重建：

```bash
./rebuild_docker_compose.sh auth df-all friend storage
```

### 运行命令

在 [services/dataForwardingService](../services/dataForwardingService) 目录执行：

```bash
env BETTERFLY_E2E=1 \
GOPROXY=https://proxy.golang.org,direct \
GOCACHE=/tmp/betterfly-go-cache-dataforwarding \
GOMODCACHE=/tmp/betterfly-go-mod-dataforwarding \
go test -v ./integration
```

### 说明

- 这组测试默认不会参与普通单测执行，只有设置 `BETTERFLY_E2E=1` 才会真正连本地服务。
- 测试会自动生成测试账号和群 ID，避免和日常联调数据冲突。
- 如果当前数据库环境较脏，导致注册链路异常返回 `ACCOUNT_EXIST`，测试会记录这一现象，并在必要时为后续登录链路预置测试用户，以保证后半段 friend/group 主流程仍能完成验证。

### 预期结果

```text
--- PASS: TestFriendServiceEndToEnd
PASS
ok  	data_forwarding_service/integration
```

具体耗时取决于镜像状态、Kafka 就绪速度和数据库网络，不在文档中固化某次运行结果。

## 可靠性收口回归（2026-09-30）

本轮无协议或 schema 变更。自动回归覆盖：已保存普通消息离线延后同步；群内离线成员不触发整批重试；跨 Pod 批次离线后继续处理在线目标；Redis/Kafka 故障仍重试；APNs 请求成功后的存储响应重放不重复发布；DLQ 两轮重放保持操作身份；重放发布/元数据失败不跨过当前 offset；已完成 Inbox 不重复业务；新旧 Kafka kick 解析和 owner fencing。

在对应 Go module 下执行：

```bash
# services/dataForwardingService
go test ./...
go test -race ./internal/consumer ./internal/handlers ./internal/connection ./internal/router ./tools/dlq-replay
go vet ./...
# shared
go test ./...
go test -race ./kafkaconsumer ./db
go vet ./...
```

这些测试使用 miniredis、Kafka producer mock、SQL mock 和本地 WebSocket，不代表真实 Docker/Kafka/PostgreSQL/APNs 端到端环境已经通过。手工回归：发送者在线，群内一人在线、一人离线，发送一次消息；确认在线成员显示一次、离线成员重新上线同步取得同一 `message_id`，且正常离线不新增 DLQ 记录。真实故障后的至少一次重试仍允许重复网络投递。

## 首次真实验收与容量测量（2026-09-30，优化前）

### 结论与范围

基线为 `c0e5fe3`。本轮仅新增 opt-in 测试和本文记录，未修改生产代码、协议、数据库 schema 或部署配置，未提交或推送。

**不能宣称整个部署验收通过**：初次真实验收暴露了 Kafka offset 与 PostgreSQL 历史 Inbox 冲突。隔离这一环境问题后，好友/群管理、消息可靠性、单 DF 重启恢复、真实 DLQ 与重复重放测试通过。容量测量显示当前环境消息吞吐约 3 条/秒，增加并发主要增加排队延迟，不能据此估计最大在线人数。

测试环境为本机 Docker Desktop，8 CPU、约 7.75 GiB 内存预算、aarch64，使用两套 DataForwarding、两台 Kafka broker、Redis、Storage、Friend、Push、Call、Auth 和既有 PostgreSQL。测试账号/群/消息保留供核查，不清空原数据。经明确许可只执行过一次 `docker compose restart df`，未重启或重建 Kafka/数据库。

### 初次失败：历史 operation key 冲突

初次 `TestFriendServiceEndToEnd` 在退群后的群成员查询超时，耗时 30.87 秒。注册登录、好友审批、群改名/转让和跨 Pod 群消息此前已走通，退群实际也已更新数据库。

只读核查发现，本次 `friend-service/2/72` 命中了 **2026-09-06 已完成的 Inbox**；其 Outbox 已发布给旧 DF Pod、响应目标用户为 1，与本次请求用户 310 不同。消费者确认旧操作而不再执行业务/生成新响应，因此即使 consumer lag 为 0，也不能证明请求被正确处理。

确认现有 Kafka log end 低于多个分区的历史 Inbox 最大 offset。Compose 没有为 Kafka 明确配置命名数据卷；实际 `/var/lib/kafka/data` 使用镜像声明的匿名卷。**匿名卷不等于没有落盘，但不能把一次日志重建后的 offset 与仍保留的 PostgreSQL Inbox 混用。** 重建日志的具体历史操作未追溯，本轮没有改造其部署生命周期。

为继续测量，`prepareAcceptanceOffsets` 用独立测试用户的只读查询推进发生冲突的 Friend/Storage 分区，共 98 条；未删除 Inbox、Outbox 或业务数据。这只是测量隔离，不是生产修复。后续复跑已有好友/群 E2E 通过（19.85 秒，两个账号均第一次注册成功，没有走测试用户预置 fallback）。

上线前仍须明确 Kafka 数据持久化、恢复与 Inbox 操作身份的配套策略，不能靠清空 Inbox 或反复推进 offset 解决生产问题。

### 实际通过的功能验收

- 现有好友/群 E2E：注册登录、申请/同意好友、列表、备注、通知开关、删除好友、建群、加入审批、改名、转让、角色、群头像、跨 Pod 群消息、退群和离群后不接收新群消息。
- 新增 `TestReliabilityEndToEnd`：三用户群内一人离线；五条跨 Pod 群消息的 ACK/实时/同步 ID 一致；相同 `client_message_id` 重发不生成新 ID，在线接收方各收到一次。
- 离线重连以 `page_size=2` 同步，三页取回全部五个 ID，无分页重复；发送者自己发出的群消息也能同步。
- 指定其他用户的同步请求在 DF 被拒绝，Storage topic 的 offset 不增长。当前实现只记录安全日志，不向客户端发送 Warn；测试不把“无数据返回”等同于客户端收到明确错误。
- 在线撤回事件、离线独立撤回游标补收和正文为空的墓碑；普通同步时间已在未来时仍能取得独立撤回记录。
- 同账号迁移到另一 DF 后旧 socket 关闭；批准的单 DF 重启后，重新登录能取回相同历史 ID 和撤回状态。包含重启的一次完整验收耗时 23.31 秒。
- 新增 `TestDLQAcceptance`：向当前测试 DF topic 注入一个损坏 protobuf，验证实际写入既有 DLQ、原 payload/来源 topic/partition/offset/event_id 和 permanent 分类保留。
- 将本轮已完成的群改名操作模拟放入 Friend DLQ，用仓库现有工具和两个独立测试消费组各重放一次；等待 Friend 提交重放 offset 后，群 UpdateTime 不再变化，只有一个 Inbox 和一个逻辑 Outbox。不会重放历史 DLQ。最终普通运行耗时 12.87 秒。

DLQ 重放是 at-least-once；这些断言只证明该业务操作幂等，**不宣称物理网络投递 exactly-once**。没有做 PostgreSQL 提交点崩溃、Kafka 全集群故障或真实 APNs 发出后的崩溃注入。

### 容量结果

`TestCapacityEndToEnd` 创建 32 个独立账号，保持 32 个真实 WSS 连接。每对用户跨 DF/df2 发送 128-byte text；每个活跃发送者只保留一条在途请求，取得 ACK 且接收方收到后才发送下一条。每档允许发送 20 秒，统计包含最后在途消息排空时间。下表是非 race 运行；总耗时 104.55 秒。

| 活跃发送对数 | 完成消息 | 实测消息/秒 | ACK P50 ms | ACK P95 ms | ACK P99 ms | 到达 P95 ms |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| 1 | 31 | 1.51 | 458.63 | 673.68 | 3162.77 | 685.45 |
| 4 | 65 | 3.07 | 1288.32 | 1353.77 | 1355.45 | 1356.74 |
| 8 | 68 | 3.04 | 2590.43 | 2620.40 | 2623.21 | 2622.05 |
| 16 | 77 | 3.07 | 5164.47 | 5285.67 | 5471.86 | 5288.05 |

共 241 条完成消息；未发现 ACK/接收 ID 不一致、接收重复或请求失败，五个 DLQ 的 log end 未增长。16 对时 ACK P95 超过内部 5 秒停止线，未尝试更高并发。测试的 PASS 只代表正确性断言通过，不代表满足生产延迟 SLO。

每档中段一次 `docker stats` 采样：DF 约 0.98%-1.17% CPU，DF2 约 0.21%-0.34%，Storage 约 1.12%-1.38%，Redis 约 0.49%-0.52%。最大采样内存约为 Storage 36.13 MiB、DF 29.16 MiB、DF2 26.88 MiB、Redis 11.3 MiB；两个 Kafka 约 1.08/1.25 GiB。这不是连续峰值采样，也不是完整机器资源总量。

宿主到当前数据库 `SELECT 1` 十次串行测量的 P50 约 41-45 ms、P95 约 41-46 ms。结合低 CPU、吞吐平台和随并发线性增加的延迟，**推断**主要限制更像串行 I/O/排队而非算力：现有 Storage Outbox 单条领取/发布需要多次数据库往返。没有增加 tracing 或进行 A/B 对照，因此尚不能把约 3 条/秒全部归因于 Outbox；这不是 Redis 已成为瓶颈的证据。

这是短时、闭环、128-byte 单聊的实测，不是开环持续流量、长时 soak、群扇出、文件或 APNs 容量测试；32 个连接也不是最大连接数。

### 复跑入口与检查

以下命令均在 `../services/dataForwardingService` 下执行，只能针对专用测试环境；WSS 使用本机自签名证书测试连接，不是 TLS 安全验收。

```bash
# 好友/群主链路
BETTERFLY_E2E=1 go test -run '^TestFriendServiceEndToEnd$' -count=1 -v -timeout 3m ./integration
# 消息/恢复；重启开关仅在明确允许重启 df 时设置
BETTERFLY_ACCEPTANCE=1 BETTERFLY_E2E_RESTART=1 go test -run '^TestReliabilityEndToEnd$' -count=1 -v -timeout 5m ./integration
# 真 Kafka DLQ 及现有工具两轮隔离重放
BETTERFLY_ACCEPTANCE=1 go test -run '^TestDLQAcceptance$' -count=1 -v -timeout 5m ./integration
# 默认四档短时容量；可调参数仅为测试开关，不是生产配置
BETTERFLY_CAPACITY=1 go test -run '^TestCapacityEndToEnd$' -count=1 -v -timeout 8m ./integration
# 真实恢复/DLQ 的 race 检查，不设置重启开关
BETTERFLY_ACCEPTANCE=1 go test -race -run '^(TestReliabilityEndToEnd|TestDLQAcceptance)$' -count=1 -v -timeout 6m ./integration
# 容量测试本身的有界 race 检查（8 连接、两档各 5 秒）
BETTERFLY_CAPACITY=1 BETTERFLY_CAPACITY_PAIRS=1,4 BETTERFLY_CAPACITY_DURATION=5s go test -race -run '^TestCapacityEndToEnd$' -count=1 -v -timeout 3m ./integration
```

`BETTERFLY_CAPACITY_PAIRS` 默认 `1,4,8,16`，各值限定 1-64；`BETTERFLY_CAPACITY_DURATION` 默认 `20s`，限定 5 秒至 1 分钟。普通 `go test ./...` 不启用真实验收、加压或重启。

本轮 15 个 Go module 的 `go test ./...` 和 `go vet ./...` 全通过；shared、DF、Storage、Friend、Call、Push、AB Test、`../proto/data_forwarding` 八个模块的 `go test -race ./...` 全通过（未变包可能使用 Go 缓存）。新增真实恢复/DLQ 使用 `-count=1 -race` 单独执行并通过，总耗时 34.36 秒；8 连接、两档各 5 秒的容量 race 运行也通过，耗时 17.49 秒，这次结果未混入上面的非 race 容量表。修改的 Go 测试执行了 gofmt，`git diff --check` 通过。

### 明确未验收项

真实 iOS APNs/VoIP/通知撤回显示、双机 WebRTC 音视频、文件上传下载、管理后台 UI 和全基础设施灾难恢复均未验证。本环境 RustFS 为 `unhealthy`，`/minio/health/live` 的 wget 检查返回 403；不能仅据此断言对象读写失效，也不能宣称文件服务验收通过。本轮未修复此健康检查或 Kafka 生命周期问题。

## 测试后的局部优化与复测（2026-09-30）

### 修改与边界

- `../shared/outbox/relay.go` 用单条 `WITH ... SELECT ... FOR UPDATE SKIP LOCKED ... UPDATE ... RETURNING` 原子领取一条事件，替换客户端 BEGIN、查询、更新、COMMIT 四次往返。
- 完成/失败标记仍按 `event_id + claimed + claim_token` 条件更新，但不再对单条原子 UPDATE 额外包一层 GORM 默认事务。领取和标记合计约从七次网络往返降为两次；没有全局关闭业务事务。
- 保留一次只领取一条、租约过期恢复、claim token fencing、稳定 Outbox event_id 和无限退避重试。没有新增 worker、批量领取、租约续期或 ACK 快速路径。
- DF 的 `PublishMessage` 在发送前创建一个随机 128-bit Kafka header `event_id`，Sarama 对同一生产消息的重试沿用该 ID。新 DF 请求不再仅凭 source offset 命中 Inbox，避免 Kafka 日志重建后错认旧操作。
- 原始发布路径保留既有 headers，DLQ 重放不重新生成身份；没有 headers 的旧消息仍使用原来的 offset fallback。没有清空或改写历史 Inbox，不修改 protobuf，也不需要客户端适配或 schema migration。

生产改动只有 Outbox relay 和 DF publisher 两个文件，共新增 33 行、删除 40 行，净减少 7 行；其他改动是测试和本文记录。没有新增依赖、Redis key、Topic、表或生产配置。

使用已有脚本定向重建 `df/df2/storage/friend/push`，加 `--no-deps`；Kafka、Redis、数据库、其他服务和证书不动。没有部署 Kafka 命名卷或切换现有数据挂载。新请求身份解决的是**新 DF 流量的 offset 碰撞**，并不让 Kafka 数据丢失变得安全；历史无 header 消息、旧 DF 混合部署和其他未携带事件 ID 的生产者仍受原边界约束，仍须保障 Kafka 持久化/恢复策略。

### ACK 为什么慢

实际链路仍是：认证 → Kafka 请求 → 业务事务提交消息/Inbox/Outbox → relay 领取和发布响应 → DF 生成 ACK → WebSocket 写队列。ACK 不需要等待 APNs 设备接收，也没有提前到数据库提交之前。

优化前一次数据库往返约 40-45 ms，串行 relay 处理一条事件约需七次往返，仅这部分就约 280-315 ms，因此持续吞吐很容易卡在约 3 条/秒。闭环测试有 16 个发送者轮流等待时，`16 / 3 ≈ 5.3s` 与测得的 ACK 延迟平台吻合。P95 是本阶段约 95% 请求能够收到 ACK 的等待时间，不是“数据库一条 INSERT 用了五秒”。

这一解释得到本次局部优化的前后对照支持，但不是完整分布式 tracing。降低 relay 往返后，低负载 ACK 仍约半秒：远端数据库的业务事务往返、现有 250 ms 空队列轮询、认证和 Kafka 传递仍要耗时；高并发还有各 partition 的串行消费和队列不均衡。本轮没有增加快速 ACK、放宽事务或削弱幂等性来隐藏这些延迟。

### 同条件容量对照

仍是 32 个真实 WSS 连接、每对跨 Pod、128-byte text、1/4/8/16 对活跃发送者、每档 20 秒闭环发送，含最后在途消息排空。数据库没有迁移到本机；复测数据库 RTT P50 为 41.92 ms、P95 为 44.33 ms，和之前相近。

| 活跃发送对数 | 修改前 消息/秒 | 修改后 消息/秒 | 修改前 ACK P95 ms | 修改后 ACK P95 ms | 修改后 到达 P95 ms |
| --- | ---: | ---: | ---: | ---: | ---: |
| 1 | 1.51 | 2.00 | 673.68 | 513.48 | 524.36 |
| 4 | 3.07 | 7.55 | 1353.77 | 879.44 | 883.98 |
| 8 | 3.04 | 9.69 | 2620.40 | 1664.47 | 1667.44 |
| 16 | 3.07 | 11.26 | 5285.67 | 2737.52 | 2740.49 |

本次完成 641 条消息，四档完成数分别为 40/154/202/245，总耗时 98.88 秒。没有请求失败、ID 不一致、接收重复或五个既有 DLQ 的 log end 增长。16 对时吞吐约提升 3.67 倍，ACK P95 约下降 48%；这仍是短时实测，不是生产容量上限或已满足某个毫秒级 SLO 的证明。

16 对中段单次采样：DF/DF2 CPU 约 3.34%/0.71%，Storage 3.79%，Redis 1.03%；Storage 内存约 63.77 MiB，两个 Kafka 约 1.12/1.32 GiB。采样没有表明 Redis 已饱和。

### 新增回归与运行结果

单测覆盖：单条领取不使用显式事务、空队列不发布、领取数据库失败不发布、发布后标记失败仍可过期重放、发布失败保留退避状态、成功/失败标记均保留 fencing。DF publisher 测试覆盖相同 topic/partition/offset 下两个新事件的 operation key 不同、原始发布保留重放 headers、Kafka 发布失败不报告成功。

新增 `TestOutboxPostgresAtomicClaim` 使用现有 PostgreSQL，在独立测试 service 下验证两个 relay 只领取一次，以及过期 worker 不能覆盖后来领取者的完成状态；测试只删除自己创建的 Outbox 行。新增 `TestRequestIdentityEndToEnd` 在各分区的下一条 offset 创建独立 completed Inbox fixture，新 DF 的真实同步请求仍返回并创建自己的事件 Inbox；随后精确清理 fixture，不重置 Kafka、不覆盖历史记录。

```bash
# services/dataForwardingService；仅专用环境，无并发外部 Storage 请求
BETTERFLY_ACCEPTANCE=1 BETTERFLY_E2E=1 go test -race -run '^(TestFriendServiceEndToEnd|TestReliabilityEndToEnd|TestDLQAcceptance|TestOutboxPostgresAtomicClaim|TestRequestIdentityEndToEnd)$' -count=1 -v -timeout 8m ./integration
```

上述五条真实测试全部通过（54.59 秒），包括本次更新后的好友/群管理、消息同步/撤回、DLQ 两轮重放、原子领取和身份冲突；本次没有开启额外单 DF restart 测试开关。15 个 Go module 的 `go test ./...`、`go vet ./...` 和前述八个关键模块的 `go test -race ./...` 全通过；修改的 Go 文件已 gofmt，`git diff --check` 通过。

## 成都数据库条件下的 ACK 延迟优化（2026-09-30）

### 目标评估与实现

目标为 ACK P95 200-300 ms。这里测量的是本机 Compose 到现有成都 PostgreSQL 的链路，不代表云端服务与真实手机的延迟；地名本身不是瓶颈，服务到数据库的 RTT 才是关键。

检查实际代码后，普通新消息的低负载关键路径包含：Auth 验证 JWT 时查询用户一次；Storage 事务中的 BEGIN、Inbox 插入、消息插入、Outbox 插入、Inbox 完成和 COMMIT 六次；relay 领取一次。以上约八次串行数据库往返，按此前实测约 42 ms 粗算就是 336 ms，还可能等待 250 ms 的空队列轮询。这是代码与 RTT 的估算，不是逐段 tracing。

本次只做两项局部修改：

- `ExecuteInboxOutbox` 在原业务事务内使用一条数据修改 CTE，同时写入 Outbox 和完成 Inbox，省去一次往返；事件通过一个 JSON 参数传递，二进制 payload 以 base64 往返，支持零个和多个事件。任何写入错误或 Inbox 条件更新失败仍使整个事务回滚。
- Storage 提交成功并完成原有缓存清理后，通过容量为 1 的本地 channel 唤醒现有 Outbox relay。信号非阻塞、可合并，不携带消息，不新增 goroutine；事务失败和已完成操作重放不发送信号。原轮询保留，负责进程重启、信号丢失、其他副本写入和失败退避的恢复。

没有缓存 JWT、提前 ACK、改变 Kafka offset/DLQ 或幂等语义。仍在业务、Inbox 和 Outbox 提交后产生 ACK，Outbox 保持 at-least-once 和 claim-token fencing。不新增协议、schema、依赖、Topic、Redis key 或部署环境变量。共享事务变更对 Storage/Friend/Push 生效；即时唤醒仅接入 Storage 的既有 relay。

优化后约剩七次串行数据库交互，42 ms × 7 ≈ 294 ms，尚未包含 Kafka、应用处理及客户端网络。因此当前部署下不能承诺稳定低于 300 ms，更不能承诺 200 ms。优先使 Auth/Storage 等依赖数据库的服务与 PostgreSQL 同城或同内网，不必一定迁走成都数据库；再用相同测试复验。高并发下还需把串行 relay 和消费队列的排队计入预算，不能用低负载成绩承诺全部负载的 P95。

### 两次同条件复测

仍使用默认 32 个真实 WSS 连接，1/4/8/16 对发送者、跨 Pod、128-byte text、每档 20 秒闭环发送，不与其他负载测试同时运行。本次定向重建 Storage/Friend/Push，最终缓存清理顺序调整后另重建 Storage；未重建 Kafka、Redis、数据库或执行迁移。

| 发送对数 | 上一轮 ACK P95 ms | 本轮第一次 P95 ms | 最终版本第二次 P95 ms | 第二次 消息/秒 | 第二次 到达 P95 ms |
| --- | ---: | ---: | ---: | ---: | ---: |
| 1 | 513.48 | 329.61 | 311.90 | 3.29 | 314.43 |
| 4 | 879.44 | 676.93 | 598.78 | 9.76 | 600.70 |
| 8 | 1664.47 | 1205.37 | 1083.22 | 11.28 | 1087.49 |
| 16 | 2737.52 | 1882.90 | 2357.69 | 11.34 | 2361.78 |

第一次完成 64/191/224/239 条，共 718 条，测试耗时 99.00 秒；第二次完成 66/200/233/243 条，共 742 条，耗时 98.00 秒。两次均无请求失败、ACK/接收 ID 不一致、接收重复或五个既有 DLQ 的 log end 增长。第二次低负载 P50 为 297.96 ms、P99 为 363.15 ms。

低负载 P95 可重复降至约 312-330 ms，但 **200 ms 未达到，稳定 300 ms 也未验收通过**。16 对的两次尾延迟有波动，不能只取较好的一次；持续吞吐仍约 11 条/秒，没有因本次修改获得明显高负载吞吐提升。每条 Outbox 仍串行进行领取和完成标记，两次远端往返约 84 ms，与该吞吐平台相符，但不能把其他排队成本全部归因于这一因素。短时闭环测试不等于容量上限或生产 SLO 验收。

### 回归覆盖

新增 SQL mock 测试覆盖合并写入失败、Inbox 条件更新失败、COMMIT 失败、无效事件回滚、零/多事件一次完成；Storage 事务回滚/提交失败不唤醒、正常提交唤醒、信号满时不阻塞、重放不重复唤醒；relay 在轮询间隔设为一小时的情况下仍可由提交信号立即唤醒，并正常响应 context 取消。

新增 `TestInboxPostgresCombinedCompletion` 使用独立 service 和无成员的已删除群 fixture，验证真实 PostgreSQL：零/多个事件和二进制响应原样保存；四个并发调用同一 operation 仅执行一次业务；Outbox ID 冲突回滚业务和 Inbox。只清理自己的测试 fixture，不迁移 schema、不发布到 Kafka。

```bash
# services/dataForwardingService，仅专用测试环境
BETTERFLY_ACCEPTANCE=1 BETTERFLY_E2E=1 go test -race -run '^(TestFriendServiceEndToEnd|TestReliabilityEndToEnd|TestDLQAcceptance|TestOutboxPostgresAtomicClaim|TestRequestIdentityEndToEnd|TestInboxPostgresCombinedCompletion)$' -count=1 -v -timeout 8m ./integration
BETTERFLY_CAPACITY=1 go test -run '^TestCapacityEndToEnd$' -count=1 -v -timeout 8m ./integration
```

15 个 Go module 的 `go test ./...`、`go vet ./...` 和上述八个关键模块的 `go test -race ./...` 全通过，真实 PostgreSQL 合并事务单独 `-race -count=1` 通过（2.95 秒）。最终上述六条真实环境 race 验收全部通过，总耗时 54.49 秒，包含好友/群管理、离线同步/撤回、跨 Pod ownership、两轮 DLQ 重放、原子领取 fencing、请求身份与合并事务；本次没有开启额外 DF restart 开关。回归中的宿主到数据库十次 `SELECT 1` 测得 RTT P50 为 40.74 ms、P95 为 41.78 ms，较上一轮略低，并非完全控制网络条件的 A/B。

修改的 Go 文件已 gofmt，`git diff --check` 通过。普通模块测试中的真实环境测试仍为 opt-in，不把跳过算作真实验收。真实 APNs、文件、音视频和灾难恢复的未验收边界不变。

## 广播频道验收（2026-09-30）

本节对应频道功能，不覆盖上一节已记录的性能修改。协议与能力见`CHANNELS.md`。

### 部署与迁移

使用现有专用 Compose 环境和原 PostgreSQL，构建现有 db_migrate 并执行 v6：
仅新增 channel_settings 和索引，保留既有用户、群关系、消息、账本；历史 v1–v5 未修改。
已重建 DF/DF2、Friend、Storage、Push，未重启 Kafka/Redis，也未删除历史数据。
新协议字段是追加字段；独立 DF/Friend/Storage protobuf 模块及消息生成工具增加了
现有本地 proto 根模块引用，没有新增第三方依赖。

### 单测与失败路径

- 输入校验：Unicode 长度、username 规范、optional PATCH、负数游标/页长、默认/上限页长。
- 鉴权：请求无 JWT 不入消息队列；只读用户不能发帖；Storage 独立检查身份和角色，不执行越权 INSERT。
- 历史：按 message_id 倒序分页；撤回墓碑清空正文与文件名；基础设施异常返回重试错误而非缓存业务失败。
- 缓存：L1/L2 命中仍检查私有频道当前权限，曾发布但已退订的用户不能绕过。
- 兼容：旧群默认 is_channel=false，群查询/已加入列表映射保留标识；空 Logout/旧 QueryGroup 字节语义保留。
- 帖子删除：频道 owner/admin 可删旧帖，subscriber 拒绝，普通消息两分钟窗口不变。
- 推送：频道名称与头像、无作者正文前缀、optional is_channel 标识、普通群 payload 保持兼容。
- 迁移：v5→v6 仅待执行 v6，再次执行无待执行版本。

15 个 Go module 全部 `go test ./...` 和 `go vet ./...` 通过。
shared、DataForwarding、Storage、Friend、Call、Push、AB Test、proto/data_forwarding 的
`go test -race ./...` 全部通过，共 38 个模块检查；

### 真实频道 E2E

`TestChannelsEndToEnd` 使用独立账号、跨两个 DF 的 WSS、实际 Kafka 与 PostgreSQL：
创建/公开搜索/预览、重复订阅不增人数、发帖拒绝和零入库、实时接收、入订阅前历史、
分页、资料 PATCH、不公开私有频道、私有邀请显式接受、管理员发帖/删除、owner 不能直接退出、
退订后的私有访问拒绝、历史同步、并发转让唯一 owner、删除频道保留三条消息。
另验证 username 冲突回滚整个新群，直接创建 PRIVATE 不被 bool 默认值变为公开，
以及旧版主动入群接口不能为私有频道创建新申请。

```bash
# services/dataForwardingService；仅专用已迁移环境
BETTERFLY_ACCEPTANCE=1 BETTERFLY_E2E=1 go test -race -run '^(TestChannelsEndToEnd|TestFriendServiceEndToEnd|TestReliabilityEndToEnd|TestDLQAcceptance|TestOutboxPostgresAtomicClaim|TestRequestIdentityEndToEnd|TestInboxPostgresCombinedCompletion)$' -count=1 -v -timeout 8m ./integration
```

最终版本七项实测全部通过，总耗时 79.23 秒，频道单项 26.28 秒。
涵盖既有好友/群/账号、跨 Pod、离线同步/撤回、DLQ 重放、请求身份和事务/Outbox fencing。
本轮没有启用额外 DF restart 开关；没有重复容量压测。

`TestChannelPushEligibilityPostgres` 用未提交事务中的用户、频道、Token、消息和投递行，
执行生产 claim SQL，验证当前 owner 可投递，非订阅者的普通帖和负数账本键撤回均被排除。
事务整体回滚，运行中的 Push worker 看不到 fixture，没有发真实 APNs。

```bash
# services/pushService；PGSQL_DSN 从安全环境注入，不打印凭据
DB_MAX_OPEN_CONNS=2 DB_MAX_IDLE_CONNS=1 BETTERFLY_ACCEPTANCE=1 go test -race -run '^TestChannelPushEligibilityPostgres$' -count=1 -v -timeout 1m ./internal/push
```

真实 PostgreSQL 推送资格测试通过，测试耗时 1.43 秒；APNs payload 另用本地模拟端点验收。
本轮未进行 iOS/Notification Service Extension 实机或真实 APNs 新频道通知验收，需客户端完成适配。
百万订阅者容量、评论/反应、静音、邀请链接不在本轮范围。

## 单张图片与配文（2026-10-01）

协议与客户端适配说明见 [IMAGE_CAPTIONS.md](IMAGE_CAPTIONS.md)。
本轮使用全新 v7 迁移追加 messages.caption，不重写已发布迁移。

覆盖 PB 旧字节兼容及六个新增字段往返；4096 code points/16KiB、非法 UTF-8、
换行/Markdown 原文保留、非 image 拒绝；Storage 新插入及不同正文重试返回原记录。
覆盖查询、普通/独立撤回同步、频道历史的配文透传和三字段撤回遮蔽；
图片 L1/L2 旧缓存命中回源，源/目标 DF 抑制已撤回图片 Kafka 重放，数据库失败仍重试。
覆盖一次副作用及 canonical ACK、频道只读鉴权、频道/普通群/私聊 APNs 摘要和头像标识。

执行结果：15 个 Go module 各自 go test ./...、go build ./...、go vet ./... 通过；
shared、DF、Storage、Friend、Call、Push、AB Test、proto/data_forwarding
各自 go test -race ./... 通过。make -C proto、gofmt 检查及 git diff --check 通过。

新增 TestImageCaptionEndToEnd 的显式启用入口：

```bash
# 已迁移 v7 且已重建的专用 Compose
cd services/dataForwardingService
BETTERFLY_ACCEPTANCE=1 go test -race -run '^TestImageCaptionEndToEnd$' -count=1 -v -timeout 3m ./integration
```

本轮 docker compose ps 未发现运行容器，因此真实 WSS/Kafka/PostgreSQL 图文 E2E
未运行（默认测试跳过不代表实测通过），未执行远端/本地数据库 v7 迁移。
真实上传对象、APNs 设备通知和 iOS/NSE 仍需部署/客户端适配后验证。

## 置顶、免打扰与引用回复（Schema v8）

新增/扩展测试分布于 shared/db、Friend handler、Storage handler、DF
handlers/consumer/integration、Push 以及 proto/data_forwarding。

- 置顶：owner/admin 权限，普通成员/非成员拒绝，同频道校验，缺失和撤回目标，
  取消置顶，数据库失败事务回滚，频道消息撤回清除对应置顶。
- 免打扰：只更新可信操作者的当前成员记录，不改变 JoinedAt；非成员/已删除
  会话和数据库故障不假报成功；偏好与响应/已加入会话列表映射正确。
- Push：入队及领取双检查，排队静音任务不发送且不误停用token；撤回替换和
  VoIP不受静音影响；20,000目标的SQL参数数量仍固定，旧频道/群/私聊展示回归。
- 引用：私聊双向和同群权限，跨会话/不可读/缺失目标拒绝；单条记录和canonical
  重试，ACK/实时/查询/普通同步/撤回同步/频道历史透传；L1/L2缓存命中复查
  撤回且不修改共享缓存实体，数据库异常不回退暴露旧引用正文。
- PB：新增字段号、往返、旧Post字节的零引用与频道默认通知开启语义。

本轮所有15个Go module的 go test ./...、go vet ./... 和构建通过；
shared、DF、Storage、Friend、Call、Push、AB Test、proto/data_forwarding
的 go test -race ./... 通过。服务二进制构建输出到临时目录。
make -C proto 再次生成的15个pb.go摘要未变化，gofmt检查和git diff --check通过。

已扩展真实频道E2E，包含新订阅者读置顶、管理员置顶/取消、静音期间实时消息
仍送达、引用历史/同步和撤回自动清除置顶。显式执行入口：

```bash
# 专用Compose已迁移v8，所有业务副本已重建
cd services/dataForwardingService
BETTERFLY_ACCEPTANCE=1 go test -race -run '^TestChannelsEndToEnd$' -count=1 -v -timeout 3m ./integration
```

Push已有的 TestChannelPushEligibilityPostgres 扩展验证实际 INSERT SELECT 和
worker SQL：静音/非成员不入队，领取时重新检查，静音成员仍接受撤回替换。
其fixture全部在回滚事务内，运行worker不可见，不会产生真实APNs请求：

```bash
cd services/pushService
BETTERFLY_ACCEPTANCE=1 go test -run '^TestChannelPushEligibilityPostgres$' -count=1 -v ./internal/push
```

两者都需现有验收环境与PGSQL_DSN。本轮本机Docker daemon未运行，因此以上
真实WSS/Kafka/PostgreSQL验证跳过，未实际执行数据库v8迁移，也未执行设备APNs
或iOS联调。默认测试中的skip不等于真实环境通过。
协议和部署契约见 CONVERSATION_FEATURES.md
