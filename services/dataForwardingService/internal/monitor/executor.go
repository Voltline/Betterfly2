package monitor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

const (
	AdminUserID           int64 = 1
	DefaultMonitorID      int64 = 900000000000000001
	defaultMonitorName          = "Betterfly Monitor"
	defaultMonitorAccount       = "monitor"
)

var ErrForbidden = errors.New("monitor access denied")

type Profile struct {
	UserID     int64
	Account    string
	Name       string
	Avatar     string
	UpdateTime string
}

func CurrentProfile() Profile {
	userID := DefaultMonitorID
	if parsed, err := strconv.ParseInt(strings.TrimSpace(os.Getenv("MONITOR_USER_ID")), 10, 64); err == nil && parsed > 0 && parsed != AdminUserID {
		userID = parsed
	}
	name := strings.TrimSpace(os.Getenv("MONITOR_NAME"))
	if name == "" {
		name = defaultMonitorName
	}
	return Profile{
		UserID:     userID,
		Account:    defaultMonitorAccount,
		Name:       name,
		Avatar:     strings.TrimSpace(os.Getenv("MONITOR_AVATAR")),
		UpdateTime: "1970-01-01T00:00:00Z",
	}
}

func IsMonitorID(userID int64) bool {
	return userID == CurrentProfile().UserID
}

type Actions struct {
	Status      func(context.Context) (string, error)
	Connections func(context.Context) (string, error)
	Route       func(context.Context, int64) (string, error)
	Kick        func(context.Context, int64) (string, error)
	User        func(context.Context, int64) (string, error)
	Group       func(context.Context, int64) (string, error)
	Requests    func(context.Context, int64) (string, error)
}

type Executor struct {
	actions Actions
}

func NewExecutor(actions Actions) *Executor {
	return &Executor{actions: actions}
}

func (e *Executor) Execute(ctx context.Context, senderUserID int64, input string) (string, string, error) {
	if senderUserID != AdminUserID {
		return "", "", ErrForbidden
	}

	fields := strings.Fields(strings.TrimSpace(input))
	if len(fields) == 0 {
		return "help", helpText(), nil
	}

	command := strings.ToLower(fields[0])
	switch command {
	case "/help", "help":
		return "help", helpText(), nil
	case "/status", "status":
		return e.runNoArg(ctx, "status", fields, e.actions.Status)
	case "/connections", "connections":
		return e.runNoArg(ctx, "connections", fields, e.actions.Connections)
	case "/route", "route":
		return e.runUserAction(ctx, "route", fields, e.actions.Route, false)
	case "/kick", "kick":
		return e.runUserAction(ctx, "kick", fields, e.actions.Kick, true)
	case "/user", "user":
		return e.runUserAction(ctx, "user", fields, e.actions.User, false)
	case "/group", "group":
		return e.runUserAction(ctx, "group", fields, e.actions.Group, false)
	case "/requests", "requests":
		return e.runUserAction(ctx, "requests", fields, e.actions.Requests, false)
	default:
		return "unknown", "未知指令。发送 /help 查看允许的指令。", nil
	}
}

func (e *Executor) runNoArg(ctx context.Context, name string, fields []string, action func(context.Context) (string, error)) (string, string, error) {
	if len(fields) != 1 {
		return name, fmt.Sprintf("用法：/%s", name), nil
	}
	if action == nil {
		return name, "该能力当前不可用。", nil
	}
	result, err := action(ctx)
	return name, result, err
}

func (e *Executor) runUserAction(ctx context.Context, name string, fields []string, action func(context.Context, int64) (string, error), mutating bool) (string, string, error) {
	if len(fields) != 2 {
		return name, fmt.Sprintf("用法：/%s <id>", name), nil
	}
	userID, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil || userID <= 0 {
		return name, "id 必须是正整数。", nil
	}
	if mutating && (userID == AdminUserID || IsMonitorID(userID)) {
		return name, "拒绝操作：不能踢出超级管理员或 Monitor。", nil
	}
	if action == nil {
		return name, "该能力当前不可用。", nil
	}
	result, err := action(ctx, userID)
	return name, result, err
}

func helpText() string {
	return strings.Join([]string{
		"Betterfly Monitor 指令：",
		"/status - 查看各服务与基础设施状态",
		"/connections - 查看本 Pod 与全局在线连接数",
		"/route <user_id> - 查询用户当前所在 DF Pod",
		"/kick <user_id> - 强制断开用户当前 WebSocket 会话",
		"/user <user_id> - 查看用户资料、好友数及已加入群角色",
		"/group <group_id> - 查看群资料、成员与角色分布",
		"/requests <user_id> - 查看用户当前待处理关系申请",
		"/help - 显示本帮助",
		"",
		"Monitor 不执行 shell、SQL 或任意网络请求。",
	}, "\n")
}
