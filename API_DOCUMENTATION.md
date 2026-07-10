# Betterfly2 API 文档

本文档描述了 Betterfly2 当前 HTTP API 与内部接口。

**文档版本**: 1.3
**最后更新**: 2026-07-10

## 目录

1. [HTTP API（对外接口）](#http-api对外接口)
2. [Kafka MQ API（对内接口）](#kafka-mq-api对内接口)
3. [Protobuf 消息定义](#protobuf消息定义)
4. [ABTest Service API](#abtest-service-api)
5. [Call Service API](#call-service-api)

---

## Call Service API

CallService 提供一对一 WebRTC 语音和视频通话的控制面。媒体流不经过 CallService；客户端通过 WebRTC 建立点对点连接，无法直连时由 Coturn 中继。

服务健康检查为 `GET /health` 和 `GET /ready`，默认端口 `8085`。客户端通话 API 继续复用已登录的 `/ws` Protobuf 连接，具体消息见本文“通话协议”章节。

### 通话协议

协议定义位于 `proto/call/call_interface.proto`。客户端将 `call_interface.ClientRequest` 放入 `df_interface.RequestMessage.call_request`，并携带当前登录 JWT。服务端返回 `df_interface.ResponseMessage.call_event`。

客户端命令:

- `get_config`: 获取 STUN/TURN 地址和短期 TURN 凭证。
- `initiate`: 发起一对一语音或视频通话，必须携带 `callee_user_id`、`AUDIO/VIDEO` 和 SDP offer。
- `accept`: 被叫接听，必须携带 `call_id` 和 SDP answer。
- `reject`: 被叫拒绝响铃中的通话。
- `hangup`: 任一参与者取消或结束通话。
- `ice_candidate`: 将 trickle ICE candidate 转发给另一参与者。

服务端事件:

- `CALL_CONFIG`: ICE server 列表。
- `OUTGOING_CALL`: 服务端已创建通话，主叫获得唯一 `call_id`。
- `INCOMING_CALL`: 被叫收到来电、主叫 ID、通话类型及 SDP offer。
- `CALL_ACCEPTED`: 主叫收到 SDP answer，双方进入 `ACTIVE`。
- `CALL_REJECTED`: 被叫拒绝。
- `CALL_ENDED`: 挂断、取消、断连或响铃超时。
- `ICE_CANDIDATE_RECEIVED`: 收到对端 candidate。
- `CALL_ERROR`: 离线、忙线、越权、状态冲突或参数错误。

典型时序:

```text
caller -> DF -> call-service: initiate(offer)
call-service -> caller: OUTGOING_CALL(call_id)
call-service -> callee: INCOMING_CALL(call_id, offer)
callee -> DF -> call-service: accept(call_id, answer)
call-service -> caller: CALL_ACCEPTED(answer)
caller <-> call-service <-> callee: ice_candidate
caller <========= WebRTC media / Coturn relay =========> callee
either side -> call-service: hangup(call_id)
call-service -> both sides: CALL_ENDED
```

身份安全边界:

- `caller_user_id` 不由客户端填写，DF 从已认证 WebSocket 会话中注入 `InternalRequest.user_id`。
- CallService 会验证接听者必须是被叫方，ICE 与挂断操作者必须是通话参与者。
- 同一用户同时只能占用一个 `RINGING` 或 `ACTIVE` 通话。
- TURN 使用基于共享密钥生成的短期 HMAC 凭证，`TURN_SHARED_SECRET` 不会下发给客户端。

### 部署端口

- `8085/tcp`: CallService 健康检查，仅需内网开放。
- `3478/udp`、`3478/tcp`: STUN/TURN 监听端口，需要对客户端开放。
- `49160-49200/udp`: Coturn 媒体中继端口范围，需要对客户端开放。

生产环境必须将 `TURN_EXTERNAL_IP` 设置为服务器公网 IP，并将 `TURN_PUBLIC_HOST` 设置为客户端可访问的域名或公网 IP；也可以通过 `CALL_STUN_URLS`、`CALL_TURN_URLS` 显式覆盖自动生成的地址。`TURN_SHARED_SECRET` 必须在 CallService 与 Coturn 中保持一致。

当前范围是一对一通话。群语音/群视频需要引入 SFU；未来可以复用现有 `call_id`、ICE 配置和状态事件，在媒体层接入 LiveKit、mediasoup 或 ion-sfu。

## ABTest Service API

ABTestService 提供统一实验配置与稳定分流能力。当前主要用于客户端实验，接口设计已预留服务端实验入口。

**基础URL**: `http://localhost:8082`

### 客户端获取实验配置

**接口**: `GET /abtest/v1/client/config`

**Query参数**:

- `device_id` 必需，客户端设备唯一ID
- `platform` 可选，例如 `ios`、`android`
- `app_version` 可选，例如 `1.2.0`
- `os` 可选，例如 `iOS`
- `system_version` 可选，例如 `17.4`

**示例**:

```http
GET /abtest/v1/client/config?device_id=device-001&platform=ios&app_version=1.2.0&system_version=17.4
```

**响应**:

```json
{
  "server_time": "2026-04-29T10:00:00Z",
  "merged_config": {
    "enable_new_chat_ui": true
  },
  "experiments": [
    {
      "experiment_id": 1,
      "experiment_key": "new_chat_ui",
      "experiment_type": "client",
      "group_key": "variant",
      "version": 3,
      "start_time": "2026-04-29T10:00:00Z",
      "end_time": "2026-05-06T10:00:00Z",
      "duration_seconds": 604800,
      "config": {
        "enable_new_chat_ui": true
      }
    }
  ]
}
```

### 通用实验求值接口

**接口**: `POST /abtest/v1/evaluate`

该接口用于未来服务端实验。`subject_type` 可以是 `device`、`user`、`server` 等，`context` 用于传入版本、平台、区域等扩展条件。

```json
{
  "subject_type": "server",
  "subject_id": "dataForwardingService",
  "context": {
    "region": "sg",
    "feature": "message_sync"
  }
}
```

### 管理面板与管理API

管理面板:

```http
GET /abtest/admin
```

管理 API 默认受 `ABTEST_ADMIN_TOKEN` 保护；本地未设置该环境变量时开放。

常用接口:

- `GET /abtest/admin/api/experiments`
- `POST /abtest/admin/api/experiments`
- `GET /abtest/admin/api/experiments/{id}`
- `PUT /abtest/admin/api/experiments/{id}`
- `POST /abtest/admin/api/experiments/{id}/start`
- `POST /abtest/admin/api/experiments/{id}/pause`
- `POST /abtest/admin/api/experiments/{id}/stop`
- `POST /abtest/admin/api/experiments/{id}/withdraw`
- `POST /abtest/admin/api/experiments/{id}/groups`
- `POST /abtest/admin/api/experiments/{id}/groups/{group_id}/push_full`
- `POST /abtest/admin/api/experiments/{id}/overrides`

### 推全语义

`push_full` 不只是把目标分组流量调成 `10000`。推全后实验会进入 `rolled_out` 状态，并记录 `rollout_group_key`。客户端或服务端再次获取该 `experiment_key` 时，会直接返回该分组配置，不再检查实验开始/结束时间，也不再走流量分桶。撤回推全使用 `withdraw`，撤回后状态变为 `stopped`，客户端下一次拉配置将不再收到该 key。

创建实验示例:

```json
{
  "experiment_key": "new_chat_ui",
  "name": "新聊天页",
  "experiment_type": "client",
  "start_time": "2026-04-29T10:00:00Z",
  "duration_seconds": 604800,
  "targeting": {
    "platforms": ["ios"],
    "min_app_version": "1.2.0",
    "min_system_version": "17.0"
  },
  "groups": [
    {
      "group_key": "control",
      "traffic_basis_points": 5000,
      "config": {"enable_new_chat_ui": false}
    },
    {
      "group_key": "variant",
      "traffic_basis_points": 5000,
      "config": {"enable_new_chat_ui": true}
    }
  ]
}
```

Targeting 规则当前支持:

- `platforms`
- `app_versions`
- `os`、`oses` 或 `operating_systems`
- `min_app_version`
- `max_app_version`
- `system_versions`
- `min_system_version`
- `max_system_version`
- `include`: 任意 context 字段白名单
- `exclude`: 任意 context 字段黑名单

---

## HTTP API（对外接口）

存储服务提供独立的 HTTP 服务，客户端可以直接通过 HTTP 进行文件的上传和下载操作。

**基础URL**: `http://localhost:8081/storage_service` (开发环境)

**认证方式**: 文件控制面接口都需要在请求头中携带 JWT Token 和用户ID；`/health` 与 `/ready` 用于探针检查，可匿名访问

**请求头设置**:
```
Authorization: Bearer <JWT_TOKEN>
X-User-ID: <USER_ID>
Content-Type: application/json
```

**或者通过 Query 参数传递 user_id**:
```
?user_id=<USER_ID>
```

### Postman 使用说明

#### 1. 设置 Headers

在 Postman 的 **Headers** 标签页中添加以下请求头：

| Key | Value | 说明 |
|-----|-------|------|
| `Authorization` | `Bearer <你的JWT_TOKEN>` | JWT Token，注意Bearer后面有空格 |
| `X-User-ID` | `<你的用户ID>` | 用户ID，数字 |
| `Content-Type` | `application/json` | 请求体格式 |

**示例**:
```
Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...
X-User-ID: 123
Content-Type: application/json
```

#### 2. 使用 Postman Environment 变量（推荐）

为了便于管理和切换环境，建议使用 Postman 的 Environment 功能：

1. **创建 Environment**:
   - 点击右上角的 Environment 下拉菜单
   - 选择 "Manage Environments"
   - 点击 "Add" 创建新环境
   - 添加以下变量：
     - `base_url`: `http://localhost:8081`
     - `jwt_token`: `<你的JWT Token>`
     - `user_id`: `<你的用户ID>`

2. **在请求中使用变量**:
   - URL: `{{base_url}}/storage_service/upload`
   - Headers:
     - `Authorization`: `Bearer {{jwt_token}}`
     - `X-User-ID`: `{{user_id}}`

3. **获取 JWT Token**:
   - 通过登录接口获取（数据转发服务或认证服务）
   - 登录成功后，从响应中提取 JWT token
   - 将 token 保存到 Environment 变量中

#### 3. 测试步骤

1. **获取 JWT Token**:
   - 先调用登录接口获取有效的 JWT token
   - 将 token 保存到 Environment 变量

2. **测试上传接口**:
   - Method: `POST`
   - URL: `{{base_url}}/storage_service/upload`
   - Headers: 使用上述变量
   - Body (raw JSON):
     ```json
     {
       "file_hash": "abc123def456...",
       "file_size": 1024
     }
     ```

3. **测试下载接口**:
   - Method: `GET`
   - URL: `{{base_url}}/storage_service/download?file_hash=<文件哈希>`
   - Headers: 使用上述变量

---

### 1. 文件上传（第一阶段：获取上传URL）

**接口**: `POST /storage_service/upload`

**描述**: 客户端请求上传文件，服务端返回预签名上传URL或文件已存在标识。

**请求头**:
```
Authorization: Bearer <JWT_TOKEN>
X-User-ID: <USER_ID>
Content-Type: application/json
```

**请求体** (JSON):
```json
{
  "file_hash": "sha512哈希值",
  "file_size": 12345
}
```

**响应** (JSON):

如果文件已存在：
```json
{
  "exists": true
}
```

如果文件不存在，返回上传URL：
```json
{
  "exists": false,
  "upload_url": "https://rustfs-endpoint/presigned-upload-url",
  "expires_in": 3600
}
```

**错误响应**:
- `400 Bad Request`: 请求参数错误
- `401 Unauthorized`: JWT验证失败
- `500 Internal Server Error`: 服务器内部错误

---

### 2. 文件上传验证（第二阶段：验证上传的文件）

**接口**: `POST /storage_service/upload/verify`

**描述**: 客户端上传文件完成后，调用此接口验证文件哈希值并保存元数据。

**请求头**:
```
Authorization: Bearer <JWT_TOKEN>
X-User-ID: <USER_ID>
Content-Type: application/json
```

**请求体** (JSON):
```json
{
  "file_hash": "sha512哈希值"
}
```

**响应** (JSON):

验证成功：
```json
{
  "success": true
}
```

验证失败：
```json
{
  "success": false,
  "error_message": "File hash mismatch"
}
```

**错误响应**:
- `400 Bad Request`: 请求参数错误
- `401 Unauthorized`: JWT验证失败
- `500 Internal Server Error`: 服务器内部错误

---

### 3. 文件下载

**接口**: `GET /storage_service/download?file_hash=<FILE_HASH>`

**描述**: 客户端请求下载文件，服务端返回预签名下载URL。

**请求头**:
```
Authorization: Bearer <JWT_TOKEN>
X-User-ID: <USER_ID>
```

**Query参数**:
- `file_hash` (必需): 文件的SHA512哈希值

**响应** (JSON):

如果文件存在：
```json
{
  "exists": true,
  "download_url": "https://rustfs-endpoint/presigned-download-url",
  "expires_in": 3600,
  "file_size": 12345
}
```

如果文件不存在：
```json
{
  "exists": false,
  "error_message": "File not found"
}
```

**错误响应**:
- `400 Bad Request`: 请求参数错误（缺少file_hash）
- `401 Unauthorized`: JWT验证失败
- `500 Internal Server Error`: 服务器内部错误

---

### 4. 健康检查

**接口**: `GET /health`

**描述**: 检查服务是否正常运行（不需要认证）

**响应**:
```
OK
```

---

### 5. 就绪检查

**接口**: `GET /ready`

**描述**: 检查文件控制面的关键依赖是否可用。当前会检查 PostgreSQL 连接和 RustFS bucket。

**认证要求**: 无

**成功响应** (JSON):
```json
{
  "ready": true
}
```

**失败响应** (JSON):
```json
{
  "ready": false,
  "error_message": "database not ready: ..."
}
```

---

## Kafka MQ API（对内接口）

存储服务通过 Kafka 消息队列接收来自其他服务（主要是数据转发服务）的查询请求。

**Topic**: `storage-service`

**消息格式**: Protobuf 序列化的 `RequestMessage`，封装在 `Envelope` 中

**响应**: 通过请求中的 `from_kafka_topic` 字段指定的 topic 返回响应

---

### 1. 查询文件是否存在

**请求消息**: `QueryFileExists`

```protobuf
message QueryFileExists {
  string file_hash = 1;  // 文件SHA512哈希值
}
```

**响应消息**: `FileExistsRsp`

```protobuf
message FileExistsRsp {
  bool exists = 1;
  int64 file_size = 2;
  string storage_path = 3;
}
```

**使用场景**: 其他服务需要查询文件是否存在时，通过 Kafka 发送查询请求。

---

### 2. 存储新消息

**请求消息**: `StoreNewMessage`

```protobuf
message StoreNewMessage {
  int64 from_user_id = 1;
  int64 to_user_id = 2;
  string content = 3;
  string message_type = 4; // text, image, gif, file, audio, video, link
  bool is_group = 5;
  string real_file_name = 6; // 文件消息对应的原始文件名，非文件消息为空
}
```

**响应消息**: `StoreMsgRsp`

```protobuf
message StoreMsgRsp {
  int64 message_id = 1;
}
```

数据转发服务收到 `StoreMsgRsp` 后，会向发送方客户端返回 `PostAckRsp`：

```protobuf
message PostAckRsp {
  int64 message_id = 1;
}
```

---

### 3. 查询消息

**请求消息**: `QueryMessage`

```protobuf
message QueryMessage {
  int64 message_id = 1;
}
```

**响应消息**: `MessageRsp`

```protobuf
message MessageRsp {
  int64 from_user_id = 1;
  int64 to_user_id = 2;
  string content = 3;
  string timestamp = 4;
  string msg_type = 5; // text, image, gif, file, audio, video, link
  bool is_group = 6;
  string real_file_name = 7;
  int64 message_id = 8;
}
```

---

### 4. 同步消息

**请求消息**: `QuerySyncMessages`

```protobuf
message QuerySyncMessages {
  int64 to_user_id = 1;
  string timestamp = 2;
}
```

**响应消息**: `SyncMessagesRsp`

```protobuf
message SyncMessagesRsp {
  repeated MessageRsp msgs = 1;
}
```

---

### 5. 查询用户信息

**请求消息**: `QueryUser`

```protobuf
message QueryUser {
  int64 user_id = 1;
}
```

**响应消息**: `UserInfoRsp`

```protobuf
message UserInfoRsp {
  int64 user_id = 1;
  string account = 2;
  string name = 3;
  string avatar = 4;
  string update_time = 5;
}
```

---

### 6. 更新用户名

**请求消息**: `UpdateUserName`

```protobuf
message UpdateUserName {
  int64 user_id = 1;
  string new_user_name = 2;
}
```

**响应消息**: `ResponseMessage` (无payload，仅result字段)

---

### 7. 更新用户头像

**请求消息**: `UpdateUserAvatar`

```protobuf
message UpdateUserAvatar {
  int64 user_id = 1;
  string new_avatar_url = 2;
}
```

**响应消息**: `ResponseMessage` (无payload，仅result字段)

---

## Protobuf 消息定义

### 通用消息结构

#### RequestMessage

所有对内接口的请求都封装在 `RequestMessage` 中：

```protobuf
message RequestMessage {
  string from_kafka_topic = 1;  // 响应发送到的topic
  int64 target_user_id = 2;     // 目标用户ID
  oneof payload {
    StoreNewMessage store_new_message = 3;
    QueryMessage query_message = 4;
    QuerySyncMessages query_sync_messages = 5;
    UpdateUserName update_user_name = 6;
    UpdateUserAvatar update_user_avatar = 7;
    QueryUser query_user = 8;
    QueryFileExists query_file_exists = 9;
  }
}
```

#### ResponseMessage

所有对内接口的响应都封装在 `ResponseMessage` 中：

```protobuf
message ResponseMessage {
  StorageResult result = 1;
  int64 target_user_id = 2;
  oneof payload {
    StoreMsgRsp store_msg_rsp = 3;
    MessageRsp msg_rsp = 4;
    SyncMessagesRsp sync_msgs_rsp = 5;
    UserInfoRsp user_info_rsp = 6;
    FileExistsRsp file_exists_rsp = 7;
  }
}
```

#### StorageResult

```protobuf
enum StorageResult {
  OK = 0;
  SERVICE_ERROR = 255;
  RECORD_NOT_EXIST = 1;
}
```

---

### HTTP 服务消息定义

#### UploadFileRequest

```protobuf
message UploadFileRequest {
  string file_hash = 1;  // 文件SHA512哈希值
  int64 file_size = 2;   // 文件大小（字节）
}
```

#### UploadFileResponse

```protobuf
message UploadFileResponse {
  bool exists = 1;              // 文件是否已存在
  string upload_url = 2;        // 预签名上传URL（如果文件不存在）
  int64 expires_in = 3;         // URL过期时间（秒）
  string error_message = 4;     // 错误信息（如果有）
}
```

#### VerifyUploadRequest

```protobuf
message VerifyUploadRequest {
  string file_hash = 1;  // 文件SHA512哈希值
}
```

#### VerifyUploadResponse

```protobuf
message VerifyUploadResponse {
  bool success = 1;           // 验证是否成功
  string error_message = 2;  // 错误信息（如果有）
}
```

#### DownloadFileRequest

```protobuf
message DownloadFileRequest {
  string file_hash = 1;  // 文件SHA512哈希值
}
```

#### DownloadFileResponse

```protobuf
message DownloadFileResponse {
  bool exists = 1;              // 文件是否存在
  string download_url = 2;      // 预签名下载URL（如果文件存在）
  int64 expires_in = 3;         // URL过期时间（秒）
  int64 file_size = 4;          // 文件大小（字节）
  string error_message = 5;     // 错误信息（如果有）
}
```

---

## 文件上传流程

1. **客户端请求上传**
   - 客户端计算文件SHA512哈希值
   - 客户端发送 `POST /storage_service/upload` 请求，包含文件哈希和大小
   - 服务端验证JWT，检查文件是否已存在
   - 如果文件已存在，返回 `exists: true`
   - 如果文件不存在，生成预签名上传URL并返回

2. **客户端上传文件**
   - 客户端使用返回的预签名URL直接上传文件到RustFS
   - 上传过程不经过存储服务，直接与RustFS交互

3. **客户端验证上传**
   - 客户端上传完成后，发送 `POST /storage_service/upload/verify` 请求
   - 服务端从RustFS下载文件并验证哈希值
   - 如果哈希匹配，将文件元数据从待验证状态更新为已验证状态
   - 如果哈希不匹配，删除文件并返回错误

4. **客户端发送消息**
   - 客户端确认文件上传成功后，通过数据转发服务发送消息
   - 文件消息仍然使用普通 `Post` 报文发送
   - 约定 `msg_type = "file"`
   - 约定 `msg = file_hash`
   - 约定 `real_file_name = 客户端原始文件名`
   - 数据转发服务只转发元数据，不处理文件内容

---

## 文件下载流程

1. **客户端请求下载**
   - 客户端从消息中获取文件哈希值
   - 客户端发送 `GET /storage_service/download?file_hash=<hash>` 请求
   - 服务端验证JWT，检查文件是否存在
   - 如果文件存在，生成预签名下载URL并返回

2. **客户端下载文件**
   - 客户端使用返回的预签名URL直接下载文件
   - 下载过程不经过存储服务，直接与RustFS交互
   - 客户端根据消息中的 `real_file_name` 恢复文件真实名称

> 说明：只有完成 `upload/verify` 校验的文件才会被视为可用文件，未完成校验的待验证文件不会对下载接口或内部文件存在性查询暴露。

---

## 环境变量配置

### 存储服务环境变量

- `HTTP_PORT`: HTTP服务端口（默认: 8080）
- `PGSQL_DSN`: PostgreSQL数据库连接字符串
- `REDIS_ADDR`: Redis地址（默认: localhost:6379）
- `KAFKA_BROKER`: Kafka broker地址（逗号分隔）
- `KAFKA_STORAGE_TOPIC`: Kafka存储服务topic（默认: storage-service）
- `KAFKA_CONSUMER_GROUP`: Kafka消费者组（默认: storage-service-group）
- `AUTH_RPC_ADDR`: 认证服务gRPC地址（默认: localhost:50051）

### RustFS环境变量

- `RUSTFS_REGION`: RustFS区域（必需）
- `RUSTFS_ACCESS_KEY_ID`: RustFS访问密钥ID（必需）
- `RUSTFS_SECRET_ACCESS_KEY`: RustFS秘密访问密钥（必需）
- `RUSTFS_ENDPOINT_URL`: RustFS端点URL（必需）
- `RUSTFS_EXTERNAL_ENDPOINT_URL`: 客户端可直接访问的RustFS外部地址（可选，推荐在生产环境显式配置）
- `RUSTFS_EXTERNAL_PORT`: 未显式配置外部地址时，基于当前HTTP请求推导RustFS地址所使用的端口（默认: 9000）
- `RUSTFS_BUCKET`: RustFS存储桶名称（默认: betterfly-files）

---

## 错误码说明

### HTTP状态码

- `200 OK`: 请求成功
- `400 Bad Request`: 请求参数错误
- `401 Unauthorized`: JWT验证失败
- `404 Not Found`: 资源不存在
- `500 Internal Server Error`: 服务器内部错误

### StorageResult枚举

- `OK (0)`: 操作成功
- `RECORD_NOT_EXIST (1)`: 记录不存在
- `SERVICE_ERROR (255)`: 服务内部错误

---

## 注意事项

1. **文件哈希**: 所有文件操作都基于SHA512哈希值，客户端必须在上传前计算文件哈希
2. **JWT验证**: 所有HTTP接口都需要JWT验证，通过gRPC调用认证服务进行验证
3. **预签名URL**: 上传和下载URL都有有效期（默认1小时），客户端需要在有效期内使用
4. **文件存储**: 文件存储在RustFS中，路径格式为 `{hash前2位}/{完整hash}`
5. **元数据存储**: 文件元数据存储在PostgreSQL数据库中，包括哈希、大小、存储路径等
6. **真实文件名**: 数据库中不存储文件真实名称，真实名称在数据转发服务的消息中传递

---

## 示例代码

### 上传文件（Go示例）

```go
// 1. 计算文件哈希
hash := calculateSHA512(fileData)

// 2. 请求上传URL
req := UploadFileRequest{
    FileHash: hash,
    FileSize: int64(len(fileData)),
}
resp := requestUploadURL(req, jwt, userID)

// 3. 使用预签名URL上传
if !resp.Exists {
    uploadToRustFS(resp.UploadUrl, fileData)
    
    // 4. 验证上传
    verifyReq := VerifyUploadRequest{FileHash: hash}
    verifyResp := verifyUpload(verifyReq, jwt, userID)
}
```

### 下载文件（Go示例）

```go
// 1. 请求下载URL
downloadResp := requestDownloadURL(fileHash, jwt, userID)

// 2. 使用预签名URL下载
if downloadResp.Exists {
    fileData := downloadFromRustFS(downloadResp.DownloadUrl)
    // 使用消息中的real_file_name恢复文件名
}
```
