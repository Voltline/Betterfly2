# 群语音与群视频

## 边界与数据流

一对一仍使用原有 SDP / ICE 与 Coturn；多人会议使用可自托管的 LiveKit SFU，
不是客户端之间的全连接 mesh。CallService 只控制权限和房间状态，不接触音视频。

```text
已登录客户端 -> /ws + JWT -> DataForwarding -> call-service Kafka -> CallService
CallService -> PostgreSQL 查询当前普通群成员/角色
CallService -> Redis Lua 原子提交房间、busy 索引、操作账本和现有 call:outbox
现有 relay -> DF topic / push-service-voip / LiveKit RoomService
客户端 -> LiveKit SDK -> SFU -> 其他参与者
LiveKit 签名回调 -> CallService -> Redis 状态与事件
```

不新增数据库表、迁移或 Kafka topic。开启多人能力后 CallService 需要现有 PostgreSQL
schema 的只读业务权限。频道不是会议群；频道关联的普通讨论群可以开会。
默认最多 16 人，可设置 2–64 人；这不是经过容量测试的性能承诺。

## 协议

沿用 `RequestMessage.call_request` 与 `ResponseMessage.call_event`。
身份来自已认证连接，群请求不接受客户端指定操作者 ID。所有群请求必须提供
`ClientRequest.request_id`，非空且最多 128 bytes。不同操作必须使用不同 ID。

| 新增 ClientRequest oneof | 字段号 | 参数 |
| --- | --- | --- |
| create_group_call | 8 | group_id、AUDIO/VIDEO、可选 invite_user_ids |
| get_group_call | 9 | group_id 或 call_id，二选一 |
| join_group_call | 10 | call_id |
| leave_group_call | 11 | call_id |
| end_group_call | 12 | call_id |
| remove_group_call_participant | 13 | call_id、target_user_id |

`request_id` 为字段 20。CallEvent 尾部字段 13–19 分别为 group_call、sfu_url、
join_token、token_expires_at、request_id、group_calls_available、group_call_max_participants。
新增事件枚举 9–14：GROUP_CALL_CREATED、UPDATED、JOINED、LEFT、ENDED、INVITATION。
新增错误 ROOM_FULL=7、MEDIA_UNAVAILABLE=8，所有旧字段和枚举保持不变。

`GroupCallInfo` 包含 call_id、group_id、creator_user_id、call_type、state、room_name、
max_participants、participants、created_at、expires_at、revision。
参与者包含 user_id、服务端生成的 LiveKit identity 和 connected。
connected=false 表示已预留席位但尚未收到媒体连接回调，不表示已经建立媒体流。

## 操作规则

- 每个普通群最多一个 ACTIVE 房间。当前群成员均可创建、查询和加入。
- 创建返回 CREATED，但不自动加入。创建者随后发送 Join，邀请对象也单独 Join。
- 创建可邀请最多“人数上限减一”个不重复的其他当前成员；不自动向全群发送来电。
- Join 返回 JOINED，只有请求者获得 sfu_url、短期 join_token 和到期时间。
  token 绑定唯一房间和用户 admission identity，不含管理权限、不允许 data publish 或屏幕共享。
- AUDIO 只允许 microphone；VIDEO 允许 microphone 和 camera。静音、摄像头和轨道订阅交给 SDK。
- Leave 只退出当前会议；最后一人退出时结束房间。创建者退出但还有其他人时会议继续。
- 创建者或群 owner/admin 可以 End。只有群 owner/admin 可以移除他人；
  admin 不能移除 owner/admin，任何人不能通过 Remove 移除自己（应使用 Leave）。
- 同一用户不能同时参加群会议和一对一通话；复用现有 busy key，不提前释放其他连接的占用。
- 未连接席位在 75 秒后释放；新建空房间也只有 75 秒加入窗口。
  最大时长沿用 CALL_ACTIVE_TTL_SECONDS（默认 6 小时），由既有扫尾任务结束。
- 媒体断线通过 participant_left 释放席位；新连接的 SID 防止旧 leave 回调移除重连。
  显式重新 Join 会更新 admission identity，原子排队移除旧身份；过期 joined/left 回调不影响新席位。
  临时网络自动恢复优先交给 SDK；明确断开后用新 request_id 重新 Join，不假设 identity 永久不变。
- 房间状态事件携带 revision，客户端只合并较新的快照；同场会议的 ENDED/LEFT 后停止媒体。

## 实时状态与邀请边界

创建、Join、Leave、Remove、End、SFU 连接变化和超时清理均向普通群的当前在线成员
广播最新 `group_call`，不要求接收者已经参会。操作者保留带 `request_id` 的原操作响应，
其他成员收到 `GROUP_CALL_UPDATED`；房间结束时收到 `GROUP_CALL_ENDED`。
状态广播的 `request_id` 为空，与操作响应携带相同的最新 revision。
操作者已经通过响应获得快照时，不再附加相同状态广播。

- `GROUP_CALL_JOINED` 与 `sfu_url`、`join_token`、`token_expires_at` 只发送给实际 Join 请求者。
- `GROUP_CALL_LEFT` 只发送给主动退出者或被移除者。Remove 操作者收到带请求 ID 的
  UPDATED（若房间结束则 ENDED），不能将其当作操作者自己退出。
- 最后一人主动 Leave 保留带请求 ID 的 LEFT，并额外收到空请求 ID 的 ENDED；
  被移除者可收到定向 LEFT 和全群状态广播。这些是不同语义事件，不是重复状态广播。
- SFU 回调和扫尾任务没有操作者，向所有当前在线成员发送一次最新状态。
- 状态事件不生成普通 Post、CallKit 来电或 APNs；只有明确指定的 `invite_user_ids`
  收到 INVITATION / VoIP Push。收到状态广播不能自动 Join、获取 token 或激活媒体。

客户端不需要每 30 秒轮询：在群内会议提示中实时合并状态；进入群聊或 WebSocket
重连后仍调用 Get 补查。快照按 `call_id + revision` 合并，旧会议的晚到 ENDED
不能清除同群新会议。Join 的相关响应即使 revision 已经见过，也必须单独处理其凭据；
不要因快照去重而丢弃请求响应。

广播目标按当前成员关系与有效 WebSocket route lease 查询，离线成员不排队补推。
路由查询失败会记录脱敏告警并跳过该用户，不因此回滚会议操作；后续通过 Get 恢复。
成员关系数据库查询失败属于可重试错误，不能假装成功提交。
已生成的状态事件仍与房间状态原子进入现有 Redis Stream，发布失败由既有 relay 重试，
不回滚已提交状态。本次没有新增协议、表、Topic、推送类型或客户端轮询配置。

## 幂等、邀请与可靠性

变更操作以 user_id + request_id 去重并与状态/事件原子落入 Redis。
重复同一个 ID 不重新变更或生成第二组逻辑事件，也不会重新签发凭据或重发原响应。
网络不确定时用 Get 查询；需要新凭据时发送新 ID 的 Join。不同正文复用同一个 ID 采用“首次成功操作为准”。
Get 不去重，可以重复获取快照。Kafka / Redis Stream 仍为 at-least-once，客户端必须按
request_id 和 call_id/revision 合并，不能假设物理消息只到达一次。

邀请复用可选 VoIP Push（required=false），失败不会结束整场会议，也不会自动让房间 active/connected。
APNs 保持 event=incoming_call，追加 is_group_call=true、group_id、group_name；
保留 call_id/call_uuid、caller_user_id、call_type、has_video、expires_at，不传 SFU token。
客户端仍须立即向 CallKit 报告 PushKit 来电，再登录、Get/Join；不能调用一对一 Resume/Accept。
不接受邀请时在客户端结束邀请 UI，不发送 Leave 来关闭未参加的会议。
保留 event 名可避免旧 PushKit 客户端忽略未知事件而不报告 CallKit。
旧客户端没有多人能力，若被邀请仍会走一对一 Resume 并收到 CALL_NOT_FOUND 后结束来电；
服务端当前不保存每设备的群通话能力，邀请名单应只选择已完成多人适配的用户。

媒体结束/移除指令持久化到同一个 Redis Stream，由既有 relay 重试。
LiveKit 不可用时业务状态可以先变更，但媒体实际断开要等待 API 恢复。
自托管 LiveKit 无即时 token 撤销：短期旧 token 可能在到期前尝试重新连接，
签名 participant_joined 回调会移除无有效 admission/群权限的连接；存在异步回调窗口，
不能宣称零窗口撤销。移除不是永久禁入，仍在群内的用户可以重新申请加入。
控制面停机跨过房间元数据 TTL 后，仍根据保留的截止时间索引原子排队 SFU 清理。
Redis 整体丢失（含索引/Stream）不能自动恢复，需运维清理遗留 SFU 房间。
SFU 重启不保证恢复原媒体会话；若 room SID 改变，旧凭据不得绕过当前 admission，
应结束旧会议后创建新会议。回调不能仅凭相同 room name 接纳连接。
群成员被踢或退群不自动扫描已有 SFU 连接；重新 Join 与媒体 joined 回调会检查最新成员关系。
需要立即驱逐正在开会的成员时，应先执行 RemoveGroupCallParticipant，再修改群关系；
退群客户端应先断开媒体和 Leave，不能把群关系删除等同于 SFU 已断开。

## 配置与部署

| 配置 | 默认 | 用途 |
| --- | --- | --- |
| LIVEKIT_API_URL | 空 | 服务端 RoomService HTTP(S) 地址 |
| LIVEKIT_PUBLIC_URL | 空 | 客户端可访问 WS(S) 地址 |
| LIVEKIT_API_KEY | 空 | 与 SFU 完全一致的 API key |
| LIVEKIT_API_SECRET | 空 | 至少 32 bytes 的 secret |
| LIVEKIT_NODE_IP | 空 | Compose SFU 对外通告的媒体 IP；本地测试填宿主机局域网 IP |
| LIVEKIT_USE_EXTERNAL_IP | true | Compose SFU 是否通过 STUN 自动发现公网 IP；显式 node_ip 时设 false |
| CALL_GROUP_MAX_PARTICIPANTS | 16 | 2–64 人 |

四个 LiveKit 配置全部为空时不启用多人能力，保留旧的一对一部署。部分配置缺失直接终止启动。
本地 `.env` 示例（密钥自行用 `openssl rand -hex 32` 生成）：

```env
LIVEKIT_API_URL=http://livekit:7880
LIVEKIT_PUBLIC_URL=ws://<客户端可访问的本机IP>:7880
LIVEKIT_API_KEY=betterfly-local
LIVEKIT_API_SECRET=<随机密钥>
LIVEKIT_NODE_IP=<客户端可访问的本机IP>
LIVEKIT_USE_EXTERNAL_IP=false
CALL_GROUP_MAX_PARTICIPANTS=16
```

```bash
cd services
# 标准全量测试入口，自动开启 group-calls
./build_docker_compose.sh
# 不需要监控、Kafka UI 和第二个 DF 时使用较轻的部署
./deploy_docker_compose.sh standard --enable group-calls --proto
# 后续只重建业务容器
./rebuild_docker_compose.sh --proto call push df
```

`group-calls` 是额外可选 profile；`build_docker_compose.sh` 默认启用，
`deploy_docker_compose.sh standard/full` 不会自动追加该 profile。
`.env` 被 Git 忽略；密钥只生成一次并保留，构建脚本不会在重建时轮换密钥。
局域网测试客户端需要能访问宿主机 IP，手机应连接同一 Wi-Fi 并允许应用访问本地网络。
宿主机 IP 改变时同步修改 LIVEKIT_PUBLIC_URL 与 LIVEKIT_NODE_IP，再重建 CallService 和 LiveKit：
`./rebuild_docker_compose.sh call sfu`。不要填写 Docker 容器 IP。
LiveKit 镜像固定 v1.13.7。Compose 使用 UDP mux 7882/udp、TCP fallback 7881/tcp、
信令 7880/tcp；防火墙/云安全组需同时允许。生产将 7880 放在可信 TLS 反向代理后，
LIVEKIT_PUBLIC_URL 使用 wss:// 域名，代理支持 WebSocket，不能发布 localhost。
公网部署可将 LIVEKIT_NODE_IP 改为可达公网 IP 并保持 LIVEKIT_USE_EXTERNAL_IP=false，
或清空 node_ip 并设为 true 自动发现；不可直接复用局域网 IP。
Docker Desktop 的本地媒体/NAT 表现不能代替公网服务器验收。
一对一 Coturn 端口保持不变；LiveKit 的 TURN/TLS 配置是独立能力，本模板未开启，
仅允许 443 出站的严苛网络需按官方部署指南额外配置，不能保证默认 UDP/TCP 方案覆盖所有网络。

回调 POST /call/livekit/webhook 只面向 SFU。校验 HS256、issuer、有效期和原始 body SHA256；
限制 64 KiB，不接受普通客户端凭据。失败返回非 2xx 供 SFU 重试，不记录 token/body。

Kubernetes：先创建 betterfly2-livekit Secret，包含以上四个 LIVEKIT_* 字段，
其中 API URL 为 http://livekit:7880。再 apply deploy/k8s/livekit.yaml，重建 Call/DF/Push 镜像并 rollout。
模板是单节点 hostNetwork SFU；节点需要公网可达 IP 与对应端口，不适合在多个节点上随意增加副本。
多人 Call Pod 的 PostgreSQL 预算为 6 × 副本数（默认两副本共 12），不新增迁移。
先更新 Call/Push，再更新 DF，最后启用新版客户端；旧客户端忽略新增字段，不具备多人 SDK/UI。

## 请求与响应示例

以下为便于阅读的字段结构，传输仍为二进制 Protobuf。

```json
{"jwt":"<JWT>","call_request":{"request_id":"create-uuid","create_group_call":{"group_id":10,"call_type":"VIDEO","invite_user_ids":[2,3]}}}
{"call_event":{"event_type":"GROUP_CALL_CREATED","request_id":"create-uuid","call_id":"<32位hex>","group_call":{"group_id":10,"state":"ACTIVE","revision":1,"participants":[]}}}
{"jwt":"<JWT>","call_request":{"request_id":"join-uuid","join_group_call":{"call_id":"<32位hex>"}}}
{"call_event":{"event_type":"GROUP_CALL_JOINED","request_id":"join-uuid","call_id":"<32位hex>","sfu_url":"wss://media.example.com","join_token":"<短期JWT>","token_expires_at":"<RFC3339Nano>","group_call":{"group_id":10,"state":"ACTIVE","revision":2,"participants":[{"user_id":1,"identity":"<opaque>","connected":false}]}}}
```

Swift 需重新生成 call/call_interface.proto 及其导入者 data_forwarding/data_forwarding_interface.proto。
push/push_interface.proto 是内部协议，若客户端生成脚本会生成全部文件，保持全量生成即可。

官方参考：[RoomService API](https://docs.livekit.io/reference/other/roomservice-api/)、
[防火墙与端口](https://docs.livekit.io/transport/self-hosting/ports-firewall/)、
[签名回调](https://docs.livekit.io/intro/basics/rooms-participants-tracks/webhooks-events/)。
