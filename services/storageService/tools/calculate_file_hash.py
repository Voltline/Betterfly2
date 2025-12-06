#!/usr/bin/env python3
"""
文件哈希计算工具 (Python版本)
使用方法: python3 calculate_file_hash.py <文件路径>
"""

import sys
import hashlib
import os
import json

def calculate_file_hash(file_path):
    """计算文件的SHA512哈希值和大小"""
    if not os.path.isfile(file_path):
        print(f"错误: 文件不存在: {file_path}", file=sys.stderr)
        sys.exit(1)
    
    # 获取文件大小
    file_size = os.path.getsize(file_path)
    
    # 计算SHA512哈希
    sha512_hash = hashlib.sha512()
    with open(file_path, 'rb') as f:
        # 分块读取，避免大文件占用过多内存
        for chunk in iter(lambda: f.read(4096), b""):
            sha512_hash.update(chunk)
    
    file_hash = sha512_hash.hexdigest()
    
    return file_hash, file_size

def main():
    if len(sys.argv) != 2:
        print("使用方法: python3 calculate_file_hash.py <文件路径>")
        print("示例: python3 calculate_file_hash.py test.txt")
        sys.exit(1)
    
    file_path = sys.argv[1]
    file_hash, file_size = calculate_file_hash(file_path)
    
    # 输出结果
    print("文件元数据:")
    print(f"  文件路径: {file_path}")
    print(f"  文件大小: {file_size} 字节")
    print(f"  SHA512哈希: {file_hash}")
    print()
    print("用于Postman测试的JSON:")
    print(json.dumps({
        "file_hash": file_hash,
        "file_size": file_size
    }, indent=2, ensure_ascii=False))

if __name__ == "__main__":
    main()

