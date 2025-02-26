package Database

import (
	"database/sql"
	"fmt"
	_ "github.com/go-sql-driver/mysql"
	"sync"
)

type DatabaseHandler struct {
	db *sql.DB
}

var (
	instance *DatabaseHandler
	once     sync.Once
	settings *DBSetting
)

func GetDatabaseHandler() (*DatabaseHandler, error) {
	var initErr error
	once.Do(func() {
		s, err := NewDBSetting("./Config/database_config.json")
		if err != nil {
			initErr = err
			return
		}
		settings = s

		// 配置连接信息并初始化连接
		dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s",
			settings.User, settings.Password, settings.IP,
			settings.Port, settings.Database)
		db, err := sql.Open("mysql", dsn)
		if err != nil {
			initErr = err
			return
		}

		// 测试连接
		if err = db.Ping(); err != nil {
			initErr = err
			return
		}

		// 配置连接池
		db.SetMaxIdleConns(64) // 最大打开连接数64
		db.SetMaxOpenConns(64) // 最大空闲连接数64

		instance = &DatabaseHandler{db: db}
	})
	if initErr != nil {
		return nil, initErr
	}
	return instance, nil
}

func (h *DatabaseHandler) Query(query string, args ...interface{}) (*sql.Rows, error) {
	return h.db.Query(query, args...)
}

func (h *DatabaseHandler) Exec(query string, args ...interface{}) (sql.Result, error) {
	return h.db.Exec(query, args...)
}

func (h *DatabaseHandler) Close() {
	h.db.Close()
}
