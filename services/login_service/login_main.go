package main

import (
	"Betterfly2/shared/db_op"
	"Betterfly2/shared/logger"
)

func main() {
	sugar := logger.Sugar()
	defer logger.Sync()
	sugar.Infoln("login服务启动中...")
	db_op.DB()
	user, err := db_op.GetUserById(0)
	if err != nil {
		sugar.Errorln(err)
	} else {
		sugar.Info(user)
	}
}
