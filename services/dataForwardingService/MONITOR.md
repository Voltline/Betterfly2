# Betterfly Monitor

Betterfly Monitor 是 DataForwarding Service 内置的虚拟系统联系人。它不对应数据库用户、不能登录，也不会接收普通消息或 APNs。默认 BF ID 为 `900000000000000001`，只有经过 JWT 验证且当前 WebSocket 会话用户 ID 为 `1` 时才可见和使用。

## 使用流程

1. 用户 `1` 查询 Monitor BF ID。
2. 服务端返回虚拟用户资料，客户端按普通好友流程发送添加请求。
3. 添加状态写入共享 Redis，随后好友列表会动态注入 Monitor。
4. 向 Monitor 发送 `text` 消息执行指令。

其他用户查询、添加、删除、修改或直接向保留 ID 发送消息时，服务端统一返回“目标用户不存在”。即使数据库因历史数据或内部请求出现相同 ID，DataForwarding Service 也会从普通用户的好友列表中过滤该记录。

## 指令

| 指令 | 作用 |
| --- | --- |
| `/help` | 显示允许的指令 |
| `/status` | 并发探测 DataForwarding、Redis、Kafka、Auth、Friend、Storage、ABTest、Call 和 Push |
| `/connections` | 查看当前 DF Pod 的连接数及 Redis 中的全局路由记录数 |
| `/route <user_id>` | 查看有效 WebSocket 路由及所在 DF Pod |
| `/kick <user_id>` | 通过 Redis Pub/Sub 请求目标 DF Pod 断开该用户当前 WebSocket |

`/kick` 不能用于用户 `1` 或 Monitor 自身。它只终止当前连接，不轮换用户 JWT，因此客户端仍可重新连接；如果需要永久封禁，应另行设计账户状态和 AuthService 校验，不能把断线冒充封禁。

Monitor 不支持 shell、SQL、任意 URL 请求、清空缓存或动态执行代码。新增管理指令时，应继续使用固定命令注册和类型化参数，不得把消息正文拼接到命令行或数据库语句中。

## 配置

```env
MONITOR_USER_ID=900000000000000001
MONITOR_NAME=Betterfly Monitor
MONITOR_AVATAR=
```

超级管理员用户 ID 固定为 `1`，不能通过环境变量修改。`MONITOR_USER_ID` 必须是正整数且不能为 `1`；非法配置会回退到默认保留 ID。部署方必须确保该保留 ID 不分配给真实用户。

状态探测默认使用 Compose 内网服务名，可通过以下可信服务端环境变量覆盖：

- `MONITOR_AUTH_ADDR`
- `MONITOR_FRIEND_READY_URL`
- `MONITOR_STORAGE_READY_URL`
- `MONITOR_ABTEST_READY_URL`
- `MONITOR_CALL_READY_URL`
- `MONITOR_PUSH_READY_URL`

这些变量只能由部署配置控制，客户端不能指定探测地址。未启用的可选 profile 会在 `/status` 中显示 unavailable，而不会影响其他指令。

## 数据与审计

- 联系人开关保存在 Redis `monitor:contact:1`，因此多个 DF Pod 行为一致；Redis 数据清空后需要重新添加。
- 命令请求和 Monitor 回复不进入 Storage Service，也不会出现在消息同步结果中。
- Monitor 为每次添加、删除和指令执行记录结构化日志，但不记录完整指令正文。
- 指令回复沿用现有 `Post` 与 `PostAckRsp`，客户端不需要增加新的 Protobuf 协议。Monitor ACK 使用负数合成 ID，避免抬高客户端记录的数据库消息 ID 上界。
