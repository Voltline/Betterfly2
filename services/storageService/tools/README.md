# 文件哈希计算工具

这个工具用于计算文件的SHA512哈希值和文件大小，方便测试文件上传功能。

提供了三种实现方式：Go版本、Shell脚本版本和Python版本，你可以选择任意一种使用。

## 使用方法

### 方法1: Go版本（推荐）

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

### 方法2: Shell脚本版本

```bash
cd services/storageService/tools
chmod +x calculate_file_hash.sh
./calculate_file_hash.sh <文件路径>
```

**示例**:
```bash
./calculate_file_hash.sh test.txt
```

### 方法3: Python版本

```bash
cd services/storageService/tools
python3 calculate_file_hash.py <文件路径>
```

**示例**:
```bash
python3 calculate_file_hash.py test.txt
```

### 方法4: 使用Docker容器内运行

```bash
docker exec -it storageService sh -c "cd /app && go run calculate_file_hash.go -file /path/to/file"
```

## 输出示例

```
文件元数据:
  文件路径: /path/to/test.txt
  文件大小: 1024 字节
  SHA512哈希: abc123def456...

用于Postman测试的JSON:
{
  "file_hash": "abc123def456...",
  "file_size": 1024
}
```

## 快速测试

1. **创建一个测试文件**:
```bash
cd services/storageService/tools
echo "Hello, Betterfly2! This is a test file for upload." > test.txt
```

2. **计算哈希**（选择任意一种方式）:
```bash
# Go版本
go run calculate_file_hash.go -file test.txt

# 或Shell版本
./calculate_file_hash.sh test.txt

# 或Python版本
python3 calculate_file_hash.py test.txt
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

