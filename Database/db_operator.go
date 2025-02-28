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
		// TODO: 后续可能需要使用分布式数据库取代mysql
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

func (h *DatabaseHandler) Prepare(query string) (*sql.Stmt, error) {
	return h.db.Prepare(query)
}

func (h *DatabaseHandler) Close() {
	h.db.Close()
}

func (h *DatabaseHandler) Login(userId int, userName, lastLogin string) error {
	// 用户登录函数
	// 返回登录时的错误
	if userId < 1000 {
		return fmt.Errorf("userID 不得小于1000")
	}
	stmt, err := h.Prepare("CALL login(?,?,?);")
	if err != nil {
		return err
	}
	defer stmt.Close()
	_, err = stmt.Exec(userId, userName, lastLogin)
	if err != nil {
		return err
	}
	return nil
}

func (h *DatabaseHandler) QueryUser(userId int) (string, error) {
	// 查询用户信息函数
	// 返回用户昵称.用户头像
	stmt, err := h.Prepare("CALL query_user(?);")
	if err != nil {
		return "", err
	}
	userRows, err := stmt.Query(userId)
	if err != nil {
		return "", err
	}
	defer userRows.Close()
	for userRows.Next() {
		ans := ""
		var userName, userAvatar sql.NullString
		userRows.Scan(&userName, &userAvatar)
		if userName.Valid {
			ans += userName.String
		}
		ans += "."
		if userAvatar.Valid {
			ans += userAvatar.String
		}
		return ans, nil
	}
	return ".", nil
}

func (h *DatabaseHandler) QueryUserName(UserId int) (string, error) {
	// 根据用户ID查询用户昵称
	stmt, err := h.Prepare("CALL query_user_name(?);")
	if err != nil {
		return "", err
	}
	userRows, err := stmt.Query(UserId)
	if err != nil {
		return "", err
	}
	defer userRows.Close()
	for userRows.Next() {
		var userName sql.NullString
		userRows.Scan(&userName.String)
		if userName.Valid {
			return userName.String, nil
		}
	}
	return "", nil
}

func (h *DatabaseHandler) InsertContact(UserId1, UserId2 int) {
	// 增加联系人
	stmt, err := h.Prepare("CALL insert_contact(?,?);")
	if err != nil {
		return
	}
	defer stmt.Close()
	stmt.Exec(UserId1, UserId2)
}

func (h *DatabaseHandler) InsertGroup(GroupId int, GroupName string) {
	// 创建新Group
	stmt, err := h.Prepare("CALL insert_group(?,?);")
	if err != nil {
		return
	}
	defer stmt.Close()
	stmt.Exec(GroupId, GroupName)
}

func (h *DatabaseHandler) InsertGroupUser(GroupId, UserId int) {
	// 向Group中添加User
	stmt, err := h.Prepare("CALL insert_group_user(?,?);")
	if err != nil {
		return
	}
	defer stmt.Close()
	stmt.Exec(GroupId, UserId)
}

func (h *DatabaseHandler) InsertMessage(FromUserId, ToId int,
	Timestamp, Text, Type string, IsGroup bool) {
	// 保存消息到服务器数据库
	stmt, err := h.Prepare("CALL insert_message(?,?,?,?,?);")
	if err != nil {
		return
	}
	defer stmt.Close()
	stmt.Exec(FromUserId, ToId, Timestamp, Text, Type, IsGroup)
}

func (h *DatabaseHandler) QueryFile(FileHash, FileSuffix string) bool {
	// 查询文件是否存在
	stmt, err := h.Prepare("CALL query_file(?,?);")
	if err != nil {
		return false
	}
	defer stmt.Close()
	rows, err := stmt.Query(FileHash, FileSuffix)
	if err != nil {
		return false
	}
	defer rows.Close()
	return rows.Next()
}

func (h *DatabaseHandler) InsertFile(FileHash, FileSuffix string) {
	// 向数据库中插入文件信息
	stmt, err := h.Prepare("CALL insert_file(?,?);")
	if err != nil {
		return
	}
	defer stmt.Close()
	stmt.Exec(FileHash, FileSuffix)
}

func (h *DatabaseHandler) QuerySyncMessage(UserId int, LastLogin string) (*sql.Rows, error) {
	// 查询离线期间收到的消息
	stmt, err := h.Prepare("CALL query_sync_message(?,?);")
	if err != nil {
		return nil, err
	}
	defer stmt.Close()
	rows, err := stmt.Query(UserId, LastLogin)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

func (h *DatabaseHandler) QueryGroupUser(GroupId int) ([]int, error) {
	stmt, err := h.Prepare("CALL query_group_user(?);")
	if err != nil {
		return nil, err
	}
	defer stmt.Close()
	rows, err := stmt.Query(GroupId)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]int, 0)
	for rows.Next() {
		var userId int
		rows.Scan(&userId)
		result = append(result, userId)
	}
	return result, nil
}

func (h *DatabaseHandler) InsertUserAPNsToken(FromUserId int, UserApnsToken string) {
	// 保存用户的APNs Token
	stmt, err := h.Prepare("CALL insert_user_apns_token(?,?);")
	if err != nil {
		return
	}
	defer stmt.Close()
	stmt.Exec(FromUserId, UserApnsToken)
}

func (h *DatabaseHandler) QueryUserApnsTokens(FromUserId int) ([]string, error) {
	stmt, err := h.Prepare("CALL query_user_apns_tokens(?);")
	if err != nil {
		return nil, err
	}
	defer stmt.Close()
	rows, err := stmt.Query(FromUserId)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]string, 0)
	for rows.Next() {
		var token string
		rows.Scan(&token)
		result = append(result, token)
	}
	return result, nil
}

func (h *DatabaseHandler) DeleteUserApnsToken(FromUserId int, UserApnsToken string) {
	stmt, err := h.Prepare("CALL delete_user_apns_token(?,?);")
	if err != nil {
		return
	}
	defer stmt.Close()
	stmt.Exec(FromUserId, UserApnsToken)
}

func (h *DatabaseHandler) UpdateUserAvatar(Id int, Avatar string) {
	stmt, err := h.Prepare("CALL update_user_avatar(?,?);")
	if err != nil {
		return
	}
	defer stmt.Close()
	stmt.Exec(Id, Avatar)
}

func (h *DatabaseHandler) UpdateGroupAvatar(Id int, Avatar string) {
	stmt, err := h.Prepare("CALL update_group_avatar(?,?);")
	if err != nil {
		return
	}
	defer stmt.Close()
	stmt.Exec(Id, Avatar)
}
