# 客户端消息同步适配

本轮协议仅尾部新增字段，不修改已有字段号。旧客户端可以继续通信，但补收撤回和实时按服务端 ID 去重需要客户端适配。

## 可直接给客户端 Codex 的 Prompt

请适配 Betterfly2 的以下消息协议增量，并保留旧服务端兼容性。先重新生成 Swift Protobuf，再修改本地消息合并和同步状态机，不要仅改 UI。

1. `Post.message_id = 9` 是服务端入库 ID。客户端发送时保持 0，继续生成稳定的 `client_message_id`，重试不可换 ID。实时收消息用 `message_id` 做持久化 upsert；旧服务端返回 0 时继续使用现有兼容逻辑。
2. `PostAckRsp.timestamp = 3` 是服务端入库时间。按 `client_message_id` 找到本地待发送消息，补上 `message_id` 和非空的服务端时间。ACK 只确认入库，不代表接收方已收到。重复 ACK 不重复创建消息或触发提示。
3. 实时 `Post.timestamp` 和同步 `MessageRsp.timestamp` 采用同一入库时间。不要再用本机时间或实时消息的最大时间推进同步游标，只使用同步响应的复合游标。
4. `QuerySyncMessages` 新增 `include_recalled_changes = 6`、`recall_cursor_timestamp = 7`、`recall_cursor_message_id = 8`。开启 `include_recalled_changes`；按账号独立持久化普通消息游标和撤回游标。首次升级时撤回游标留空，服务端从 epoch 开始分页补收历史撤回，不要用普通消息游标初始化它。
5. `SyncMessagesRsp` 新增 `recalled_msgs = 5`、`recalls_has_more = 6`、`next_recall_cursor_timestamp = 7`、`next_recall_cursor_message_id = 8`。`recalled_msgs` 是已遮蔽正文和真实文件名的 `MessageRsp`；其 `timestamp` 仍是原消息时间，撤回分页用 `recalled_at`。普通消息页和撤回页独立推进；任一 `has_more` 为 true 时继续请求，不得因为普通页为空就结束。空页会保留对应游标。旧服务端不返回撤回游标时，不要将本地撤回游标清空或假定撤回已补齐。
6. 在一次新同步任务开始时，将已保存的撤回时间向前重叠至少 5 分钟，并将撤回 ID 下界设为 0，覆盖同秒较小 ID 的后续撤回及正常事务提交延迟。此重叠只在新任务开始时执行，任务内部严格使用服务器返回的游标，否则会循环。重复墓碑必须幂等合并。该时间游标不是 PostgreSQL commit sequence，不能声称覆盖任意长的事务延迟。
7. 同步现在也包含自己发送的单聊消息。按 `from_user_id` 判断方向，不得把它们当作收到的消息。普通同步、实时消息和 ACK 共用同一服务端 ID 去重规则。
8. 按 `message_id` 将撤回处理为不可逆墓碑：不存在的消息先创建墓碑；后续迟到的实时 Post 或普通同步不能恢复其正文。按既有逻辑更新会话预览、附件显示、未读数，并移除对应本地通知。实时 `MessageRecallEvent`、APNs 的 `message_recalled` 和同步墓碑使用同一合并逻辑。
9. 本地消息变更和游标推进必须在同一本地事务中落盘。网络重试或进程崩溃后可以重放，不能提前保存游标导致漏消息。

请添加测试：离线期间已同步消息被撤回、普通页为空但撤回页有多页、同秒多条撤回及重叠重放、实时/ACK/同步乱序去重、自己的单聊同步、旧协议 fixture，以及墓碑先于原消息到达。

## 服务端部署与边界

- 先确认数据库已有已发布的 v5 撤回迁移，再部署 Storage，最后部署 DataForwarding。本轮不新增 migration；v4 会被启动版本检查明确拒绝。
- Redis ACK 值仍使用旧的 `ack:<id>` 格式；已完成消息的客户端重试经过 Storage 幂等读取原记录，取得原始服务端时间，`created=false` 不重复触发投递。
- Kafka 维持至少一次投递。群聊部分成功后重试可能重复实时事件，客户端必须按 ID 去重；APNs 继续使用已有逐设备账本。
- 离线、Redis/Kafka/本地写队列失败不等同于实时投递成功；达到现有重试上限且 DLQ 成功后才确认 offset。离线消息仍可通过数据库同步恢复；DLQ 不会自动替代客户端同步。
- 副作用占位使用现有 key 的短 TTL 和 owner fencing，完成后才写长期完成标记，不引入新表、Topic、后台任务或配置。进程崩溃后需要等占位过期再重放；发布后崩溃仍可能产生重复，不承诺 exactly-once。
- 未升级客户端不会主动请求新的撤回分页，离线补收撤回仍可能缺失。秒级时间游标配合重叠重放仅覆盖正常延迟；超长事务或数据库时钟异常需要扩大回溯或重新同步。
- 当前没有专门的撤回时间查询索引；大消息库上线前应使用真实数据执行 EXPLAIN 验证查询成本。本轮不重写已发布迁移，也不添加索引迁移。
