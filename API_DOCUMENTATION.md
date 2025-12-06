# Betterfly2 API 文档

本文档描述了 Betterfly2 存储服务的所有 API 接口。

**文档版本**: 1.0  
**最后更新**: 2025-12-06

## 目录

1. [HTTP API（对外接口）](#http-api对外接口)
2. [Kafka MQ API（对内接口）](#kafka-mq-api对内接口)
3. [Protobuf 消息定义](#protobuf消息定义)

---

## HTTP API（对外接口）

存储服务提供独立的 HTTP 服务，客户端可以直接通过 HTTP 进行文件的上传和下载操作。

**基础URL**: `http://localhost:8081/storage_service` (开发环境)

**认证方式**: 所有接口都需要在请求头中携带 JWT Token 和用户ID

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
  string message_type = 4; // text, image, gif, file
  bool is_group = 5;
}
```

**响应消息**: `StoreMsgRsp`

```protobuf
message StoreMsgRsp {
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
  string msg_type = 5;
  bool is_group = 6;
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
   - 如果哈希匹配，保存文件元数据到数据库
   - 如果哈希不匹配，删除文件并返回错误

4. **客户端发送消息**
   - 客户端确认文件上传成功后，通过数据转发服务发送消息
   - 消息中包含文件哈希和真实文件名（`real_file_name`字段）
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

---

## 环境变量配置

### 存储服务环境变量

- `HTTP_PORT`: HTTP服务端口（默认: 8080）
- `PGSQL_DSN`: PostgreSQL数据库连接字符串
- `REDIS_ADDR`: Redis地址（默认: localhost:6379）
- `KAFKA_BROKER`: Kafka broker地址（逗号分隔）
- `KAFKA_STORAGE_TOPIC`: Kafka存储服务topic（默认: storage-requests）
- `KAFKA_CONSUMER_GROUP`: Kafka消费者组（默认: storage-service-group）
- `AUTH_RPC_ADDR`: 认证服务gRPC地址（默认: localhost:50051）

### RustFS环境变量

- `RUSTFS_REGION`: RustFS区域（必需）
- `RUSTFS_ACCESS_KEY_ID`: RustFS访问密钥ID（必需）
- `RUSTFS_SECRET_ACCESS_KEY`: RustFS秘密访问密钥（必需）
- `RUSTFS_ENDPOINT_URL`: RustFS端点URL（必需）
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

