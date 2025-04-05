package main

import (
	"go.uber.org/zap"
	logger_config "shared/logger_config"
)

func main() {
	log := zap.New(logger_config.CoreConfig, zap.AddCaller())

}
