package main

import (
	"crypto/sha512"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"
)

func main() {
	var filePath string
	flag.StringVar(&filePath, "file", "", "文件路径")
	flag.Parse()

	if filePath == "" {
		fmt.Println("使用方法: go run calculate_file_hash.go -file <文件路径>")
		fmt.Println("或者: ./calculate_file_hash -file <文件路径>")
		flag.Usage()
		os.Exit(1)
	}

	// 打开文件
	file, err := os.Open(filePath)
	if err != nil {
		fmt.Printf("打开文件失败: %v\n", err)
		os.Exit(1)
	}
	defer file.Close()

	// 获取文件信息
	fileInfo, err := file.Stat()
	if err != nil {
		fmt.Printf("获取文件信息失败: %v\n", err)
		os.Exit(1)
	}

	fileSize := fileInfo.Size()

	// 计算SHA512哈希
	hasher := sha512.New()
	_, err = io.Copy(hasher, file)
	if err != nil {
		fmt.Printf("读取文件失败: %v\n", err)
		os.Exit(1)
	}

	hashBytes := hasher.Sum(nil)
	fileHash := hex.EncodeToString(hashBytes)

	// 输出结果
	fmt.Println("文件元数据:")
	fmt.Printf("  文件路径: %s\n", filePath)
	fmt.Printf("  文件大小: %d 字节\n", fileSize)
	fmt.Printf("  SHA512哈希: %s\n", fileHash)
	fmt.Println()
	fmt.Println("用于Postman测试的JSON:")
	fmt.Printf(`{
  "file_hash": "%s",
  "file_size": %d
}`, fileHash, fileSize)
	fmt.Println()
}
