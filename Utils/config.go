package Utils

import (
	"encoding/json"
	"os"
)

type Config struct {
	path string
	ip   string
	port int
}

func NewConfig(filePath string) Config {
	f, err := os.Open(filePath)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	decoder := json.NewDecoder(f)
	config := Config{}
	err = decoder.Decode(&config)
	if err != nil {
		panic(err)
	}
	return config
}
