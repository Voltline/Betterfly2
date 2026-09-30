# 给客户端 Codex 的频道适配 Prompt

以下正文可作为完整任务发送给客户端 Codex。

---

请基于 Betterfly2 iOS 客户端当前实际代码，完成广播频道适配，不要破坏普通群、好友、
登录、通话、现有消息同步、撤回和 APNs。服务端已实现的模式为：管理员发布、订阅者阅读。
先读取服务端 `proto/channel/channel.proto`、增量 DF 协议及 `CHANNELS.md`，
再按客户端既有架构实现，不要自行假设字段号或新建不受支持的 HTTP API。
本机服务端工作区为 `/Users/voltline/Documents/Sources/Betterfly2`；本轮尚未提交/推送，
请同步这里的最新本地协议，而不是 GitHub 上的旧版。不要修改服务端工作区。

## 协议与模型

1. 将 channel/channel.proto 纳入现有 Swift Protobuf 生成脚本，并重新生成所有有 import
   变化的协议；只生成客户端需要的协议，不手改生成代码。
2. DF RequestMessage 新增 channel_request=39、query_channel_history=40，
   ResponseMessage 新增 channel_response=23。每次请求携带当前 JWT。
3. ChannelRequest 含 request_id 和一个管理操作 oneof；用 request_id 关联异步响应，
   不要拿它当消息去重键。服务端操作者来自认证连接，不传操作者 ID。
4. ChannelInfo 包含 channel_id/name/description/avatar_hash/username/visibility/
   owner_user_id/subscriber_count/subscribed/my_role/update_time。
   owner/admin 可发帖，member 只读，空 role 是公开预览者。
5. GroupInfo.is_channel=5、JoinedGroupInfo.is_channel=6，新字段默认 false。
   同一频道也会出现在旧已加入群列表中，请按 ID 合并为单一频道会话，不能重复列出。
6. 保留未识别字段/响应的兼容处理。旧服务不认识新增请求，客户端应超时提示不支持，
   不无限自动重试；入口只在已完成服务端升级的环境启用。

## 页面与操作

提供频道列表、公开搜索、公开频道预览、订阅/退订、频道详情、创建和管理。
频道是单向信息流，不是普通聊天页；只读订阅者隐藏输入框，owner/admin 显示发布入口。
内容以频道名称/头像呈现，可保留作者 ID 供管理，但不默认显示普通群的“作者：正文”。

ChannelRequest 操作：create/update/get/search/subscribe/unsubscribe/list_subscribed/
list_members/delete。ChannelResponse.operation 对应 create_channel/update_channel/
get_channel/search_channels/subscribe_channel/unsubscribe_channel/list_subscribed_channels/
list_channel_members/delete_channel。

创建用正数 int64 channel_id，与普通群共用 ID 命名空间，使用当前客户端已有唯一群 ID
生成策略，不截断到 Int32；创建重试保持 ID。name Trim 后 1–100 Unicode code points，
description ≤1000，username 可空；非空 5–32 ASCII 字符，首字母，后续字母/数字/下划线，
服务端统一去首个 @ 和转小写；avatar_hash 沿用文件上传流程。
visibility PUBLIC=0、PRIVATE=1，创建时显式选择。
UpdateChannel 的资料字段是 optional PATCH，不填保留，显式空 description/avatar_hash/
username 清空；空 name 不合法。管理员可改资料，只有 owner 可改公开/私有。

search/list_subscribed 按 channel_id 升序，list_members 按 user_id 升序；
page_size 默认50、最大100，has_more 时用 next_cursor_id 继续。名单仅 owner/admin 可见。
错误码 CHANNEL_OK=0、NOT_FOUND=1、INVALID_ARGUMENT=2、FORBIDDEN=3、ALREADY_EXISTS=4、
INVALID_STATE=5、SERVICE_ERROR=10；私有频道对未订阅者返回 NOT_FOUND，不能当成网络故障。
创建 ALREADY_EXISTS 时查询原 ID 确认，不重新换 ID 重复创建。

私有频道仅成员能看信息与历史。邀请按现有 InviteGroupMember、QueryGroupInvitations、
ResolveGroupInvitation 实现，target_group_id=channel_id；接收者必须显式同意，申请7天有效。
邀请页要区分频道和群，接受后刷新频道列表并拉完整历史。
设置管理员、移除成员、转让使用现有 UpdateGroupMemberRole、KickGroupMember、
TransferGroupOwner，传频道 ID。权限沿用群主/管理员规则。
owner 退订会得到 INVALID_STATE，需先转让或明确删除频道；不要展示假成功。
delete 仅 owner 可用并二次确认，成员退出但服务端保留历史记录，不能视为销毁整个账号。

## 消息、历史与撤回

发布仍用现有 Post：is_group=true、to_id=channel_id、msg=内容或媒体 hash、
msg_type=既有类型、real_file_name=原文件名、client_message_id=稳定重试ID。
不新增独立发帖 API。以 PostAckRsp 回填 canonical message_id，原消息 ACK/幂等逻辑不变。
收到 Warn 说明发帖被拒绝，不应永远显示发送中；刷新权限，不继续盲目重发。
服务端仍保留真实 Post.from_id，不要据此误渲染为普通群气泡。

QueryChannelHistory(request_id,channel_id,page_size,before_message_id) 用于首次订阅、
初次打开及向上加载：before_message_id=0 取最新页，之后使用 next_before_message_id。
响应 ChannelResponse.operation=query_channel_history、posts、has_more，最新优先。
ChannelPost 有 message_id/author_user_id/content/msg_type/real_file_name/timestamp/
is_recalled/recalled_at/recalled_by。历史排序存储和 UI 展示要一致，合并到本地时按 message_id
去重，与 Post 实时消息、PostAck、QuerySyncMessages 共享同一消息表和撤回状态。

不能只用全局同步游标获取新频道历史；它可能比历史帖更新，必须单独 bootstrap 历史接口。
当前订阅的频道会进入现有全局同步，且不按入订阅时间截断。
不要用历史接口回退全局游标。正常新增消息与撤回同步继续使用两个独立现有游标。
owner/admin 调用 RecallMessage 可以删频道内任何作者/任何时间的帖；普通群/私聊仍是原规则。
撤回墓碑必须覆盖晚到的历史或实时消息，不恢复已经删除的正文/附件名。
私人频道退订或被踢后，不再尝试拿旧作者身份读取历史；公开频道仍可预览。

## APNs 与 NSE

频道通知沿用普通 APNs MESSAGE：conversation_id=channel_id、is_group=true，
额外 is_channel=true，title=频道名称，body=正文预览，无发布者前缀；
conversation_name/group_name/头像标识均为频道资料。
NSE 应优先使用频道名称和 conversation_avatar/avatar，避免 INSendMessageIntent
重新把标题/头像变成真实发布者；is_channel 缺失时兼容现有普通群/私聊逻辑。
通知点击落到频道；本地缺少频道元信息时通过 GetChannel 或已有带 is_channel 的 QueryGroup
识别，不能仅依赖 from_id。头像仍由客户端扩展转换，不是服务端替换 App 图标。
频道撤回通知仍是 message_recalled，也含 is_channel，沿用现有删除 message_id 通知逻辑。

## 验证

完成 Swift PB 往返、optional 字段 presence、普通群默认 is_channel=false、历史分页/去重、
撤回乱序、只读 UI、权限变更刷新、创建重试、私有邀请同意、退订失败、APNs/NSE 测试。
双账号实机验证管理员发布、订阅者实时接收与离线同步，公开预览、私有隐藏、媒体、
入订阅前历史和频道头像通知；正常群/好友/通话必须回归。
不要声称实机或 APNs 验证通过，除非实际执行；最终说明测试结果与仍缺少的项。
