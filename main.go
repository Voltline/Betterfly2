package main

import (
	"Betterfly-Server-Go/Database"
	"Betterfly-Server-Go/Utils"
	"database/sql"
	"time"
)

func test() {
	logger := Utils.NewLogger()
	logger.Info.Println(Utils.Login)
	config, err := Utils.NewConfig("./Config/config.json")
	if err != nil {
		logger.Error.Println(err)
	} else {
		logger.Info.Println(config)
	}

	cosconfig, err := Utils.NewCOSConfig("./Config/cos_config.json")
	if err != nil {
		logger.Error.Println(err)
	} else {
		logger.Info.Println(cosconfig)
	}

	dbsetting, err := Database.NewDBSetting("./Config/database_config.json")
	if err != nil {
		logger.Error.Println(err)
	} else {
		logger.Info.Println(dbsetting)
	}

	logger.Info.Println(time.Now())

	msg := "{\"type\":123,\n\"from\": 10213901414,\n\"name\": \"Voltline\",\n\"timestamp\":\"2025-02-25 10:20:34\"}"
	logger.Info.Println(msg)
	rmsg, err := Utils.NewRequestMessage(msg)
	if err != nil {
		logger.Error.Println(err)
	} else {
		logger.Info.Println(rmsg)
	}

	str1, err := Utils.MakeHelloMessage(123, 456, "Voltline", false, "")
	if err != nil {
		logger.Error.Println(err)
	} else {
		logger.Info.Println(str1)
	}

	logger.Info.Println("----Encoding and Decoding----")
	s1 := "Hello, world!"
	s2 := "Good world!"
	s3, s4 := string(Utils.Encode(s1)), string(Utils.Encode(s2))
	logger.Info.Println(s3, s4)
	s5 := []byte(s3 + s4)
	logger.Info.Printf("%q\n", Utils.Decode(s5))
	message, err := Utils.MakeDownloadMessage("qq.dmg", "aaa")
	if err != nil {
		logger.Error.Println(err)
	} else {
		logger.Info.Println(string(Utils.Encode(message)))
		logger.Info.Println(message)
		logger.Info.Println(Utils.Decode(Utils.Encode(message)))
	}
}

func main() {
	test()
	logger := Utils.NewLogger()
	logger.Info.Println("Hello World")
	logger.Error.Println("Error!")
	db, err := Database.GetDatabaseHandler()
	if err != nil {
		logger.Error.Println(err)
	}
	defer db.Close()
	row, err := db.Query("select * from users where users.user_id >= 10000 limit 3")
	if err != nil {
		logger.Error.Println(err)
	}
	defer row.Close()
	for row.Next() {
		var user_id int
		var user_name string
		var salt, auth_string sql.NullString
		var last_login, update_time []uint8
		var user_avatar sql.NullString
		if err := row.Scan(&user_id, &user_name, &salt, &auth_string, &last_login, &update_time, &user_avatar); err != nil {
			logger.Error.Println(err)
		}
		u_avatar := "NULL"
		if user_avatar.Valid {
			u_avatar = user_avatar.String
		}
		logger.Info.Printf("user_id: %d, user_name: %s, last_login: %s, update_time: %s, user_avatar: %s\n",
			user_id, user_name, last_login, update_time, u_avatar)
	}

	info, err := db.QuerySyncMessage(10000, "1970-01-01 00:00:00")
	if err != nil {
		logger.Error.Println(err)
	} else {
		for info.Next() {
			var from_user_id, to_id int
			var timestamp string
			var text, tp string
			var is_group int
			if err := info.Scan(&from_user_id, &to_id, &timestamp, &text, &tp, &is_group); err != nil {
				logger.Error.Println(err)
			} else {
				logger.Info.Println(from_user_id, to_id, timestamp, text, tp, is_group)
			}
		}
	}

	//wp := Utils.NewWorkerPool(10, func(conn int) {
	//	fmt.Println(conn)
	//})
	//
	//for i := 0; i < 10; i++ {
	//	wp.Submit(i)
	//}
	//
	//wp.Shutdown()
}
