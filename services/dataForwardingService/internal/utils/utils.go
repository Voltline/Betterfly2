package utils

import "strings"

// SplitBrokers 解析多个 Kafka broker 地址
func SplitBrokers(broker string) []string {
	// 将逗号分隔的 broker 地址拆分为数组
	return strings.Split(broker, ",")
}
