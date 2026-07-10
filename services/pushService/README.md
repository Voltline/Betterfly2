# Push Service

PushService 是 Betterfly2 的统一推送控制面。当前一期实现 iOS PushKit VoIP Push，用于在 App 被系统终止或没有活跃 WebSocket 时唤醒客户端并通过 CallKit 展示来电。

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

PushService 将两类 token 分开保存，并分别请求 `api.sandbox.push.apple.com` 和 `api.push.apple.com`。两种请求都使用 topic `com.Voltline.Betterfly2.voip`、`apns-push-type: voip` 和 `apns-priority: 10`。同一枚 `.p8` provider key 可用于两个 endpoint。

## 健康检查

- `GET /health`: 进程存活。
- `GET /ready`: PostgreSQL 可用并且 APNs 私钥配置有效。

默认 HTTP 端口为 `8086`，只需内网开放。服务器必须能够访问 APNs `443/tcp`，也可以按网络策略使用 Apple 支持的 `2197/tcp`。
