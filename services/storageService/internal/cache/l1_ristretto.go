package cache

import (
	"Betterfly2/shared/logger"
	"sync"
	"time"

	"github.com/dgraph-io/ristretto"
)

var (
	once      sync.Once
	closeOnce sync.Once
	l1Cache   *ristretto.Cache
)

// L1Cache 实现Cache接口
type L1Cache struct{}

// InitL1Cache 用于初始化一个L1 Cache
func InitL1Cache() {
	once.Do(func() {
		cache, err := ristretto.NewCache(&ristretto.Config{
			NumCounters: 1e7,     // 计数器数量
			MaxCost:     1 << 30, // 最大成本(1 GB)
			BufferItems: 256,     // 并发写缓冲大小
		})
		if err != nil {
			logger.Sugar().Fatal("初始化L1缓存失败: %v", err)
		}
		l1Cache = cache
		logger.Sugar().Debugf("L1 缓存初始化成功")
	})
}

// L1Set 可以设置一个带TTL的键值对加入L1缓存
func L1Set(key string, value interface{}, ttl time.Duration) bool {
	if l1Cache == nil {
		InitL1Cache()
	}
	return l1Cache.SetWithTTL(key, value, 1, ttl)
}

// L1Get 可以从L1缓存中获取一个值
func L1Get(key string) (interface{}, bool) {
	if l1Cache == nil {
		InitL1Cache()
	}
	val, found := l1Cache.Get(key)
	return val, found
}

// L1Del 从L1缓存中删除一个键
func L1Del(key string) {
	if l1Cache == nil {
		InitL1Cache()
	}
	l1Cache.Del(key)
}

// L1Close 用于关闭L1缓存
func L1Close() {
	if l1Cache == nil {
		return
	}
	closeOnce.Do(func() {
		l1Cache.Close()
		logger.Sugar().Infof("L1 缓存已关闭")
	})
}

// Set 实现Cache接口的Set方法
func (c *L1Cache) Set(key string, value interface{}, ttl time.Duration) bool {
	return L1Set(key, value, ttl)
}

// Get 实现Cache接口的Get方法
func (c *L1Cache) Get(key string) (interface{}, bool) {
	return L1Get(key)
}

// Del 实现Cache接口的Del方法
func (c *L1Cache) Del(key string) {
	L1Del(key)
}

// Close 实现Cache接口的Close方法
func (c *L1Cache) Close() {
	L1Close()
}

// NewL1Cache 创建新的L1Cache实例
func NewL1Cache() *L1Cache {
	InitL1Cache()
	return &L1Cache{}
}
