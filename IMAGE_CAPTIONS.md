# 单张图片与配文

## 协议和消息模型

继续使用 WSS 二进制 Protobuf 的 RequestMessage.post（字段 5），不新增发布 API。
msg_type=image，msg 是既有 Storage HTTP 上传/verify 流程得到的图片 SHA512 hash，
caption 是独立的完整配文；一张图片和配文只有一个 message_id、服务端时间和 ACK。
普通私聊、群聊和频道共用 messages 表。

| 协议消息 | 新增字段 | 字段号 | 源文件 |
| --- | --- | --- | --- |
| DF Post | caption | 10 | proto/data_forwarding/common.proto |
| DF MessageRsp | caption | 12 | proto/data_forwarding/response.proto |
| ChannelPost | caption | 10 | proto/channel/channel.proto |
| Storage StoreNewMessage | caption | 9 | proto/storage/request.proto |
| Storage StoreMsgRsp | caption | 12 | proto/storage/response.proto |
| Storage MessageRsp | caption | 12 | proto/storage/response.proto |

StorageResult 追加 INVALID_ARGUMENT=5，用于内部非法配文的业务拒绝；
DF 转为既有 Warn，不改变旧枚举值、请求 oneof 或 PostAckRsp。
GroupPostDelivery/GroupPostBatchDelivery 嵌套的仍然是 Post，
Push.MessagePushRequest 继续只携带 preview，不增加完整配文/附件字段。

链路：认证 Post -> DF 校验 -> Storage Request -> Inbox/业务写入/Outbox ->
StoreMsgRsp -> DF ACK + 实时 Post / 跨 Pod Post + Push preview。
QueryMessage、QuerySyncMessages 和 QueryChannelHistory 返回同一个数据库配文。
同步 SQL 的两侧 UNION 都包含 caption，消息游标/撤回游标及历史分页规则不变。

## 校验与幂等

- caption 为空时，消息存储和显示模型与旧图片一致；没有配文也不会拆成文本消息。
- 非空 caption 仅允许 image，不接受 gif/video/text 等消息携带配文。
- 必须是有效 UTF-8，不超过 4096 个 Unicode code points，同时不超过 16384 UTF-8 bytes。
- 不 Trim、不截断持久化原文；换行、Markdown、前后空格完整保存。
- DF 校验失败返回 Warn；Storage 独立校验失败返回 INVALID_ARGUMENT，不访问消息写入。
- 认证用户仍取自连接；频道仅 owner/admin 发帖，订阅者无发言权限。
- 图片上传、校验与下载 API 不变；本功能没有新增图片存在性规则或绕过原来的文件流程。
- client_message_id 和 ACK cache/副作用账本保持原语义。重试必须复用同一 ID。
- 同一发送者 + client_message_id 携带不同的合法 hash/caption 时采用“原记录优先”：
  不覆盖原文、不创建第二条消息，返回原 message_id 和服务端 timestamp。
  非法配文即使使用已有 ID 仍被拒绝。没有显式 client_message_id 的旧客户端
  继续使用已有确定性兜底，新增 caption 自然参与序列化；新客户端必须显式给稳定 ID。
- 重复插入 created=false 不再触发实时/APNs；Kafka 原结果重放受既有 effects/设备账本保护。
  保留 Kafka/Outbox 至少一次边界，不宣称物理 exactly-once。

## 撤回与缓存

RecallMessage 仍只有 message_id，撤回整条图片和配文，不支持分别撤回。
QueryMessage、普通同步、独立撤回同步和频道历史的 tombstone 同时清空
content（图片 hash）、caption、real_file_name，保留 ID、时间与撤回元数据。
已撤回记录的重复存储响应也遮蔽这三个字段，ACK 仍返回原 ID/时间。

图片 QueryMessage 命中 L1/L2 后回读数据库，避免旧版本缓存写入者丢失 caption，
或缓存删除失败后读到撤回前的图文。仍执行原来的集中鉴权。
图片实时投递在源 DF 和目标 DF 检查已提交 is_recalled；
已撤回/已不存在时抑制旧 Kafka 图片事件，数据库错误进入既有重试路径。
不新增 Redis key 或后台任务。

已经进入 WebSocket 写队列/网络的事件无法收回。若撤回在投递检查之后并发发生，
客户端仍必须以 message_id + tombstone 合并，不能用晚到 Post 覆盖已撤回状态。
APNs 继续使用既有撤回账本/替换通知标识；已在途 APNs 存在相同边界。

## APNs 摘要

有配文：`[图片] 配文摘要`。没有摘要：`[图片]`。
通知统一图片标记；无配文的图片消息本体/持久化行为不变。
摘要压平换行/控制字符、移除常见 Markdown 标记和内联链接地址，
复用既有 180 code point 截断再追加省略号的流程，不是完整 Markdown 渲染器。
正文原文只保存在消息字段，不向 APNs 发送完整 caption、图片二进制或附件下载字段。

频道 title 是频道名称，body 不带真实发布者前缀。
普通群仍使用群名称及发送者前缀，私聊使用发送者名称；
avatar/conversation_avatar/is_channel/message_recalled 语义不变。
Push 后台手工通知及旧请求无 preview 的 fallback 行为不改。
生产数据库 SQL logger 保持关闭，业务日志仅记录 ID、结果和安全错误，不记录 caption/JWT/token。

## v7 迁移和部署

本轮仅追加消息列，无新表：

```sql
ALTER TABLE messages ADD COLUMN IF NOT EXISTS caption text NOT NULL DEFAULT '';
```

v1-v6 不重写。v7 在现有迁移锁及事务下执行；只有成功才登记版本。
失败会回滚、重跑安全；旧记录自动得到空字符串，不改原 Content。
旧服务可继续忽略此列；不要为了回滚旧二进制删列。

```bash
make -C proto
cd services
docker compose build db_migrate
docker compose run --rm --no-deps db_migrate
./rebuild_docker_compose.sh --no-sudo storage push
./rebuild_docker_compose.sh --no-sudo df-all
```

如果同时重新构建其他业务服务，新 shared 也要求 schema >=7，必须先迁移。
Kubernetes 使用现有 migrations kustomization 内的 schema-v7 Job，
镜像 betterfly2/db-migrate:schema-v7 应替换为已发布固定 tag/digest。
先等待 Job 成功，再部署 Storage，最后全部 DF；Push wire 不变，可滚动升级。
完成所有 Storage/DF 副本升级后再启用客户端配文入口。
混合版本仍能解析旧/新 PB，但旧 Storage/DF 的字段转换可能丢弃 caption，
所以混合阶段不承诺新配文展示完整，不能提前启用。
旧客户端忽略新增 caption，仍可展示图片；无法显示配文是旧版本能力限制。

## iOS 适配 Prompt

```text
请适配 Betterfly2 单张图片+配文消息，不修改发送 API，不实现多图、编辑或图文块。
以最新服务端 /Users/voltline/Documents/Sources/Betterfly2/proto 为准重新生成 Swift PB。
直接变更源文件：
  data_forwarding/common.proto
  data_forwarding/response.proto
  channel/channel.proto
  storage/request.proto、storage/response.proto、storage/storage_interface.proto
若客户端没有生成 Storage 内部协议，则不必新增其生成目标。
继续按已有脚本生成包含这些文件的依赖图，不手改 pb.swift。
新字段：Post.caption=10，DF MessageRsp.caption=12，ChannelPost.caption=10。
生成产物通常为 common.pb.swift、response.pb.swift、channel.pb.swift，
以客户端生成脚本的实际目录/命名为准，不创建同名重复模型。

1. 图片先走原上传/verify，Post.msg_type=image，msg=已校验图片hash，
   caption=完整原文，client_message_id=稳定ID；不要复用real_file_name或JSON包装msg。
2. 单条本地消息追加caption并做兼容性迁移/默认空字符串；
   所有实时Post、QueryMessage、普通/撤回同步、频道历史转换都保存caption。
3. 渲染为同一气泡/频道帖子：图片下显示配文，支持换行及现有Markdown安全渲染。
   caption为空时保持旧图片UI，不为配文创建第二个本地message_id。
4. 校验4096 Unicode code points +16KiB UTF-8。
   Swift String.count是grapheme数量，不是code points，应使用unicodeScalars.count；
   bytes使用utf8.count；超限禁止发送，不静默截断。
5. ACK仍用client_message_id关联，仅更新canonical message_id/timestamp。
   重试复用同一个ID；已有ID不能作为修改caption的编辑API。
   同一ID不同合法内容服务端保留原记录，必要时重新QueryMessage收敛。
6. 撤回整条图文：本地清空hash/caption/filename；
   晚到Post/重复同步不能让已撤回消息复活，按message_id保持tombstone优先。
7. 频道owner/admin才能发布；普通订阅者无发布入口。历史bootstrap/双游标不变。
8. APNs只包含摘要，不需要新增图片附件下载；现有Notification Service Extension
   继续解析频道头像、is_channel和message_recalled。
9. 补PB往返、旧消息默认空caption、Unicode边界、重试去重、
   实时/同步/频道历史一致及撤回竞态测试，并跑iOS构建。
服务端迁移/所有Storage和DF副本部署完成前，不启用新发送能力。
```

## 完整消息示例

下面是 Protobuf 的 JSON 表示，实际传输仍为二进制 PB。
示例 hash 用 128 个 a 表示假定已上传并 verify 的 SHA512，需替换成实际 hash；
JWT 为占位符。频道 ID=9001，操作者=1001，message_id=7001。

发送 RequestMessage：

```json
{
  "jwt": "<有效JWT>",
  "post": {
    "from_id": "1001",
    "is_group": true,
    "to_id": "9001",
    "msg": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    "msg_type": "image",
    "client_message_id": "announcement-image-001",
    "caption": "# 维护公告\n今晚 **23:00** 开始维护。\n请提前保存数据。"
  }
}
```

发送者 ACK（客户端 timestamp 不决定服务端时间）：

```json
{"post_ack_rsp":{"message_id":"7001","client_message_id":"announcement-image-001","timestamp":"2026-10-01T03:00:00Z"}}
```

订阅者实时 ResponseMessage：

```json
{"post":{"from_id":"1001","is_group":true,"to_id":"9001","msg":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","msg_type":"image","timestamp":"2026-10-01T03:00:00Z","client_message_id":"announcement-image-001","message_id":"7001","caption":"# 维护公告\n今晚 **23:00** 开始维护。\n请提前保存数据。"}}
```

查询、同步及频道历史：

```json
{"jwt":"<有效JWT>","query_message":{"message_id":"7001"}}
{"message_rsp":{"message_id":"7001","from_user_id":"1001","to_user_id":"9001","content":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","msg_type":"image","is_group":true,"timestamp":"2026-10-01T03:00:00Z","caption":"# 维护公告\n今晚 **23:00** 开始维护。\n请提前保存数据。"}}

{"jwt":"<有效JWT>","query_sync_messages":{"to_user_id":"1002","timestamp":"2026-10-01T00:00:00Z","page_size":50,"include_recalled_changes":true}}
{"sync_msgs_rsp":{"msgs":[{"message_id":"7001","from_user_id":"1001","to_user_id":"9001","content":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","msg_type":"image","is_group":true,"timestamp":"2026-10-01T03:00:00Z","caption":"# 维护公告\n今晚 **23:00** 开始维护。\n请提前保存数据。"}],"has_more":false,"next_cursor_timestamp":"2026-10-01T03:00:00Z","next_cursor_message_id":"7001","recalled_msgs":[],"recalls_has_more":false,"next_recall_cursor_timestamp":"1970-01-01T00:00:00Z","next_recall_cursor_message_id":"0"}}

{"jwt":"<有效JWT>","query_channel_history":{"request_id":"history-1","channel_id":"9001","page_size":50}}
{"channel_response":{"request_id":"history-1","operation":"query_channel_history","result":"CHANNEL_OK","posts":[{"message_id":"7001","author_user_id":"1001","content":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","msg_type":"image","timestamp":"2026-10-01T03:00:00Z","caption":"# 维护公告\n今晚 **23:00** 开始维护。\n请提前保存数据。"}],"has_more":false,"next_before_message_id":"7001"}}
```

撤回、撤回后查询及非法配文：

```json
{"jwt":"<有效JWT>","recall_message":{"message_id":"7001"}}
{"message_recall_event":{"result":"MESSAGE_RECALL_OK","message_id":"7001","from_user_id":"1001","to_user_id":"9001","is_group":true,"operator_user_id":"1001","recalled_at":"2026-10-01T03:01:00Z"}}
{"message_rsp":{"message_id":"7001","from_user_id":"1001","to_user_id":"9001","content":"","caption":"","real_file_name":"","msg_type":"image","is_group":true,"timestamp":"2026-10-01T03:00:00Z","is_recalled":true,"recalled_at":"2026-10-01T03:01:00Z","recalled_by":"1001"}}
{"warn":{"warning_message":"图片配文无效：仅image支持配文，须为有效UTF-8且不超过4096字符及16KiB"}}
```

频道 APNs 的 alert 为
`{"title":"维护公告频道","body":"[图片] 维护公告 今晚 23:00 开始维护。 请提前保存数据。"}`；
同一通知的既有 avatar/is_channel/message_id 元数据继续保留。

## 验证入口

各受影响模块执行 go test ./...、go test -race ./...、go vet ./... 和 go build ./...。
协议使用 make -C proto，生成物不手改。
真实链路验收需要专用 Compose 已重建并完成 v7：

```bash
cd services/dataForwardingService
BETTERFLY_ACCEPTANCE=1 go test -race -run '^TestImageCaptionEndToEnd$' -count=1 -v -timeout 3m ./integration
```

该用例使用消息 hash fixture，检查跨 Pod 实时、查询、同步、频道历史、不同正文重试、
频道权限、整条撤回及撤回后的重复发送。真实文件上传/verify 由既有 HTTP 测试覆盖。
iOS/NSE 渲染及真实设备 APNs 必须由客户端适配后另行实测。
