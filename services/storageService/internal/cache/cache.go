package cache

import "time"

// Cache 接口定义缓存操作
type Cache interface {
	// Set 设置键值对，带TTL
	Set(key string, value interface{}, ttl time.Duration) bool
	// Get 获取键值
	Get(key string) (interface{}, bool)
	// Del 删除键
	Del(key string)
	// Close 关闭缓存连接
	Close()
}
