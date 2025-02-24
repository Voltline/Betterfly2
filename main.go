package main

import (
	"Betterfly-Server-Go/Utils"
	"fmt"
)

func main() {
	fmt.Println(Utils.Login)
	config, err := Utils.NewConfig("./Config/config.json")
	if err != nil {
		fmt.Println(err)
	} else {
		fmt.Println(config)
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
