#!/bin/bash

# 文件哈希计算工具 (Shell版本)
# 使用方法: ./calculate_file_hash.sh <文件路径>

if [ $# -eq 0 ]; then
    echo "使用方法: $0 <文件路径>"
    echo "示例: $0 test.txt"
    exit 1
fi

FILE_PATH="$1"

if [ ! -f "$FILE_PATH" ]; then
    echo "错误: 文件不存在: $FILE_PATH"
    exit 1
fi

# 获取文件大小
FILE_SIZE=$(stat -f%z "$FILE_PATH" 2>/dev/null || stat -c%s "$FILE_PATH" 2>/dev/null)

# 计算SHA512哈希
if command -v sha512sum >/dev/null 2>&1; then
    FILE_HASH=$(sha512sum "$FILE_PATH" | cut -d' ' -f1)
elif command -v shasum >/dev/null 2>&1; then
    FILE_HASH=$(shasum -a 512 "$FILE_PATH" | cut -d' ' -f1)
else
    echo "错误: 未找到 sha512sum 或 shasum 命令"
    exit 1
fi

# 输出结果
echo "文件元数据:"
echo "  文件路径: $FILE_PATH"
echo "  文件大小: $FILE_SIZE 字节"
echo "  SHA512哈希: $FILE_HASH"
echo ""
echo "用于Postman测试的JSON:"
echo "{"
echo "  \"file_hash\": \"$FILE_HASH\","
echo "  \"file_size\": $FILE_SIZE"
echo "}"

