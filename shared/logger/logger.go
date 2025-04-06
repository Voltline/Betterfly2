package logger

import (
	"Betterfly2/shared/logger/logger_config"
	"go.uber.org/zap"
	"sync"
)

var (
	once  sync.Once
	log   *zap.Logger
	sugar *zap.SugaredLogger
)

func initSugar() {
	once.Do(func() {
		log = zap.New(logger_config.CoreConfig, zap.AddCaller())
		sugar = log.Sugar()
	})
}

func Sugar() *zap.SugaredLogger {
	if sugar == nil {
		initSugar()
	}
	return sugar
}

// 在程序结束前调用
func Sync() error {
	if log != nil {
		return log.Sync()
	}
	return nil
}
