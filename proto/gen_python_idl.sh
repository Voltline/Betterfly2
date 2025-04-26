#!/bin/bash

# 设置proto文件目录和输出目录
PROTO_DIR="."
OUT_DIR="debugger/"

# 确保输出目录存在
mkdir -p ${OUT_DIR}

# 找到所有proto文件
PROTO_FILES=$(find ${PROTO_DIR} -name "*.proto")

# 生成Python文件
echo "正在从proto文件生成Python文件..."

# 遍历proto文件并生成Python文件
for proto_file in ${PROTO_FILES}; do
    echo "处理proto文件：${proto_file}"
    python3.9 -m grpc_tools.protoc \
        -I${PROTO_DIR} \
        -I${PROTO_DIR}/idl \
        --python_out=${OUT_DIR} \
        --grpc_python_out=${OUT_DIR} \
        ${proto_file}
done

echo "proto编译完成"