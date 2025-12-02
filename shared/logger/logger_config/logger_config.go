package logger_config

import (
	"go.uber.org/zap/zapcore"
	"os"
	"strings"
)

var encoderConfig zapcore.EncoderConfig = zapcore.EncoderConfig{
	TimeKey:       "time",
	LevelKey:      "level",
	NameKey:       "logger_config",
	CallerKey:     "caller",
	MessageKey:    "msg",
	StacktraceKey: "stacktrace",
	EncodeLevel:   zapcore.CapitalColorLevelEncoder,
	EncodeTime:    zapcore.ISO8601TimeEncoder,
	EncodeCaller:  zapcore.ShortCallerEncoder,
}

// getLogLevel 根据环境变量获取日志级别
func getLogLevel() zapcore.Level {
	logLevel := strings.ToLower(os.Getenv("LOG_LEVEL"))
	switch logLevel {
	case "debug":
		return zapcore.DebugLevel
	case "info":
		return zapcore.InfoLevel
	case "warn", "warning":
		return zapcore.WarnLevel
	case "error":
		return zapcore.ErrorLevel
	default:
		// 默认使用Info级别
		return zapcore.InfoLevel
	}
}

var CoreConfig zapcore.Core = zapcore.NewCore(
	zapcore.NewConsoleEncoder(encoderConfig), // 使用控制台编码器
	zapcore.AddSync(os.Stdout),               // 输出到控制台
	getLogLevel(),                            // 动态日志级别
)
