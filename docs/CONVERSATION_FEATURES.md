# 置顶、免打扰与引用回复

本轮复用 Friend 的成员关系和事务型 Inbox/Outbox、Storage 消息、DF 转发及
Push 持久投递账本，不新增服务、Topic、后台循环、Redis key 或依赖。
置顶与免打扰变更不实时广播：操作者立即获得响应，其他会话在下次查询时更新。

## 接口

所有请求仍通过已认证 WebSocket 的 `RequestMessage` 发送，并携带有效 jwt。
操作者取自认证连接，客户端不提供可用于修改其他用户偏好的 user_id。

### 频道置顶

`channel_request.set_pin{channel_id, message_id}`：仅当前 owner/admin 可操作，
每频道一条置顶，message_id=0 取消置顶。目标必须是该频道的未撤回帖子。
普通订阅者不能置顶；其他会话或不存在的消息返回 CHANNEL_NOT_FOUND；
已撤回目标返回 CHANNEL_INVALID_STATE；非法 ID 返回 CHANNEL_INVALID_ARGUMENT。
私有频道对非成员仍隐藏存在性。

响应为现有 `channel_response`，operation=`set_channel_pin`，成功时 channel
包含 pinned_message_id。Get/Search/ListSubscribed/Subscribe/Update/Create
返回的 ChannelInfo 也包含当前置顶 ID。新订阅者可立即获得入订阅前的置顶。
用现有 `query_message{message_id}` 读取帖子内容，无需从历史第一页猜测置顶。
取消置顶、帖子撤回不删除原消息记录；撤回与清除该 ID 在同一事务内完成。
服务器锁定消息行，避免已撤回帖子被并发重新置顶。频道删除后仍沿用不可访问规则。

### 群聊／频道免打扰

`update_group_notify{target_group_id, is_notify}`：is_notify=false 开启免打扰，
true 恢复通知。只允许修改自己当前有效成员关系；未入群、已退群或已删除会话
返回 RECORD_NOT_EXIST。owner/admin/member 都可设置自己的偏好，无权代他人设置。

响应复用 `group_member_operation_rsp`：operation=`update_group_notify`，
result 使用既有 FRIEND_OK / INVALID_ARGUMENT / RECORD_NOT_EXIST 等字符串，
group_id、user_id 为会话和操作者，notifications_muted 为实际存储值。
只有 result=FRIEND_OK 时应用成功状态；数据库故障仍走现有重试/DLQ，不假报成功。

QueryJoinedGroups 的 JoinedGroupInfo 和频道 ChannelInfo 返回调用者自己的
notifications_muted；不在群成员列表中公开其他人的通知偏好。
查询返回的置顶和偏好应始终合并，不能仅因 Group.UpdateTime 未变化就忽略；
自动撤回清除置顶和个人偏好变化不保证刷新群资料的时间戳。
默认 false；离开后物理删除成员记录，重新加入默认为不静音。
角色变化不覆盖偏好，设置偏好不修改 JoinedAt。

普通 APNs 在持久入队时过滤免打扰成员，在 worker 领取时再检查最新设置。
已领取待发送的消息可能与随后设置免打扰发生竞态，不能撤销已提交给 APNs 的通知。
已经因免打扰跳过的旧消息不会在恢复通知后补推；消息仍在历史／同步中。
不影响实时 WebSocket、消息存储、ACK、同步、未读状态或 VoIP。
撤回替换通知仍发送给当前成员，用于清理旧通知；不会按静音设置重新暴露原正文。
Push Admin 的会话消息默认遵循偏好，既有 IgnorePreferences 显式绕过能力保留。

### 引用回复

沿用 `Post`，添加 reply_to_message_id，0 表示普通消息。支持既有消息类型，
图片配文与文件名仍使用 caption 和 real_file_name，不复用正文字段存引用 JSON。
一条回复只有自己的一个 message_id、服务端 timestamp 和 client_message_id。

服务器校验引用目标存在、会话类型与会话相同、发送者可读取原消息。
私聊双方的发送／接收方向均可引用；群聊入群前不可读的消息、跨群、跨私聊引用
统一拒绝为 Storage INVALID_ARGUMENT，DF 映射现有 Warn，不产生新记录或 ACK。
频道发布仍仅限 owner/admin；引用不能让订阅者变成发布者，也不是评论功能。

仅保存引用 ID，不保存正文快照、嵌套引用或发送者昵称快照。客户端可从已授权
本地消息解析引用，缺失时通过 QueryMessage 获取；服务器不对每页引用作 N+1 查询。
QueryMessage 每次重新鉴权，并在缓存命中时检查最新撤回状态，失败时不回退暴露缓存。
被引用消息撤回后其 content/caption/real_file_name 为空，is_recalled=true；
引用消息自己的内容不被删除，仍携带原引用 ID。不可读取的引用显示不可用，不能
通过引用 ID 绕过入群时间、退群或私有频道的权限规则。

同 sender + client_message_id 的重试采用 first-write-wins：返回原始 ID、时间、
正文、配文和引用 ID，不覆盖记录，不产生第二次投递或 APNs 副作用。
合法但不同的重试引用被忽略；首次不存在或无权读取的引用被拒绝。
负数引用 ID 始终不合法。新功能依然要求稳定 client_message_id。
PostAckRsp 的既有语义不变；ACK 不包含引用快照，不代表已读或已送达。
APNs 预览使用回复自身内容，不附带引用正文，标题、头像和频道规则保持原状。

## 字段号与部署

所有 Protobuf 字段只追加；0/false 默认值保持旧序列化数据行为。

| 消息 | 新字段号 |
| --- | --- |
| DF RequestMessage | update_group_notify=41 |
| DF UpdateGroupNotify | target_group_id=1, is_notify=2 |
| Friend RequestMessage | update_group_notify=27 |
| Friend UpdateGroupNotify | group_id=1, is_notify=2 |
| DF GroupMemberOperationRsp / Friend GroupOperationRsp | notifications_muted=9 / 8 |
| DF JoinedGroupInfo / Friend JoinedGroupContact | notifications_muted=7 |
| ChannelRequest | set_pin=11 |
| SetChannelPin | channel_id=1, message_id=2 |
| ChannelInfo | pinned_message_id=12, notifications_muted=13 |
| DF Post / ChannelPost | reply_to_message_id=11 |
| DF MessageRsp | reply_to_message_id=13 |
| Storage StoreNewMessage | reply_to_message_id=10 |
| Storage StoreMsgRsp / MessageRsp | reply_to_message_id=13 |

Go 协议生成：`make -C proto`，生成文件不可手改。
iOS 重生成 data_forwarding/common.proto、request.proto、response.proto、
df_interface.proto 和 channel/channel.proto 及现有 import 闭包。
内部 Friend/Storage 不需要生成进 iOS 应用。

Schema v8 仅追加三个 NOT NULL 列：ChannelSettings.PinnedMessageID（默认0）、
GroupMember.NotificationsMuted（默认false）、Message.ReplyToMessageID（默认0）。
无新表、FK、正文重写或删除；迁移在现有 advisory lock 下逐版本事务执行，
ADD COLUMN IF NOT EXISTS 可重复运行，失败不会记录成功版本，可重试。

1. 重建 db_migrate 镜像，并运行 Compose db_migrate 或 K8s schema-v8 Job。
2. 确认迁移成功，再部署 Friend、Storage、Push，最后 DataForwarding。
3. 所有副本完成升级后再开放客户端新入口。旧客户端无需更新即可继续原有功能。
4. 回滚先 DF 后业务服务，保留新增列，不执行降级删除。旧服务可使用新增 schema。

旧服务能忽略新增字段，但不具备新功能，可能丢失引用、无法置顶或不遵循静音。
混合版本阶段不能开启新功能；不是宣称旧代码能够执行新接口。
K8s 使用新的 v8 Job 名称与固定镜像；没有新建第二套迁移路径。

## 请求示例

以下是 protobuf 字段示意，不是新 JSON/HTTP API；实际通过现有二进制 WebSocket 发送。

```json
{"jwt":"<current-jwt>","channel_request":{"request_id":"pin-1","set_pin":{"channel_id":9001,"message_id":44}}}
{"channel_response":{"request_id":"pin-1","result":"CHANNEL_OK","operation":"set_channel_pin","channel":{"channel_id":9001,"pinned_message_id":44}}}
{"jwt":"<current-jwt>","update_group_notify":{"target_group_id":9001,"is_notify":false}}
{"group_member_operation_rsp":{"operation":"update_group_notify","result":"FRIEND_OK","group_id":9001,"user_id":2,"notifications_muted":true}}
{"jwt":"<current-jwt>","post":{"to_id":9001,"is_group":true,"msg_type":"image","msg":"<verified-image-hash>","caption":"回复公告","reply_to_message_id":44,"client_message_id":"reply-1"}}
{"post_ack_rsp":{"message_id":45,"client_message_id":"reply-1","timestamp":"2026-10-02T00:00:00Z"}}
{"post":{"from_id":1,"to_id":9001,"is_group":true,"msg_type":"image","msg":"<verified-image-hash>","caption":"回复公告","reply_to_message_id":44,"message_id":45,"timestamp":"2026-10-02T00:00:00Z","client_message_id":"reply-1"}}
{"jwt":"<current-jwt>","query_message":{"message_id":44}}
{"message_rsp":{"message_id":44,"from_user_id":1,"to_user_id":9001,"is_group":true,"msg_type":"text","content":"","caption":"","real_file_name":"","is_recalled":true,"recalled_at":"2026-10-02T00:01:00Z","recalled_by":1}}
```

测试入口与真实环境限制见 [回归测试文档](REGRESSION_TESTING.md)。
Kafka/Outbox/APNs 仍是至少一次边界；客户端按 message_id 去重并让撤回墓碑优先。
