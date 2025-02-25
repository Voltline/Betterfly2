package Utils

import (
	"encoding/json"
	"io"
	"os"
)

type Config struct {
	IP   string `json:"ip"`
	Port int    `json:"port"`
}

func NewConfig(filePath string) (*Config, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	data, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}

	var config Config
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, err
	}
	return &config, nil
}

type COSConfig struct {
	SecretID  string `json:"secret_id"`
	SecretKey string `json:"secret_key"`
	Region    string `json:"region"`
}

func NewCOSConfig(filePath string) (*COSConfig, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	data, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}

	var cos COSConfig
	if err := json.Unmarshal(data, &cos); err != nil {
		return nil, err
	}
	return &cos, nil
}
