package platform

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	envelope "Betterfly2/proto/envelope"
	storage "Betterfly2/proto/storage"
	"Betterfly2/shared/db"
	"Betterfly2/shared/logger"
	"Betterfly2/shared/mq"
	"Betterfly2/shared/utils"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

const (
	tokenPrefix        = "bfbot_"
	maxJSONBody        = 64 << 10
	maxDescriptionLen  = 512
	maxUpdatesPerPoll  = 100
	sendPollInterval   = 100 * time.Millisecond
	pollRecheckEvery   = 2 * time.Second
	maxWebhookURLBytes = 512
	maxSecretBytes     = 256
)

var apiRequests = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "betterfly_chatbot_api_requests_total",
	Help: "Bot API请求结果",
}, []string{"route", "status"})

// NewToken 生成机器人令牌；数据库只保存它的SHA-256。
func NewToken() (token, tokenHash string, err error) {
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", "", err
	}
	token = tokenPrefix + base64.RawURLEncoding.EncodeToString(random[:])
	return token, HashToken(token), nil
}

func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

type apiError struct {
	status  int
	code    string
	message string
}

func (e *apiError) Error() string { return e.message }

func fail(status int, code, message string) *apiError {
	return &apiError{status: status, code: code, message: message}
}

var (
	errUnauthorized  = fail(http.StatusUnauthorized, "unauthorized", "令牌无效")
	errBotDisabled   = fail(http.StatusForbidden, "bot_disabled", "机器人已停用")
	errFilesDisabled = fail(http.StatusServiceUnavailable, "files_disabled", "服务端未配置文件存储")
)

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeResult(w http.ResponseWriter, status int, result any) {
	writeJSON(w, status, map[string]any{"ok": true, "result": result})
}

func writeError(w http.ResponseWriter, err error) {
	var failure *apiError
	if !errors.As(err, &failure) {
		failure = fail(http.StatusInternalServerError, "internal", "服务器内部错误")
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			failure = fail(http.StatusServiceUnavailable, "unavailable", "服务暂时不可用")
		}
	}
	if failure.status == http.StatusServiceUnavailable {
		w.Header().Set("Retry-After", "2")
	}
	writeJSON(w, failure.status, map[string]any{"ok": false, "error": map[string]string{"code": failure.code, "message": failure.message}})
}

func decodeJSON(r *http.Request, target any) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, maxJSONBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fail(http.StatusBadRequest, "invalid_json", "请求体不是有效的JSON: "+err.Error())
	}
	return nil
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

type botHandler func(w http.ResponseWriter, r *http.Request, bot *BotInfo) error

// withBot 校验Bearer令牌，并按路由记录结果。
func (p *Platform) withBot(route string, handler botHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		err := p.authenticate(r, func(bot *BotInfo) error { return handler(recorder, r, bot) })
		if err != nil {
			var failure *apiError
			if !errors.As(err, &failure) {
				logger.Sugar().Warnw("Bot API请求失败", "route", route, "error", err)
			}
			writeError(recorder, err)
		}
		apiRequests.WithLabelValues(route, strconv.Itoa(recorder.status)).Inc()
	}
}

func (p *Platform) authenticate(r *http.Request, next func(*BotInfo) error) error {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	token = strings.TrimSpace(token)
	if !ok || !strings.HasPrefix(token, tokenPrefix) {
		return errUnauthorized
	}
	bot, err := p.store.BotByTokenHash(r.Context(), HashToken(token))
	if err != nil {
		return err
	}
	if bot == nil || bot.Delivery != db.BotDeliveryAPI {
		return errUnauthorized
	}
	if bot.Disabled {
		return errBotDisabled
	}
	return next(bot)
}

// Routes 注册Bot API。
func (p *Platform) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /bot/v1/me", p.withBot("me", p.handleMe))
	mux.HandleFunc("PATCH /bot/v1/me", p.withBot("update_me", p.handleUpdateMe))
	mux.HandleFunc("GET /bot/v1/updates", p.withBot("updates", p.handleUpdates))
	mux.HandleFunc("POST /bot/v1/messages", p.withBot("send", p.handleSend))
	mux.HandleFunc("POST /bot/v1/files", p.withBot("upload", p.handleUpload))
	mux.HandleFunc("GET /bot/v1/files/{file_id}", p.withBot("download", p.handleDownload))
	mux.HandleFunc("GET /bot/v1/webhook", p.withBot("webhook", p.handleGetWebhook))
	mux.HandleFunc("PUT /bot/v1/webhook", p.withBot("set_webhook", p.handleSetWebhook))
	mux.HandleFunc("DELETE /bot/v1/webhook", p.withBot("delete_webhook", p.handleDeleteWebhook))
}

type botProfile struct {
	ID          int64  `json:"id"`
	Account     string `json:"account"`
	Name        string `json:"name"`
	Avatar      string `json:"avatar,omitempty"`
	Description string `json:"description,omitempty"`
	AllowGroups bool   `json:"allow_groups"`
}

func profile(bot *BotInfo) botProfile {
	return botProfile{ID: bot.UserID, Account: bot.Account, Name: bot.Name, Avatar: bot.Avatar, Description: bot.Description, AllowGroups: bot.AllowGroups}
}

func (p *Platform) handleMe(w http.ResponseWriter, _ *http.Request, bot *BotInfo) error {
	writeResult(w, http.StatusOK, profile(bot))
	return nil
}

type profilePatch struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
	Avatar      *string `json:"avatar"` // file_id，或空串清除头像
}

// validateProfile 校验名字、简介与头像；头像必须是该机器人能使用的已校验文件。
func (p *Platform) validateProfile(ctx context.Context, botID int64, name, description, avatar *string) (*string, error) {
	if name != nil {
		trimmed := strings.TrimSpace(*name)
		if trimmed == "" || !utils.ValidText(trimmed, 50) {
			return nil, fail(http.StatusBadRequest, "invalid_name", "名字必须是1到50个字符")
		}
		*name = trimmed
	}
	if description != nil && !utils.ValidText(*description, maxDescriptionLen) {
		return nil, fail(http.StatusBadRequest, "invalid_description", "简介不超过512个字符")
	}
	if avatar == nil || *avatar == "" {
		return avatar, nil
	}
	fileHash, ok := db.FileHashFromContent(*avatar)
	if !ok {
		return nil, fail(http.StatusBadRequest, "invalid_avatar", "头像必须是file_id")
	}
	allowed := false
	var err error
	if botID > 0 {
		allowed, err = p.store.CanUseFile(ctx, fileHash, botID)
	} else {
		_, _, allowed, err = p.store.VerifiedFile(ctx, fileHash)
	}
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, fail(http.StatusBadRequest, "invalid_avatar", "头像文件不存在或没有使用权限")
	}
	return &fileHash, nil
}

func (p *Platform) handleUpdateMe(w http.ResponseWriter, r *http.Request, bot *BotInfo) error {
	var patch profilePatch
	if err := decodeJSON(r, &patch); err != nil {
		return err
	}
	avatar, err := p.validateProfile(r.Context(), bot.UserID, patch.Name, patch.Description, patch.Avatar)
	if err != nil {
		return err
	}
	if err := p.store.UpdateBot(r.Context(), bot.UserID, BotPatch{Name: patch.Name, Description: patch.Description, Avatar: avatar}); err != nil {
		return err
	}
	updated, err := p.store.Bot(r.Context(), bot.UserID)
	if err != nil {
		return err
	}
	writeResult(w, http.StatusOK, profile(updated))
	return nil
}

func queryInt(r *http.Request, key string, fallback, minimum, maximum int64) (int64, error) {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < minimum || value > maximum {
		return 0, fail(http.StatusBadRequest, "invalid_"+key, fmt.Sprintf("%s必须是%d到%d之间的整数", key, minimum, maximum))
	}
	return value, nil
}

// handleUpdates 实现长轮询：offset确认之前的更新，timeout秒内没有新更新时返回空数组。
func (p *Platform) handleUpdates(w http.ResponseWriter, r *http.Request, bot *BotInfo) error {
	if bot.WebhookURL != "" {
		return fail(http.StatusConflict, "webhook_active", "已设置webhook，不能同时使用getUpdates")
	}
	offset, err := queryInt(r, "offset", 0, 0, 1<<62)
	if err != nil {
		return err
	}
	limit, err := queryInt(r, "limit", maxUpdatesPerPoll, 1, maxUpdatesPerPoll)
	if err != nil {
		return err
	}
	timeout, err := queryInt(r, "timeout", 0, 0, int64(p.config.MaxPollTimeout/time.Second))
	if err != nil {
		return err
	}
	deadline := p.now().Add(time.Duration(timeout) * time.Second)
	for {
		woken := p.waiters.wait(bot.UserID)
		rows, err := p.store.PendingUpdates(r.Context(), bot.UserID, offset, int(limit))
		if err != nil {
			return err
		}
		remaining := deadline.Sub(p.now())
		if len(rows) > 0 || remaining <= 0 {
			updates, err := decodeUpdates(rows)
			if err != nil {
				return err
			}
			writeResult(w, http.StatusOK, updates)
			return nil
		}
		timer := time.NewTimer(min(remaining, pollRecheckEvery))
		select {
		case <-woken:
		case <-timer.C:
		case <-r.Context().Done():
			timer.Stop()
			return nil
		}
		timer.Stop()
	}
}

func decodeUpdates(rows []db.BotUpdate) ([]Update, error) {
	updates := make([]Update, 0, len(rows))
	for _, row := range rows {
		var update Update
		if err := json.Unmarshal([]byte(row.Payload), &update); err != nil {
			return nil, fmt.Errorf("解析更新%d失败: %w", row.UpdateID, err)
		}
		update.UpdateID = row.UpdateID
		updates = append(updates, update)
	}
	return updates, nil
}

type sendRequest struct {
	ChatID           int64  `json:"chat_id"`
	ChatType         string `json:"chat_type"`
	Type             string `json:"type"`
	Text             string `json:"text"`
	FileID           string `json:"file_id"`
	FileName         string `json:"file_name"`
	Caption          string `json:"caption"`
	ReplyToMessageID int64  `json:"reply_to_message_id"`
	ClientMessageID  string `json:"client_message_id"`
}

type sentMessage struct {
	MessageID       int64  `json:"message_id,omitempty"`
	ClientMessageID string `json:"client_message_id"`
	Chat            Chat   `json:"chat"`
	Date            string `json:"date,omitempty"`
	Pending         bool   `json:"pending,omitempty"`
}

var sendableFileTypes = map[string]bool{"image": true, "gif": true, "video": true, "audio": true, "voice": true, "file": true}

// buildStoreMessage 校验发送请求并生成入库请求；权限检查在checkSend里做。
func buildStoreMessage(botID int64, request *sendRequest) (*storage.StoreNewMessage, error) {
	if request.ChatID <= 0 || (request.ChatType != ChatPrivate && request.ChatType != ChatGroup) {
		return nil, fail(http.StatusBadRequest, "invalid_chat", "chat_id必须为正数，chat_type为private或group")
	}
	if request.Type == "" {
		request.Type = "text"
		if request.FileID != "" {
			request.Type = "file"
		}
	}
	message := &storage.StoreNewMessage{
		FromUserId: botID, ToUserId: request.ChatID, IsGroup: request.ChatType == ChatGroup, MessageType: request.Type,
		ReplyToMessageId: request.ReplyToMessageID, ClientMessageId: strings.TrimSpace(request.ClientMessageID), ClientTimestamp: utils.NowTime(),
	}
	switch {
	case request.Type == "text":
		if strings.TrimSpace(request.Text) == "" || request.FileID != "" || request.Caption != "" {
			return nil, fail(http.StatusBadRequest, "invalid_text", "文字消息需要text，且不能带file_id或caption")
		}
		message.Content = request.Text
	case sendableFileTypes[request.Type]:
		if _, ok := db.FileHashFromContent(request.FileID); !ok || request.Text != "" {
			return nil, fail(http.StatusBadRequest, "invalid_file", "文件消息需要POST /bot/v1/files返回的file_id，且不能带text")
		}
		if err := utils.ValidateImageCaption(request.Type, request.Caption); err != nil {
			return nil, fail(http.StatusBadRequest, "invalid_caption", "只有图片可以带caption，且不超过长度限制")
		}
		message.Content, message.Caption, message.RealFileName = request.FileID, request.Caption, request.FileName
	default:
		return nil, fail(http.StatusBadRequest, "invalid_type", "type必须是text、image、gif、video、audio、voice或file")
	}
	if message.ClientMessageId == "" {
		var random [12]byte
		if _, err := rand.Read(random[:]); err != nil {
			return nil, err
		}
		message.ClientMessageId = "botapi:" + hex.EncodeToString(random[:])
	}
	if request.ReplyToMessageID < 0 || !db.ValidMessageFields(message.Content, message.MessageType, message.RealFileName, message.ClientMessageId) {
		return nil, fail(http.StatusBadRequest, "invalid_message", "消息内容、文件名或client_message_id超过长度限制")
	}
	return message, nil
}

// checkSend 在发布前校验权限，Storage拒绝时机器人收不到原因，只会等到超时。
func (p *Platform) checkSend(ctx context.Context, bot *BotInfo, message *storage.StoreNewMessage) error {
	if message.GetIsGroup() {
		if !bot.AllowGroups {
			return fail(http.StatusForbidden, "groups_disabled", "该机器人未开启群聊")
		}
		allowed, err := p.store.CanPublishGroup(ctx, message.GetToUserId(), bot.UserID)
		if err != nil {
			return err
		}
		if !allowed {
			return fail(http.StatusForbidden, "not_member", "机器人不在该群或没有发言权限")
		}
	} else {
		friendOK := false
		var err error
		if !db.IsBotUserID(message.GetToUserId()) {
			friendOK, err = p.store.IsFriend(ctx, bot.UserID, message.GetToUserId())
		}
		if err != nil {
			return err
		}
		if !friendOK {
			return fail(http.StatusForbidden, "not_friend", "只能给已添加机器人为好友的用户发私聊")
		}
	}
	if db.IsFileMessageType(message.GetMessageType()) {
		fileHash, _ := db.FileHashFromContent(message.GetContent())
		allowed, err := p.store.CanUseFile(ctx, fileHash, bot.UserID)
		if err != nil {
			return err
		}
		if !allowed {
			return fail(http.StatusForbidden, "file_not_owned", "file_id不是该机器人上传的文件")
		}
	}
	if replyID := message.GetReplyToMessageId(); replyID > 0 {
		reply, err := p.store.Message(ctx, replyID)
		if err != nil {
			return err
		}
		sameChat := reply != nil && reply.IsGroup == message.GetIsGroup() && !reply.IsRecalled
		if sameChat && message.GetIsGroup() {
			sameChat = reply.ToUserID == message.GetToUserId() && reply.DiscussionRootMessageID == 0
		} else if sameChat {
			sameChat = (reply.FromUserID == bot.UserID && reply.ToUserID == message.GetToUserId()) ||
				(reply.FromUserID == message.GetToUserId() && reply.ToUserID == bot.UserID)
		}
		if !sameChat {
			return fail(http.StatusBadRequest, "invalid_reply", "reply_to_message_id不是该会话里的消息")
		}
	}
	return nil
}

func (p *Platform) handleSend(w http.ResponseWriter, r *http.Request, bot *BotInfo) error {
	var request sendRequest
	if err := decodeJSON(r, &request); err != nil {
		return err
	}
	message, err := buildStoreMessage(bot.UserID, &request)
	if err != nil {
		return err
	}
	chat := Chat{ID: message.GetToUserId(), Type: request.ChatType}
	if existing, err := p.store.SentMessage(r.Context(), bot.UserID, message.GetClientMessageId()); err != nil {
		return err
	} else if existing != nil {
		writeResult(w, http.StatusOK, sentMessage{MessageID: existing.MessageID, ClientMessageID: message.GetClientMessageId(), Chat: chat, Date: existing.Timestamp})
		return nil
	}
	if err := p.checkSend(r.Context(), bot, message); err != nil {
		return err
	}
	payload, err := mq.MarshalEnvelope(envelope.MessageType_STORAGE_REQUEST, &storage.RequestMessage{
		FromKafkaTopic: p.config.ReplyTopic, TargetUserId: bot.UserID,
		Payload: &storage.RequestMessage_StoreNewMessage{StoreNewMessage: message},
	})
	if err != nil {
		return err
	}
	key := fmt.Sprintf("u:%d", message.GetToUserId())
	if message.GetIsGroup() {
		key = fmt.Sprintf("g:%d", message.GetToUserId())
	}
	if err := p.publish(r.Context(), p.config.StorageTopic, key, payload); err != nil {
		return fail(http.StatusServiceUnavailable, "unavailable", "消息队列暂时不可用")
	}
	// 等入库结果回来再返回message_id；超时只说明还在处理，用同一client_message_id重试不会重复发送。
	deadline := time.Now().Add(p.config.SendWait)
	for {
		stored, err := p.store.SentMessage(r.Context(), bot.UserID, message.GetClientMessageId())
		if err != nil {
			return err
		}
		if stored != nil {
			writeResult(w, http.StatusOK, sentMessage{MessageID: stored.MessageID, ClientMessageID: message.GetClientMessageId(), Chat: chat, Date: stored.Timestamp})
			return nil
		}
		if time.Now().After(deadline) {
			writeResult(w, http.StatusAccepted, sentMessage{ClientMessageID: message.GetClientMessageId(), Chat: chat, Pending: true})
			return nil
		}
		select {
		case <-r.Context().Done():
			return nil
		case <-time.After(sendPollInterval):
		}
	}
}

// fileSuffix 只保留短的字母数字后缀，客户端按它判断文件类型。
func fileSuffix(name string) string {
	suffix := strings.ToLower(strings.TrimPrefix(filepath.Ext(name), "."))
	if suffix == "" || len(suffix) > 10 || strings.Trim(suffix, "abcdefghijklmnopqrstuvwxyz0123456789") != "" {
		return ""
	}
	return suffix
}

type uploadedFile struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name,omitempty"`
	Size     int64  `json:"size"`
}

// handleUpload 接收multipart表单里名为file的文件，按SHA-512存入对象存储，并授予机器人使用权。
func (p *Platform) handleUpload(w http.ResponseWriter, r *http.Request, bot *BotInfo) error {
	if p.objects == nil {
		return errFilesDisabled
	}
	r.Body = http.MaxBytesReader(w, r.Body, p.config.MaxUploadBytes+1<<20)
	reader, err := r.MultipartReader()
	if err != nil {
		return fail(http.StatusBadRequest, "invalid_multipart", "需要multipart/form-data，字段名为file")
	}
	for {
		part, err := reader.NextPart()
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				return fail(http.StatusRequestEntityTooLarge, "file_too_large", "文件超过大小限制")
			}
			return fail(http.StatusBadRequest, "missing_file", "缺少名为file的文件字段")
		}
		if part.FormName() != "file" {
			_ = part.Close()
			continue
		}
		result, err := p.storeUpload(r.Context(), bot.UserID, part, part.FileName())
		_ = part.Close()
		if err != nil {
			return err
		}
		writeResult(w, http.StatusOK, result)
		return nil
	}
}

func (p *Platform) storeUpload(ctx context.Context, botID int64, body io.Reader, name string) (*uploadedFile, error) {
	temp, err := os.CreateTemp("", "bot-upload-*")
	if err != nil {
		return nil, err
	}
	defer func() { _ = temp.Close(); _ = os.Remove(temp.Name()) }()
	hasher := sha512.New()
	size, err := io.Copy(io.MultiWriter(temp, hasher), io.LimitReader(body, p.config.MaxUploadBytes+1))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return nil, fail(http.StatusRequestEntityTooLarge, "file_too_large", "文件超过大小限制")
		}
		return nil, fail(http.StatusBadRequest, "upload_interrupted", "读取上传文件失败")
	}
	if size > p.config.MaxUploadBytes {
		return nil, fail(http.StatusRequestEntityTooLarge, "file_too_large", "文件超过大小限制")
	}
	if size == 0 {
		return nil, fail(http.StatusBadRequest, "empty_file", "文件不能为空")
	}
	fileHash := hex.EncodeToString(hasher.Sum(nil))
	if _, _, exists, err := p.store.VerifiedFile(ctx, fileHash); err != nil {
		return nil, err
	} else if exists {
		// 已有同样内容的文件：上传了完整内容即证明持有，直接授权。
		if err := p.store.GrantFile(ctx, fileHash, botID); err != nil {
			return nil, err
		}
	} else {
		if _, err := temp.Seek(0, io.SeekStart); err != nil {
			return nil, err
		}
		if err := p.objects.Put(ctx, StoragePath(fileHash), temp, size); err != nil {
			logger.Sugar().Warnw("机器人文件写入对象存储失败", "bot_user_id", botID, "error", err)
			return nil, fail(http.StatusServiceUnavailable, "unavailable", "文件存储暂时不可用")
		}
		if err := p.store.StoreUploadedFile(ctx, fileHash, size, botID); err != nil {
			return nil, err
		}
	}
	id := fileHash
	if suffix := fileSuffix(name); suffix != "" {
		id += "." + suffix
	}
	if !utf8.ValidString(name) || len(name) > db.MaxRealFileNameLen {
		name = ""
	}
	return &uploadedFile{FileID: id, FileName: filepath.Base(name), Size: size}, nil
}

// handleDownload 下载机器人能看到的文件：自己上传的，或会话里收到的文件消息。
func (p *Platform) handleDownload(w http.ResponseWriter, r *http.Request, bot *BotInfo) error {
	if p.objects == nil {
		return errFilesDisabled
	}
	notFound := fail(http.StatusNotFound, "file_not_found", "文件不存在或没有下载权限")
	fileHash, ok := db.FileHashFromContent(r.PathValue("file_id"))
	if !ok {
		return notFound
	}
	allowed, err := p.store.CanDownloadFile(r.Context(), fileHash, bot.UserID)
	if err != nil {
		return err
	}
	if !allowed {
		return notFound
	}
	path, size, exists, err := p.store.VerifiedFile(r.Context(), fileHash)
	if err != nil {
		return err
	}
	if !exists {
		return notFound
	}
	body, err := p.objects.Open(r.Context(), path)
	if err != nil {
		logger.Sugar().Warnw("读取机器人文件失败", "bot_user_id", bot.UserID, "error", err)
		return fail(http.StatusServiceUnavailable, "unavailable", "文件存储暂时不可用")
	}
	defer body.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, body)
	return nil
}

type webhookInfo struct {
	URL       string     `json:"url"`
	HasSecret bool       `json:"has_secret"`
	Failures  int        `json:"failures"`
	LastError string     `json:"last_error,omitempty"`
	NextRetry *time.Time `json:"next_retry_at,omitempty"`
}

func (p *Platform) handleGetWebhook(w http.ResponseWriter, _ *http.Request, bot *BotInfo) error {
	info := webhookInfo{URL: bot.WebhookURL, HasSecret: bot.WebhookSecret != "", Failures: bot.WebhookFailures, LastError: bot.WebhookLastError}
	if bot.WebhookFailures > 0 {
		info.NextRetry = bot.WebhookRetryAt
	}
	writeResult(w, http.StatusOK, info)
	return nil
}

func (p *Platform) handleSetWebhook(w http.ResponseWriter, r *http.Request, bot *BotInfo) error {
	var request struct {
		URL    string `json:"url"`
		Secret string `json:"secret"`
	}
	if err := decodeJSON(r, &request); err != nil {
		return err
	}
	if len(request.URL) > maxWebhookURLBytes {
		return fail(http.StatusBadRequest, "invalid_url", "webhook地址过长")
	}
	if err := validateWebhookURL(r.Context(), request.URL, p.config.WebhookAllowPrivate); err != nil {
		return fail(http.StatusBadRequest, "invalid_url", err.Error())
	}
	if len(request.Secret) > maxSecretBytes || !utils.ValidText(request.Secret, maxSecretBytes) {
		return fail(http.StatusBadRequest, "invalid_secret", "secret不超过256字节")
	}
	if err := p.store.SetWebhook(r.Context(), bot.UserID, request.URL, request.Secret); err != nil {
		return err
	}
	writeResult(w, http.StatusOK, webhookInfo{URL: request.URL, HasSecret: request.Secret != ""})
	return nil
}

func (p *Platform) handleDeleteWebhook(w http.ResponseWriter, r *http.Request, bot *BotInfo) error {
	if err := p.store.SetWebhook(r.Context(), bot.UserID, "", ""); err != nil {
		return err
	}
	writeResult(w, http.StatusOK, webhookInfo{})
	return nil
}
