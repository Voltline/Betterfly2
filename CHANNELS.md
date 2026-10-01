# 广播频道

频道是管理员发布、订阅者阅读的会话，不是多人自由聊天的群。
本实现复用 Friend 的群关系与角色、Storage 消息、Kafka Inbox/Outbox、
DataForwarding 跨 Pod 投递和 Push 的每设备账本。不新增服务、Topic 或任务循环。

## 能力与权限

| 能力 | 规则 |
| --- | --- |
| 创建 | 登录用户提供与普通群共用命名空间的正数 `channel_id` |
| 公开发现 | 按名称或 username 搜索；登录用户可预览公开频道和全部历史 |
| 订阅 | 公开频道直接订阅，重复订阅不增加人数、不重置入订阅时间 |
| 私有访问 | 仅当前成员可查询信息或帖子，其他用户统一收到 NOT_FOUND |
| 私有邀请 | 复用 InviteGroupMember、QueryGroupInvitations、ResolveGroupInvitation；接收者明确同意，7 天有效 |
| 发帖 | 仅 owner/admin；DataForwarding 与 Storage 均检查权限 |
| 编辑资料 | owner/admin；公开/私有切换仅 owner |
| 订阅者名单 | 仅 owner/admin，可分页；普通订阅者不能列出其他人 |
| 管理成员 | 复用现有设置管理员、踢人、转让群主 API 和权限规则 |
| 删除帖子 | 复用 RecallMessage；频道 owner/admin 可删除任意时间的帖子，保留撤回墓碑 |
| 退订 | 物理删除成员关系；owner 必须先转让所有权或显式删除频道 |
| 删除频道 | 仅 owner；注销成员、释放 username、取消待审批申请；保留历史消息与频道类型墓碑 |

旧普通群的加入审批、发言、群主退群时自动转让、两分钟撤回窗口均保留。
频道复用群 ID，公开频道的旧版入群申请仍可由管理员审批，私有频道不接受新的主动入群申请。
删除的频道不能经旧版 CreateGroup 重新激活。

## 客户端接口

定义位于 `proto/channel/channel.proto`。
`RequestMessage.channel_request = 39` 和 `query_channel_history = 40`，
`ResponseMessage.channel_response = 23`，均为增量字段。
每次请求携带现有 JWT，操作者来自认证连接，不存在客户端填写的操作者字段。

### 管理和发现

所有管理请求包在 `ChannelRequest`，可选 `request_id` 最多 128 bytes，用于关联响应，
不是鉴权凭据或幂等键。

| ChannelRequest.payload | 内容 | response.operation |
| --- | --- | --- |
| create | channel_id、name、description、username、visibility、avatar_hash | create_channel |
| update | channel_id 及 optional 资料字段 | update_channel |
| get | channel_id 或 username 二选一 | get_channel |
| search | query、page_size、cursor_channel_id | search_channels |
| subscribe | channel_id | subscribe_channel |
| unsubscribe | channel_id | unsubscribe_channel |
| list_subscribed | page_size、cursor_channel_id | list_subscribed_channels |
| list_members | channel_id、page_size、cursor_user_id | list_channel_members |
| delete | channel_id | delete_channel |

名称 Trim 后 1–100 Unicode code points，描述最多 1000，avatar_hash 最多 255。
username 可空；非空会去除首个 `@`、转小写，必须是 5–32 个 ASCII 字符，
首字符字母，其余字母、数字或下划线，全局唯一。私有频道 username 也不公开可见。
`Visibility.PUBLIC=0`、`PRIVATE=1`。搜索按名称/username 的字面子串匹配，
`%`、`_` 不作为用户可控通配符。

UpdateChannel 是 PATCH：未设置的 optional 字段保留；显式空 description、
avatar_hash、username 用于清空；显式空 name 不合法；空 PATCH 不合法。

列表默认 50 条，最多 100 条，按 ID 升序；`has_more` 与 `next_cursor_id` 分页。
成员列表的 next_cursor_id 是 user ID，其余是 channel ID；负数页长/游标被拒绝。
`ChannelInfo` 提供名字、描述、头像、username、visibility、owner_user_id、
subscriber_count、subscribed、my_role 和 update_time，人数包含管理员与 owner。
`my_role=member` 表示只读订阅者；空串表示公开预览者。

响应 result：OK=0、NOT_FOUND=1、INVALID_ARGUMENT=2、FORBIDDEN=3、
ALREADY_EXISTS=4、INVALID_STATE=5、SERVICE_ERROR=10。
基础设施异常仍沿用有限 Kafka 重试/DLQ，不缓存为已完成的业务拒绝；
因此断线或服务故障仍需要客户端请求超时与重试。
创建重试必须保持 channel_id；另一个客户端请求若得到 ALREADY_EXISTS，
应查询该 ID 并验证归属，不应自动更换 ID 重复创建。

### 发帖、同步和推送

沿用 `Post{is_group:true,to_id:channel_id,msg:内容,msg_type:类型,
client_message_id:稳定客户端ID,real_file_name:原始文件名}`。
媒体上传仍走已有 Storage HTTP，`msg` 为 file hash；文字/链接沿用原有语义。
成功获得 PostAckRsp 的 canonical message_id；订阅者通过现有 Post 收到实时帖子。
权限拒绝返回现有 Warn，不产生 ACK 或入库记录。

`QueryChannelHistory` 返回完整频道历史，最新优先，默认/最大页长 50/100，
`before_message_id=0` 获取最近页，之后使用 `next_before_message_id` 向前翻页。
返回 ChannelPost 含 message_id、author_user_id、content、msg_type、real_file_name、
timestamp 及撤回字段；墓碑不下发正文或文件名。

首次订阅、频道页面首次打开必须用独立历史接口补全，再与实时 Post 合并。
不能只靠全局 QuerySyncMessages：它仍受客户端的全局时间游标限制。
普通增量同步包含当前订阅的频道和自己的帖子；频道不按 JoinedAt 截断历史。
同步撤回继续使用现有独立 recall 游标，必须去重 message_id 并保留墓碑。

兼容的 GroupInfo/JoinedGroupInfo 尾部新增 `is_channel`；默认 false。
新客户端应将频道与普通群区分显示，频道帖子显示频道身份而非普通群聊天气泡。
协议仍保留真实作者 ID，用于审计/管理，不提供匿名作者隐私承诺。

APNs 标题为频道名、正文为帖子预览，不加发布者姓名前缀，头像为频道头像。
沿用 conversation_id/is_group/group_name/conversation_name/conversation_avatar，
频道额外带 `is_channel:true`，包括撤回通知；普通群 payload 不增加这个字段。
客户端 NSE 使用频道名称/头像构造通信通知；服务端不能单独替换 iOS App 图标。
Push worker 在领取任务时重新检查有效频道与订阅关系，退订后的排队任务不再发送。
已进入 APNs 网络请求的通知无法撤销，仍存在正常的并发退订窗口。

## 部署与兼容

需要 v6 迁移：只新增 `channel_settings` 表与 username/公开发现索引；
不修改既有 Group/GroupMember/Message 字段，不重写 v1–v5。

```bash
make -C proto
cd services
docker compose build db_migrate
docker compose run --rm --no-deps db_migrate
./rebuild_docker_compose.sh --no-sudo storage friend push
./rebuild_docker_compose.sh --no-sudo df-all
```

先迁移，再部署 Friend/Storage/Push，最后所有 DataForwarding；
所有副本完成升级后才开启新客户端频道入口。回滚先 DF 后业务服务，保留新增表。
旧客户端和旧序列化字段仍可解析；旧客户端忽略 is_channel，可能将频道当普通群显示，
但旧 DF 的群发言路径仍受到新版 Storage 的只读检查。
协议生成文件按仓库现有方式生成，不手工编辑 pb.go。
频道最初使用 schema-v6 Job；当前活动 migration manifest 为 schema-v7 Job
（追加图片配文列），需构建相应固定镜像并等待成功后再 rollout。
schema-v5/v6 Job 留作历史文件但不在活动 kustomization 中。

## 本轮边界

不包含评论/讨论群联动、反应、阅读计数、订阅者静音设置、邀请链接或内容审核后台。
不承诺百万订阅者扇出容量：实时投递仍复用现有按成员名单读取和分 Pod 扇出的路径，
历史/成员/发现查询有界分页，但发布扇出应按实际订阅规模单独压测。
Kafka/Outbox/APNs 保留至少一次边界；客户端必须依据 message_id 去重。
群/频道资料与成员变动不实时广播，刷新列表/详情时看到最新状态。

客户端改造任务见 `CLIENT_CHANNEL_PROMPT.md`；测试入口见 `REGRESSION_TESTING.md`。
