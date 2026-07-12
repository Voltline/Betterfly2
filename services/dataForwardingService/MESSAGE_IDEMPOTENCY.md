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
4. 重复请求返回第一次生成的 `message_id`，不会再次写库、转发或推送。
5. Kafka 存储响应通过 `message_id` 副作用键防止重复消费再次触发投递。
6. PushService 以 `(message_id, token_id)` 投递账本阻止 Kafka 重投再次请求 APNs，发送失败时释放记录以允许重试；账本保留 30 天并低频清理。
7. 普通 APNs 使用 `message_id` 生成 `apns-collapse-id`，作为 Apple 侧的额外折叠保护。
8. Monitor 指令不写入消息表，但会在 Redis 中缓存 ACK 和执行结果；重复请求只重放结果，不重新执行命令。

Redis 中的处理中状态保留 30 秒，成功 ACK 保留 7 天，副作用标记保留 30 天，均为有界缓存。数据库唯一约束是长期幂等依据。

## 部署要求

本次变更同时修改了 DataForwardingService、StorageService、PushService 和 Protobuf。部署时应在同一维护窗口完成三个服务升级；数据库由服务启动时的 AutoMigrate 添加可空 `client_message_id` 列、消息唯一索引和 Push 投递账本，历史消息保持为 `NULL`，无需回填。
