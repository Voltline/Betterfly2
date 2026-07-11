# Push Service

PushService 是 Betterfly2 的统一推送控制面，支持 iOS PushKit VoIP Push 与普通 APNs 消息通知。两类 token 独立注册和存储，避免将普通 APNs device token 错用于 `.voip` topic。

## 数据流

```text
iOS PKPushRegistry token
  -> authenticated WebSocket push_request
  -> dataForwardingService
  -> Kafka push-service
  -> PostgreSQL push_device_tokens

offline incoming call
  -> callService creates RINGING session
  -> Kafka push-service
  -> APNs sandbox or production
  -> PushKit wakes app
  -> app reports CallKit incoming call immediately
  -> app reconnects WebSocket and sends resume_call(call_id)
  -> callService returns SDP offer and ICE servers
```

普通消息发送时，原始 DataForwarding Pod 会并行发布一条消息推送请求。PushService 使用应用 Bundle topic 向目标用户的所有普通 APNs token 发送 `alert` 通知；WebSocket 在线状态不会阻止推送，因此同一账号的其他离线设备仍能收到通知。客户端在前台时应通过 `UNUserNotificationCenterDelegate` 决定是否展示横幅。

普通通知只包含 `sender_user_id`、`conversation_id`、`is_group`、`message_type` 和 `sent_at`，不包含消息正文。客户端被唤醒后继续通过现有消息同步接口获取完整内容；私聊通知会遵循接收方好友关系中的 `is_notify` 设置。

APNs payload 只包含 `call_id`、`call_uuid`、`caller_user_id`、`call_type`、`has_video` 和过期时间，不包含 SDP。这样 payload 始终低于 Apple 对 VoIP Push 的 5 KB 限制，SDP 仍通过认证后的 Protobuf 链路传输。

每个来电都会尝试发送 VoIP Push，即使被叫当前有在线 WebSocket，以覆盖 DF Pod 突然退出的短暂路由窗口。客户端必须按 `call_id/call_uuid` 合并 WebSocket 与 PushKit 两条来源，避免重复创建 CallKit 会话。

## APNs 配置

默认标识:

```env
APNS_KEY_ID=C6D5695Q4Y
APNS_TEAM_ID=8R5Q4A3RC7
APNS_BUNDLE_ID=com.Voltline.Betterfly2
```

私钥不能提交到仓库。Docker Compose 开发环境推荐在被 Git 忽略的 `services/.env` 中配置宿主机只读挂载路径:

```env
APNS_PRIVATE_KEY_HOST_PATH=/path/to/AuthKey_C6D5695Q4Y.p8
```

也可以将 `.p8` 转为单行 Base64:

```bash
base64 < /path/to/AuthKey_C6D5695Q4Y.p8 | tr -d '\n'
```

```env
APNS_PRIVATE_KEY_BASE64=<上一步的输出>
```

容器内路径固定为 `/run/secrets/apns_key.p8`。Kubernetes 使用 `betterfly2-secret` 中的 `APNS_PRIVATE_KEY_BASE64`。

## 双环境

客户端注册 token 时必须发送真实环境:

- Debug/development entitlement 使用 `SANDBOX`。
- TestFlight/App Store/ad-hoc production entitlement 使用 `PRODUCTION`。

客户端通过认证后的 `push_request` 注册：

- `register_voip_token` / `unregister_voip_token`: PushKit token。
- `register_apns_token` / `unregister_apns_token`: `didRegisterForRemoteNotificationsWithDeviceToken` 返回的普通 APNs token。

PushService 分别请求 `api.sandbox.push.apple.com` 和 `api.push.apple.com`。VoIP 使用 topic `com.Voltline.Betterfly2.voip` 与 `apns-push-type: voip`；消息通知使用 topic `com.Voltline.Betterfly2` 与 `apns-push-type: alert`。两者优先级均为 10，同一枚 `.p8` provider key 可用于两个 endpoint。

## 健康检查

- `GET /health`: 进程存活。
- `GET /ready`: PostgreSQL 可用并且 APNs 私钥配置有效。

默认 HTTP 端口为 `8086`，只需内网开放。服务器必须能够访问 APNs `443/tcp`，也可以按网络策略使用 Apple 支持的 `2197/tcp`。

## 内网调试控制台

配置 `PUSH_ADMIN_TOKEN` 后访问 `http://hostname:8086/push/admin`。未配置时页面及全部管理 API 返回 `404`，不会默认暴露调试能力。

控制台支持：

- 按用户批量发送普通 APNs 通知，或按脱敏的 token 记录 ID 定向发送。
- 自定义标题、正文和最多 2 KB 的字符串字典；调试字典位于 payload 的 `debug_data`。
- 发送 sandbox/production 普通通知与 PushKit 通道测试。
- 查询普通 APNs/VoIP token、环境和有效状态，完整 token 永不返回浏览器。
- 持久化记录操作人、脱敏目标、APNs ID、成功数和失败原因。

管理 API 使用 `Authorization: Bearer <PUSH_ADMIN_TOKEN>` 或 `X-Admin-Token`。页面将 token 仅保存到当前标签页的 `sessionStorage`。可通过 `X-Admin-Operator` 标记操作人。

PushKit 调试请求只验证 APNs 与 PushKit 通道，不会创建 CallService session，因此不能替代完整通话测试。普通消息调试默认遵循私聊 `is_notify` 设置，页面可显式选择仅本次忽略。
