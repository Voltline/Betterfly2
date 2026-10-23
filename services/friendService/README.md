# FriendService

FriendService 通过 Kafka topic `friend-service` 维护好友关系、群关系以及相关审批状态。客户端不直接访问该服务，而是通过 DataForwardingService 的 WebSocket protobuf API 调用。

## 关系规则

- 添加好友会创建有效期为 7 天的申请，只有目标用户明确接受后才会原子创建双向好友关系。
- 加入群聊会创建有效期为 7 天的入群申请，由群主或管理员接受或拒绝。
- 群主或管理员可以发出有效期为 7 天的群邀请，被邀请人明确接受后入群。
- 重复提交返回同一条 pending 申请；过期、已处理申请不能再次处理。
- 申请人可以使用 `REQUEST_CANCEL` 撤销自己的 pending 申请或邀请。
- `InsertContact` 和 `InsertGroupUser` 为兼容旧客户端保留，但已分别改为创建好友申请和入群申请，不再直接建立关系。

## 群权限

| 操作 | owner | admin | member |
| --- | --- | --- | --- |
| 审批入群申请 | 是 | 是 | 否 |
| 邀请成员 | 是 | 是 | 否 |
| 修改群头像 | 是 | 是 | 否 |
| 踢出普通成员 | 是 | 是 | 否 |
| 踢出管理员 | 是 | 否 | 否 |
| 任免管理员 | 是 | 否 | 否 |
| 踢出群主 | 否 | 否 | 否 |

群成员退出或被踢时物理删除 `group_members` 记录；历史消息不会随成员关系删除。

## 客户端 API

好友：

- `insert_contact`：创建好友申请，可携带 `message`。
- `query_friend_requests`：查询收到的申请；`include_outgoing=true` 时同时返回自己发出的申请。
- `resolve_friend_request`：使用 `REQUEST_ACCEPT`、`REQUEST_REJECT` 或 `REQUEST_CANCEL` 处理申请。
- `query_contacts`、`delete_contact`、`update_contact_alias`、`update_contact_notify`：管理已建立的好友关系。

群聊：

- `insert_group_user`：创建入群申请，可携带 `message`。
- `query_group_join_requests`、`resolve_group_join_request`：群主或管理员查询和审批入群申请。
- `invite_group_member`：群主或管理员邀请用户。
- `query_group_invitations`、`resolve_group_invitation`：查询和处理收到的群邀请；`include_outgoing=true` 时同时返回自己发出的邀请和入群申请。
- `kick_group_member`：按权限移除成员。
- `update_group_member_role`：群主将成员设置为 `admin` 或 `member`。

申请响应使用 `relationship_request_list_rsp` 或 `relationship_operation_rsp`，包含 `request_id`、`status`、`created_at`、`expires_at`、用户资料和群资料。群成员管理响应使用 `group_member_operation_rsp`。错误通过 `result` 返回，包括 `FORBIDDEN`、`REQUEST_EXPIRED`、`INVALID_STATE`、`ALREADY_EXIST` 和 `RECORD_NOT_EXIST`。

### 申请与邀请变化推送

申请或邀请新建、处理、撤回成功后，服务端主动向相关的另一方推送 `relationship_changed_event`（DF `ResponseMessage` 字段 24）。该推送不对应任何请求，不带请求 ID：

```protobuf
message RelationshipChangedEvent {
  RelationshipRequestInfo request = 1; // 变化后的完整状态，按 request_id 覆盖本地记录
  string operation = 2;                // created / resolved / cancelled
  string changed_at = 3;               // created 取 created_at，其余取 resolved_at
}
```

| 类型 | 新建或撤回推给 | 同意或拒绝推给 |
|------|----------------|----------------|
| 好友申请 | 被申请人 | 申请人 |
| 入群申请 | 群主和全部管理员 | 申请人，以及群主和其他管理员 |
| 群邀请 | 被邀请人 | 邀请人 |

- 操作者本人不推送，其结果已在 `relationship_operation_rsp` 中返回。
- 重复提交（`REQUEST_PENDING`）、处理失败和过期都不产生推送；客户端按 `expires_at` 自行判定过期。
- 事件与业务写入在同一事务内写入 outbox，由发起请求的 DF 按接收者路由（可跨 Pod）。接收者离线时直接丢弃，不重试、不进死信。客户端应保留低频轮询兜底，并在重连认证成功、回到前台时全量刷新。
- 旧客户端会忽略未知的字段 24，不需要开关。部署 friendService 之前须先部署能识别该事件的 DF：旧 DF 会把它当作未知好友响应，向接收者发一条“添加好友成功”。

## Monitor

Monitor 是虚拟联系人。仅 BFID 1 可以发现并添加，添加时直接成功且不创建好友申请；其他用户查询或操作时仍表现为目标不存在。除原有 `/status`、`/connections`、`/route`、`/kick` 外，Monitor 支持 `/user <user_id>` 查看安全用户摘要、`/group <group_id>` 查看成员角色分布，以及 `/requests <user_id>` 查看待处理申请数量。
