---
name: betterfly2-chatbot
description: Use when an OpenClaw or AgentSkills-compatible agent needs to call Betterfly2 ChatbotService APIs to query Betterfly users, groups, group members, recent messages, or submit OpenClaw webhook events through a controlled bot token.
compatibility: Requires Betterfly2 ChatbotService HTTP access and BETTERFLY_CHATBOT_BASE_URL plus BETTERFLY_CHATBOT_TOKEN environment variables.
metadata: {"service":"chatbotService","version":"1.0.0","project":"Betterfly2"}
---

# Betterfly2 Chatbot Skill

Use this skill to interact with Betterfly2 through ChatbotService. The service is a controlled HTTP facade for Agent and chatbot integrations, not a raw database interface.

## Required Environment

Read these values from the runtime environment:

- `BETTERFLY_CHATBOT_BASE_URL`: ChatbotService base URL, for example `http://localhost:8083`.
- `BETTERFLY_CHATBOT_TOKEN`: Bot token configured as `CHATBOT_BOT_TOKEN` on the server.

Never reveal the token in chat, logs, summaries, or generated files.

## Authentication

All protected requests must include:

```http
Authorization: Bearer ${BETTERFLY_CHATBOT_TOKEN}
```

Health checks do not require authentication.

## Available Operations

### Check Service Health

Use when you need to verify the Betterfly2 chatbot bridge is reachable.

```http
GET ${BETTERFLY_CHATBOT_BASE_URL}/health
GET ${BETTERFLY_CHATBOT_BASE_URL}/ready
```

### Query User

Use when a task needs Betterfly2 user profile context.

```http
GET ${BETTERFLY_CHATBOT_BASE_URL}/chatbot/v1/users/{user_id}
```

Required server scope: `read:user`.

### Query Group

Use when a task needs group metadata such as name, avatar, owner, or update time.

```http
GET ${BETTERFLY_CHATBOT_BASE_URL}/chatbot/v1/groups/{group_id}
```

Required server scope: `read:group`.

### Query Group Members

Use when a task needs the current member list for a group.

```http
GET ${BETTERFLY_CHATBOT_BASE_URL}/chatbot/v1/groups/{group_id}/members
```

Required server scope: `read:group`.

### Query Recent Direct Messages

Use when a task needs recent private conversation context between a requester and another user.

```http
GET ${BETTERFLY_CHATBOT_BASE_URL}/chatbot/v1/conversations/direct/{peer_user_id}/recent_messages?user_id={requester_user_id}&limit=20
```

Required server scope: `read:messages`.

### Query Recent Group Messages

Use when a task needs recent group conversation context.

```http
GET ${BETTERFLY_CHATBOT_BASE_URL}/chatbot/v1/conversations/group/{group_id}/recent_messages?limit=20
```

Required server scope: `read:messages`.

### Submit OpenClaw Webhook

Use when OpenClaw needs to forward a tool event to Betterfly2 for server-side auditing or later action dispatch.

```http
POST ${BETTERFLY_CHATBOT_BASE_URL}/chatbot/v1/openclaw/webhook
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

Required server scope: `openclaw:webhook`.

### Send Message

The endpoint shape exists, but Betterfly2 ChatbotService phase 1 returns `501 Not Implemented`.

Do not use it for production message sending until the server has wired it to the official DataForwardingService message path.

```http
POST ${BETTERFLY_CHATBOT_BASE_URL}/chatbot/v1/messages/send
```

Required server scope: `send:message`.

## Safety Rules

- Ask for explicit user intent before reading conversation history.
- Prefer the smallest useful `limit` for message queries.
- Do not infer permission from successful HTTP access; treat sensitive user or group data as private.
- Do not call `send:message` unless the user explicitly asks and the server no longer returns `501`.
- If a request returns `401` or `403`, explain that the bot token or scope is insufficient rather than retrying repeatedly.
