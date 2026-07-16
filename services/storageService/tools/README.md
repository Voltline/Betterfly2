# 文件哈希计算工具

这个工具用于计算文件的SHA512哈希值和文件大小，方便测试文件上传功能。

工具使用 Go 实现，与服务端开发环境保持一致。

## 使用方法

### Go版本

**直接运行**:
```bash
cd services/storageService/tools
go run calculate_file_hash.go -file <文件路径>
```

**编译后使用**:
```bash
cd services/storageService/tools
go build -o calculate_file_hash calculate_file_hash.go
./calculate_file_hash -file <文件路径>
```

**示例**:
```bash
go run calculate_file_hash.go -file test.txt
```

## 输出示例

```
文件元数据:
  文件路径: /path/to/test.txt
  文件大小: 1024 字节
  SHA512哈希: <128位十六进制SHA-512>

用于Postman测试的JSON:
{
  "file_hash": "<128位十六进制SHA-512>",
  "file_size": 1024
}
```

## 快速测试

1. **创建一个测试文件**:
```bash
cd services/storageService/tools
echo "Hello, Betterfly2! This is a test file for upload." > test.txt
```

2. **计算哈希**:
```bash
go run calculate_file_hash.go -file test.txt
```

3. **复制输出的JSON到Postman的请求体中**

## 完整测试流程

1. **计算文件哈希**:
```bash
cd services/storageService/tools
go run calculate_file_hash.go -file your_file.txt
```

2. **在Postman中测试上传**:
   - URL: `POST http://localhost:8081/storage_service/upload`
   - Headers:
     - `Authorization: Bearer <你的JWT_TOKEN>`
     - `X-User-ID: <你的用户ID>`
     - `Content-Type: application/json`
   - Body: 使用工具输出的JSON

3. **如果返回上传URL，使用该URL上传文件到RustFS**

4. **上传完成后，调用验证接口**:
   - URL: `POST http://localhost:8081/storage_service/upload/verify`
   - Headers: 同上
   - Body: `{"file_hash": "<文件哈希>"}`

5. **通过普通消息发送文件消息**:
   - `msg_type = "file"`
   - `msg = file_hash`
   - `real_file_name = 原始文件名`

## 探针接口

- 存活检查: `GET http://localhost:8081/health`
- 就绪检查: `GET http://localhost:8081/ready`

`/ready` 会检查 PostgreSQL 和 RustFS 是否可用，适合容器探针或反向代理健康检查。
