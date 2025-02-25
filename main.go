package main

import (
	"Betterfly-Server-Go/Database"
	"Betterfly-Server-Go/Utils"
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
