package Database

import (
	"encoding/json"
	"io"
	"os"
)

type DBSetting struct {
	User     string `json:"user"`
	Password string `json:"password"`
	IP       string `json:"ip"`
	Port     int    `json:"port"`
	Database string `json:"database"`
	Charset  string `json:"charset"`
}

func NewDBSetting(filePath string) (*DBSetting, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	data, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}

	var dbsetting DBSetting
	if err := json.Unmarshal(data, &dbsetting); err != nil {
		return nil, err
	}
	return &dbsetting, nil
}
