package logger

import (
	"log"
	"os"
)

const (
	BLUE   = "\033[94m"
	GREEN  = "\033[92m"
	YELLOW = "\033[93m"
	RED    = "\033[91m"
	END    = "\033[0m"
)

type Logger struct {
	Debug *log.Logger
	Info  *log.Logger
	Warn  *log.Logger
	Error *log.Logger
	Fatal *log.Logger
}

func NewLogger() *Logger {
	flags := log.LstdFlags | log.Lshortfile
	return &Logger{
		Debug: log.New(os.Stdout, END+BLUE+"[DEBUG] ", flags),
		Info:  log.New(os.Stdout, END+GREEN+"[INFO] ", flags),
		Warn:  log.New(os.Stdout, END+YELLOW+"[WARN] ", flags),
		Error: log.New(os.Stdout, END+RED+"[ERROR] ", flags),
		Fatal: log.New(os.Stdout, END+RED+"[FATAL] ", flags),
	}
}
