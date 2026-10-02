# 频道关联讨论区

## 模型与权限

频道继续只允许 owner/admin 发布。频道 owner 可以绑定一个自己拥有的普通群，
作为公告讨论区；一个群最多绑定一个频道，管理员不能代替 owner 绑定。
不创建额外服务、Kafka Topic 或专属评论表。

绑定后发布的新公告会在同一 Storage Inbox/Outbox 事务内生成一条讨论根卡片：

- 公告仍存于频道，有自己的 message_id。
- 根卡片存于讨论群，有另一个 message_id，仅引用公告，不复制正文或配文。
- 公告与卡片的 discussion_root_message_id 都指向卡片 ID。
- 卡片的 source_channel_message_id 指向公告；该字段只允许服务端生成。
- 评论沿用 Post 和 messages 表，收件人为讨论群，携带同一个根 ID。
- 一条评论只有一个 message_id，在群时间线和公告评论页共享同一记录。

必须是**当前讨论群成员**才能发评论或读取评论，包括评论作者本人。
加入仍走现有群申请/邀请审批；订阅频道不等于加入讨论群，不自动添加群成员。
当前群成员可以读取该线程的入群前评论，但这不开放普通群的入群前聊天记录。
私有频道还要求当前频道成员身份；只加入讨论群不能绕过频道隐私。

公开频道未入群用户可以查询公告的 DiscussionInfo（用于展示入群入口），
但 joined/can_comment=false、reply_count=0，不能拉取评论。
私有频道非成员得到 CHANNEL_NOT_FOUND，不能探测内容或线程。

解绑或改绑时旧线程永久只读，已有评论保留；重新绑定同一个群也不重开旧线程。
只有之后的新公告生成新的卡片。未绑定时的旧公告不自动补卡片。
群或来源频道删除后线程不可访问；退群后不能继续从查询、同步或排队推送读取评论。

## 增量协议

定义见 proto/channel/channel.proto 及 data_forwarding、storage、push 下对应文件。
所有现有字段号、PostAckRsp、JWT 与 client_message_id 语义保持不变。

| 消息 | 追加字段 | 字段号 |
| --- | --- | --- |
| DF RequestMessage | get_discussion / query_discussion_replies | 42 / 43 |
| ChannelRequest | set_discussion_group | 12 |
| ChannelInfo | discussion_group_id | 14 |
| DF Post | discussion_root_message_id / source_channel_message_id | 12 / 13 |
| DF MessageRsp | discussion_root_message_id / source_channel_message_id | 14 / 15 |
| ChannelPost | discussion_root_message_id / source_channel_message_id | 12 / 13 |
| ChannelResponse | discussion | 11 |
| Storage RequestMessage | get_discussion / query_discussion_replies | 12 / 13 |
| Storage StoreNewMessage | discussion_root_message_id | 11 |
| Storage StoreMsgRsp / MessageRsp | discussion_root_message_id / source_channel_message_id | 14 / 15 |
| Storage RecallMessageRsp | discussion_root（原子级联撤回结果） | 7 |
| Push MessagePushRequest | discussion_root_message_id | 9 |

客户端重新生成 channel/channel.proto 和 data_forwarding/common.proto、
response.proto、df_interface.proto 及其既有依赖，不手改生成产物。
内部 Storage/Push 协议不需要为 iOS 增加新的生成目标。

### 绑定和查询

`channel_request.set_discussion_group{channel_id,group_id}` 经 Friend 现有
事务链路处理，响应 operation=set_discussion_group，返回更新后的 ChannelInfo。
group_id=0 解绑；要求操作者为频道 owner，非零目标还必须是该普通群 owner。
非普通群目标/自身 ID 返回 INVALID_ARGUMENT，已绑定其他频道返回 ALREADY_EXISTS。

`get_discussion{request_id,channel_id,post_message_id}` 经 Storage 查询，
operation=get_discussion。DiscussionInfo 提供 channel_id、post_message_id、
group_id、root_message_id、joined、can_comment、closed、reply_count 和 canonical post。
没有讨论根的旧公告返回 NOT_FOUND；客户端不要把它当成需要修复的上传失败。
request_id 只关联响应，不是幂等键。长度最多 128 bytes。

`query_discussion_replies{request_id,root_message_id,page_size,before_message_id}`
operation=query_discussion_replies，复用 ChannelResponse.posts（ChannelPost 模型）。
page_size 默认50、最大100，message_id 降序，0游标取最新，之后使用
next_before_message_id；has_more 标识续页。负数页长/游标拒绝。
返回评论及撤回墓碑，不包括根卡片；reply_count 只数未撤回评论。
客户端展示可反转为正序，但不得改变翻页游标。closed 线程仍可查询保留的评论。

### 发送、历史和幂等

评论使用现有 Post：is_group=true，to_id=讨论群，discussion_root_message_id=根ID。
msg/msg_type/caption/real_file_name 保持原消息模型；可用 reply_to_message_id
引用同线程卡片或评论，不能引用频道原帖、其他线程或其他会话。
旧的普通引用回复不自动变成评论；必须显式填写根 ID。
任何客户端 Post 的非零 source_channel_message_id 都会被拒绝。

只有新公告落库时创建根卡片，原帖、根卡片和两个 Outbox 事件同事务提交。
原帖与评论正常返回 ACK；自动根卡片只实时投递，不产生第二个客户端 ACK。
QueryMessage、QuerySyncMessages、QueryChannelHistory 和评论分页均透传根/来源字段。
根卡片的 content/caption/real_file_name 为空，应按引用卡片展示，不渲染为空聊天气泡。

必须复用稳定 client_message_id。相同用户与 ID 重试保留首条 canonical 内容、
线程 ID、message_id 和时间，不覆盖原文，不重复创建根或评论。
既有副作用账本抑制重复实时/APNs，但 Kafka/Outbox 仍是 at-least-once，
客户端必须按 message_id 合并去重并优先保留撤回墓碑。

新入群后首次打开评论必须调用独立线程分页，不仅依靠全局同步时间游标。
同步包含当前可读的讨论记录，但不会自动把旧客户端游标倒退来补历史。
读取单条消息和 L1/L2 命中时仍检查当前群关系及私有频道权限，不缓存授权结果。
跨 Pod 投递与 Push 领取再次按当前关系过滤，避免排队期间退出后的内容泄漏。

## 撤回与推送

撤回频道原帖会在同一数据库事务撤回对应根卡片，并生成两者的撤回通知，
线程立即停止新评论，已存在的评论不级联删除。
讨论群 owner/admin 可撤回评论或根卡片，不受普通作者两分钟窗口限制；
普通成员对自己的评论仍沿用原两分钟规则，不能撤回服务端卡片。
撤回卡片会关闭线程，不会删除频道原帖。

墓碑同时遮蔽正文/hash、caption、real_file_name；保留 ID/线程关系。
晚到的根卡片或评论投递沿用 canonical 撤回检查，不让墓碑内容复活。
源帖撤回操作者也应在本地按已知根 ID 关闭线程，因为原帖响应不是第二个根 ACK。

根卡片没有第二次 APNs；频道原帖按既有频道标题/头像规则通知订阅者。
评论走普通群通知，遵循讨论群免打扰，增加 discussion_root_message_id 用于跳转线程。
标题/头像仍为群身份，消息预览与图片配文规则不变，不发送引用原文或图片二进制。
已进入 WebSocket/APNs 网络的内容不能物理撤销，客户端仍负责墓碑优先合并。

## 请求示例

下面仅为 Protobuf JSON 表示，实际仍是二进制 WSS，不新增 HTTP API。
频道9001、群9002、公告7001、根7002、评论7003：

```json
{"jwt":"<jwt>","channel_request":{"request_id":"bind-1","set_discussion_group":{"channel_id":"9001","group_id":"9002"}}}
{"channel_response":{"request_id":"bind-1","operation":"set_discussion_group","result":"CHANNEL_OK","channel":{"channel_id":"9001","discussion_group_id":"9002"}}}

{"jwt":"<jwt>","get_discussion":{"request_id":"thread-1","channel_id":"9001","post_message_id":"7001"}}
{"channel_response":{"request_id":"thread-1","operation":"get_discussion","result":"CHANNEL_OK","discussion":{"channel_id":"9001","post_message_id":"7001","group_id":"9002","root_message_id":"7002","joined":true,"can_comment":true,"closed":false,"reply_count":"0","post":{"message_id":"7001","content":"公告","msg_type":"text","discussion_root_message_id":"7002"}}}}

{"jwt":"<jwt>","post":{"to_id":"9002","is_group":true,"msg_type":"text","msg":"一条评论","discussion_root_message_id":"7002","reply_to_message_id":"7002","client_message_id":"comment-001"}}
{"post_ack_rsp":{"message_id":"7003","client_message_id":"comment-001","timestamp":"2026-10-02T03:00:00Z"}}
{"post":{"from_id":"1002","to_id":"9002","is_group":true,"msg_type":"text","msg":"一条评论","message_id":"7003","timestamp":"2026-10-02T03:00:00Z","discussion_root_message_id":"7002","reply_to_message_id":"7002"}}

{"jwt":"<jwt>","query_discussion_replies":{"request_id":"page-1","root_message_id":"7002","page_size":50}}
{"channel_response":{"request_id":"page-1","operation":"query_discussion_replies","result":"CHANNEL_OK","posts":[{"message_id":"7003","author_user_id":"1002","content":"一条评论","msg_type":"text","discussion_root_message_id":"7002","reply_to_message_id":"7002"}],"has_more":false,"next_before_message_id":"7003"}}
```

## v9 迁移和部署

v9 为 channel_settings 增加 nullable discussion_group_id，为 messages 增加
discussion_root_message_id、source_channel_message_id（默认0）和
discussion_closed（默认false，仅根使用）。新增绑定唯一、根来源唯一和评论分页索引。
没有新表、外键、正文复制或旧数据删除；历史v1–v8不改写。
迁移在既有锁和逐版本事务中执行，IF NOT EXISTS 支持重试，失败不会登记完成版本。

```bash
make -C proto
cd services
docker compose build db_migrate
docker compose run --rm --no-deps db_migrate
./rebuild_docker_compose.sh --no-sudo storage friend push
./rebuild_docker_compose.sh --no-sudo df-all
```

K8s 活动 migration kustomization 使用 schema-v9 Job，先构建/发布固定镜像，
等待 betterfly-db-migrate-v9 成功，再 rollout。所有副本升级后才能开放客户端入口。
旧客户端新增字段默认0、仍可用原有聊天；不会展示讨论入口/引用卡片。
混合版本仅保证旧功能，不保证新字段透传或讨论权限；不能提前启用讨论功能。
回滚顺序先DF再业务服务，保留增量列。不要回滚到旧代码后继续暴露新讨论入口。

不包含自动审批、历史公告补发、群事件广播、评论编辑或频道匿名身份功能。
测试入口与已执行范围见 [回归测试](REGRESSION_TESTING.md)。
