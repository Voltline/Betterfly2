package platform

import (
	"strings"
	"unicode"

	"Betterfly2/shared/db"
)

// Update 是交给第三方机器人的一条更新，message与event二选一。
type Update struct {
	UpdateID int64          `json:"update_id"`
	Message  *UpdateMessage `json:"message,omitempty"`
	Event    *UpdateEvent   `json:"event,omitempty"`
}

type Chat struct {
	ID    int64  `json:"id"`
	Type  string `json:"type"` // private或group
	Title string `json:"title,omitempty"`
}

type UpdateMessage struct {
	MessageID        int64      `json:"message_id"`
	Chat             Chat       `json:"chat"`
	From             UserBrief  `json:"from"`
	Date             string     `json:"date"`
	Type             string     `json:"type"`
	Text             string     `json:"text,omitempty"`
	Caption          string     `json:"caption,omitempty"`
	File             *FileBrief `json:"file,omitempty"`
	ReplyToMessageID int64      `json:"reply_to_message_id,omitempty"`
	// Command 是消息开头的命令名（不含@机器人），例如/start；参数留在text里。
	Command string `json:"command,omitempty"`
}

type FileBrief struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name,omitempty"`
}

type UpdateEvent struct {
	Type string    `json:"type"` // friend_added / group_joined / friend_removed / group_removed
	User UserBrief `json:"user"`
	Chat *Chat     `json:"chat,omitempty"`
	Date string    `json:"date"`
}

const (
	ChatPrivate = "private"
	ChatGroup   = "group"
)

// normalizeMention 去掉空白并转小写，“@Betterfly 助手”和“@betterfly助手”视为同一个提及。
func normalizeMention(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return unicode.ToLower(r)
	}, text)
}

// mentions 判断文字里是否@了机器人的昵称或账号。
func mentions(text string, bot *BotInfo) bool {
	normalized := normalizeMention(text)
	if normalized == "" {
		return false
	}
	if name := normalizeMention("@" + bot.Name); len(name) > 1 && strings.Contains(normalized, name) {
		return true
	}
	return strings.Contains(normalized, "@"+strings.ToLower(bot.Account))
}

// parseCommand 识别消息开头的/命令。返回命令名（含/）与它指向的机器人账号（/cmd@account），
// 不是命令时返回空串。命令名只允许字母、数字和下划线，遇到其他字符即结束，
// 因此“/weather上海”也是/weather，参数紧跟在后面。
func parseCommand(text string) (command, target string) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") {
		return "", ""
	}
	name := commandWord(text[1:])
	if name == "" || len(name) > 32 {
		return "", ""
	}
	if rest := text[1+len(name):]; strings.HasPrefix(rest, "@") {
		target = commandWord(rest[1:])
	}
	return "/" + name, strings.ToLower(target)
}

// commandWord 返回开头由字母、数字和下划线组成的部分。
func commandWord(text string) string {
	end := strings.IndexFunc(text, func(r rune) bool {
		return r != '_' && (r < '0' || r > '9') && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z')
	})
	if end < 0 {
		return text
	}
	return text[:end]
}

// messageText 返回可能带@或命令的文字：文字消息取正文，图片取说明。
func messageText(message *db.Message) string {
	switch message.MessageType {
	case "text", "link":
		return message.Content
	case "image":
		return message.Caption
	}
	return ""
}

// fileID 是机器人收发文件时使用的标识，与消息content一致：<sha512>.<后缀>。
func fileID(message *db.Message) *FileBrief {
	if !db.IsFileMessageType(message.MessageType) {
		return nil
	}
	if _, ok := db.FileHashFromContent(message.Content); !ok {
		return nil
	}
	return &FileBrief{FileID: message.Content, FileName: message.RealFileName}
}
