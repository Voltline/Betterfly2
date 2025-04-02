package logger_config

import (
	"go.uber.org/zap/zapcore"
	"os"
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

var CoreConfig zapcore.Core = zapcore.NewCore(
	zapcore.NewConsoleEncoder(encoderConfig), // 使用控制台编码器
	zapcore.AddSync(os.Stdout),               // 输出到控制台
	zapcore.DebugLevel,                       // 日志级别
)
