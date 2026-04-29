# Chatbot Service

ChatbotService 是 Betterfly2 面向外部 Agent 的受控工具服务，第一阶段优先服务 OpenClaw 这类聊天机器人运行时。它不会直接暴露数据库，也不会绕过 Betterfly2 现有权限边界，而是通过固定 HTTP API、Bot Token、Scope 和审计日志，把“查询用户/群聊/消息”等能力收束成一组可控工具。

当前阶段已经完成只读查询、OpenClaw webhook 骨架、审计日志和 Docker Compose 接入。发送消息接口已预留，但还没有接入 DataForwardingService 的实时投递链路，因此会返回 `501 Not Implemented`。

## 服务边界

- ChatbotService 面向 Agent、后台机器人和未来服务端实验/自动化逻辑，不直接面向普通客户端。
- 只读能力直接读取 Betterfly2 共享数据库模型，包括用户、群组、群成员和历史消息。
- 所有受保护接口都需要 Bot Token，并按 `CHATBOT_BOT_SCOPES` 做最小权限校验。
- 每次工具调用都会写入 `chatbot_audit_logs`，用于后续追踪哪个 Bot 做了什么操作。
- 写入型能力目前只保留接口形态，后续应走 DataForwardingService 或独立内部 MQ 路由，避免绕过主消息链路。

## 运行配置

Docker Compose 中默认暴露 `8083`:

```yaml
CHATBOT_HTTP_PORT=8083
CHATBOT_BOT_ID=openclaw
CHATBOT_BOT_TOKEN=dev-chatbot-token
CHATBOT_BOT_SCOPES=read:user,read:group,read:messages,openclaw:webhook
```

生产环境必须替换 `CHATBOT_BOT_TOKEN`，并建议按用途拆分不同 Bot Token。例如只做查询的 OpenClaw Bot 不应该拥有 `send:message`。

## 认证方式

推荐使用 Bearer Token:

```http
Authorization: Bearer <CHATBOT_BOT_TOKEN>
```

也支持兼容写法:

```http
X-Bot-Token: <CHATBOT_BOT_TOKEN>
```

可用 Scope:

- `read:user`: 查询用户基础信息。
- `read:group`: 查询群组和群成员信息。
- `read:messages`: 查询最近私聊或群聊消息。
- `send:message`: 发送消息，当前预留未实现。
- `openclaw:webhook`: 接收 OpenClaw 侧 webhook。
- `*`: 开发期全权限，不建议生产使用。

## API

### 健康检查

```http
GET /health
GET /ready
```

这两个接口不需要 Token。

### 查询用户

```http
GET /chatbot/v1/users/{user_id}
```

示例:

```bash
curl -H "Authorization: Bearer dev-chatbot-token" \
  http://localhost:8083/chatbot/v1/users/1
```

### 查询群组

```http
GET /chatbot/v1/groups/{group_id}
GET /chatbot/v1/groups/{group_id}/members
```

示例:

```bash
curl -H "Authorization: Bearer dev-chatbot-token" \
  http://localhost:8083/chatbot/v1/groups/10001/members
```

### 查询最近消息

私聊消息需要同时传入请求方用户 ID 和对端用户 ID:

```http
GET /chatbot/v1/conversations/direct/{peer_user_id}/recent_messages?user_id={user_id}&limit=20
```

群聊消息只需要群 ID:

```http
GET /chatbot/v1/conversations/group/{group_id}/recent_messages?limit=20
```

`limit` 默认 `20`，最大 `100`。接口会按时间倒序从数据库读取，再按时间正序返回，方便 Agent 直接阅读上下文。

### 发送消息

```http
POST /chatbot/v1/messages/send
Content-Type: application/json

{
  "from_user_id": 1,
  "to_user_id": 2,
  "content": "hello",
  "message_type": "text"
}
```

当前第一阶段返回 `501 Not Implemented`。后续接入时建议复用主消息链路，让消息仍然产生正式 `message_id`、存储记录、同步回执和在线投递。

### OpenClaw Webhook

```http
POST /chatbot/v1/openclaw/webhook
Content-Type: application/json

{
  "event_id": "evt-001",
  "type": "tool_call",
  "payload": {
    "tool": "betterfly.query_group",
    "group_id": 10001
  }
}
```

当前 webhook 只做接收、审计和 accepted 响应，真正的工具动作可以在第二阶段根据 `payload.tool` 分发到 ChatbotService 内部 action registry。

## OpenClaw Skill

仓库内已经提供一个 OpenClaw/AgentSkills 兼容的 skill 模板:

```text
skills/betterfly2-chatbot/SKILL.md
```

如果 OpenClaw 的 workspace 指向 Betterfly2 仓库根目录，它可以作为 workspace skill 被发现。否则可以复制该目录到 OpenClaw 支持的本地 skill 目录，或通过 OpenClaw 的 `skills.load.extraDirs` 指向本仓库的 `skills` 目录。

建议给 OpenClaw 运行环境配置:

```bash
export BETTERFLY_CHATBOT_BASE_URL="http://localhost:8083"
export BETTERFLY_CHATBOT_TOKEN="dev-chatbot-token"
```

## 下一阶段建议

- 接入 DataForwardingService 或内部 MQ，完成 `send:message` 的正式投递和回执。
- 给 OpenClaw webhook 增加 action registry，让 `payload.tool` 能映射到明确的服务端操作。
- 增加多 Bot Token 管理，支持不同 Bot 使用不同 scope、过期时间和审计标识。
- 增加细粒度访问控制，例如限制某个 Bot 只能查询指定群或指定用户授权的会话。
