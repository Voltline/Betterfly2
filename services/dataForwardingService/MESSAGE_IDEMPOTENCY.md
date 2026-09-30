# 消息幂等与 ACK 关联

## 客户端要求

发送 `Post` 时应生成不超过 128 字符的 `client_message_id`。该 ID 在一条本地消息的所有重试中必须保持不变，不同消息必须不同，推荐直接使用 UUID 或本地数据库的稳定消息键。

服务端返回 `PostAckRsp` 时会同时返回：

- `message_id`：数据库中的最终消息 ID。
- `client_message_id`：原请求的关联 ID。

客户端应通过 `client_message_id` 定位本地消息并更新状态，不应再依赖 ACK 到达顺序。服务端仍兼容未发送该字段的旧客户端，会根据消息发送者、接收者、内容和原始时间戳生成稳定的 `legacy:` 幂等键。

## 服务端语义

1. DataForwardingService 在产生任何副作用前申请 Redis 幂等键。
2. StorageService 使用 `(from_user_id, client_message_id)` 数据库唯一索引完成持久化兜底。
3. 只有 StorageService 返回 `created=true` 时才执行实时转发和 APNs。
4. 重复客户端请求返回第一次生成的 `message_id`，不会再次写库，也不会通过 `created=false` 的存储响应重新触发转发或推送。
5. Kafka 存储响应通过 `message_id` 副作用键抑制已完成工作的重放。完成表示实时消息已进入写队列/Kafka，或明确离线等待同步，并且普通 APNs 请求已成功发布到 Kafka；不表示客户端已收到或 APNs 已送达。
6. PushService 以 `(message_id, token_id)` 持久投递账本跳过已 `sent` 的设备；可重试失败保留为 `retryable`，由现有 worker 恢复；账本低频清理，默认保留 30 天。
7. 普通 APNs 使用 `message_id` 生成 `apns-collapse-id`，作为 Apple 侧的额外折叠保护。
8. Monitor 指令不写入消息表，但会在 Redis 中缓存 ACK 和执行结果；重复请求只重放结果，不重新执行命令。

Redis 中的处理中状态保留 30 秒，成功 ACK 保留 7 天，副作用标记保留 30 天，均为有界缓存。数据库唯一约束是长期幂等依据。

## 离线与重试边界

ACK 只确认消息已持久化。已保存的普通单聊/群聊消息没有有效 WebSocket 路由时，记录等待同步，离线成员下次通过消息同步取得消息；不因一个成员离线重试整组在线成员。APNs 请求仍按原策略发布。跨 Pod 投递同样处理明确离线；没有 `message_id` 的历史跨 Pod 报文不据此推断已经保存，保留原错误行为。

Redis 查询失败、Kafka 发布失败、连接写队列不可用都不是正常离线，仍进入现有有限重试与 DLQ。通话、好友响应等其他消息不采用普通消息的离线延后规则。发送者 ACK 投递失败也仍保留现有重试/DLQ 行为。

Kafka 和 Outbox 均是 at-least-once。部分发送成功后发生真实故障、发送后完成标记写入失败、进程崩溃或有限幂等记录过期，都可能产生重复网络投递。客户端应按服务端 `message_id` 去重；旧客户端仍能解析报文，但不能因此获得完全相同的去重能力。`event_id` 提供稳定事件身份及追踪，不表示 DataForwarding 已对所有事件实现去重，也不承诺端到端 exactly-once。

## 部署要求

本轮收口不修改 Protobuf、数据库 schema 或客户端 API，不需要新迁移。升级 DataForwarding 可生效离线语义及踢人解析修复；混合版本期间旧 Pod 仍保留旧投递行为。DLQ 重放前，应先升级使用 shared consumer 的服务以识别稳定 `operation_key`，再使用更新的重放工具，详见 `deploy/kafka-dlq.md`。既有 schema 迁移仍通过独立迁移命令/部署流程执行，不依赖生产业务进程启动时 AutoMigrate。
