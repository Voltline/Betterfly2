# Call Service

CallService 是 Betterfly2 的一对一 WebRTC 语音/视频通话控制面。它负责通话状态和信令，不接触音视频媒体内容。

## 组件边界

```text
client WebSocket / PushKit wake-up
  -> dataForwardingService
  -> Kafka topic: call-service
  -> callService + Redis state machine
  -> destination DF pod topic
  -> peer client

client WebRTC media
  -> peer-to-peer when possible
  -> Coturn relay when direct connectivity fails
```

Redis 保存活跃通话、用户占用索引和响铃截止时间。状态转换使用事务或 Lua 原子执行，因此多个 CallService Pod 可以共享同一 Redis 和 Kafka consumer group 横向扩展。

被叫没有在线 WebSocket 时，CallService 会保留 `RINGING` 会话并请求 PushService 发送 VoIP Push。客户端唤醒并重新登录后使用 `resume_call(call_id)` 获取原始 SDP offer 和 ICE 配置。

## 环境变量

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `HTTP_PORT` | `8085` | 健康检查端口 |
| `REDIS_ADDR` | `localhost:6379` | Redis 地址 |
| `KAFKA_BROKER` | `localhost:9092` | Kafka broker，逗号分隔 |
| `KAFKA_CALL_TOPIC` | `call-service` | 通话请求 topic |
| `KAFKA_CONSUMER_GROUP` | `call-service-group` | 消费组 |
| `CALL_RING_TIMEOUT_SECONDS` | `45` | 响铃超时 |
| `CALL_ACTIVE_TTL_SECONDS` | `21600` | 活跃通话状态最大 TTL |
| `TURN_PUBLIC_HOST` | `localhost` | 自动生成 ICE URL 时使用的客户端可访问主机 |
| `CALL_STUN_URLS` | 根据 `TURN_PUBLIC_HOST` 生成 | 可显式覆盖的 STUN URL |
| `CALL_TURN_URLS` | 根据 `TURN_PUBLIC_HOST` 生成 UDP/TCP URL | 可显式覆盖的 TURN URL，逗号分隔 |
| `TURN_SHARED_SECRET` | 仅本地开发值 | Coturn REST API 共享密钥 |
| `TURN_CREDENTIAL_TTL_SECONDS` | `3600` | 临时 TURN 凭证有效期 |

## 生产配置

至少设置以下变量:

```env
TURN_EXTERNAL_IP=<public-ip>
TURN_PUBLIC_HOST=<public-domain-or-ip>
TURN_SHARED_SECRET=<strong-random-secret>
```

防火墙需要开放 `3478/tcp`、`3478/udp` 和 `49160-49200/udp`。`8085/tcp` 只用于内部健康检查，不需要暴露到公网。

## API 与限制

客户端 API 使用 `proto/call/call_interface.proto`，经现有 `/ws` 连接发送。HTTP 仅提供 `GET /health` 和 `GET /ready`。

当前支持一对一语音、视频、trickle ICE、拒绝、取消、挂断、忙线、响铃超时和 PushKit 离线唤醒。暂不支持群通话、录制、服务端转码和屏幕共享策略；群通话需要在下一阶段引入 SFU。
