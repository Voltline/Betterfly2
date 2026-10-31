package platform

import (
	"crypto/subtle"
	_ "embed"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"Betterfly2/shared/db"
	"Betterfly2/shared/logger"
)

//go:embed admin.html
var adminPage []byte

type adminBot struct {
	botProfile
	Delivery     string     `json:"delivery"`
	KafkaTopic   string     `json:"kafka_topic,omitempty"`
	HasToken     bool       `json:"has_token"`
	Disabled     bool       `json:"disabled"`
	WebhookURL   string     `json:"webhook_url,omitempty"`
	WebhookError string     `json:"webhook_last_error,omitempty"`
	Failures     int        `json:"webhook_failures,omitempty"`
	RetryAt      *time.Time `json:"webhook_retry_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
}

func adminView(bot *BotInfo) adminBot {
	view := adminBot{
		botProfile: profile(bot), Delivery: bot.Delivery, KafkaTopic: bot.KafkaTopic, HasToken: bot.HasToken, Disabled: bot.Disabled,
		WebhookURL: bot.WebhookURL, WebhookError: bot.WebhookLastError, Failures: bot.WebhookFailures, CreatedAt: bot.CreatedAt,
	}
	if bot.WebhookFailures > 0 {
		view.RetryAt = bot.WebhookRetryAt
	}
	return view
}

type adminHandler func(w http.ResponseWriter, r *http.Request) error

// withAdmin 校验X-Admin-Token。未配置CHATBOT_ADMIN_TOKEN时管理接口一律404。
func (p *Platform) withAdmin(route string, handler adminHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		var err error
		token := r.Header.Get("X-Admin-Token")
		switch {
		case p.config.AdminToken == "":
			err = fail(http.StatusNotFound, "admin_disabled", "未配置CHATBOT_ADMIN_TOKEN，管理接口未开启")
		case subtle.ConstantTimeCompare([]byte(token), []byte(p.config.AdminToken)) != 1:
			err = fail(http.StatusUnauthorized, "unauthorized", "管理令牌无效")
		default:
			err = handler(recorder, r)
		}
		if err != nil {
			var failure *apiError
			if !errors.As(err, &failure) {
				logger.Sugar().Warnw("机器人管理请求失败", "route", route, "error", err)
			}
			writeError(recorder, err)
		}
		apiRequests.WithLabelValues("admin_"+route, strconv.Itoa(recorder.status)).Inc()
	}
}

// AdminRoutes 注册管理页面与管理接口。
func (p *Platform) AdminRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin/{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'")
		w.Header().Set("X-Frame-Options", "DENY")
		_, _ = w.Write(adminPage)
	})
	mux.HandleFunc("GET /admin/api/bots", p.withAdmin("list", p.handleListBots))
	mux.HandleFunc("POST /admin/api/bots", p.withAdmin("create", p.handleCreateBot))
	mux.HandleFunc("PATCH /admin/api/bots/{id}", p.withAdmin("update", p.handlePatchBot))
	mux.HandleFunc("POST /admin/api/bots/{id}/token", p.withAdmin("token", p.handleResetToken))
}

func (p *Platform) handleListBots(w http.ResponseWriter, r *http.Request) error {
	bots, err := p.store.ListBots(r.Context())
	if err != nil {
		return err
	}
	views := make([]adminBot, 0, len(bots))
	for i := range bots {
		views = append(views, adminView(&bots[i]))
	}
	writeResult(w, http.StatusOK, views)
	return nil
}

func (p *Platform) handleCreateBot(w http.ResponseWriter, r *http.Request) error {
	var request struct {
		Account     string `json:"account"`
		Name        string `json:"name"`
		Avatar      string `json:"avatar"`
		Description string `json:"description"`
		AllowGroups *bool  `json:"allow_groups"`
	}
	if err := decodeJSON(r, &request); err != nil {
		return err
	}
	request.Account = strings.TrimSpace(request.Account)
	if !ValidAccount(request.Account) {
		return fail(http.StatusBadRequest, "invalid_account", "账号只能用小写字母、数字和下划线，3到50个字符")
	}
	avatar, err := p.validateProfile(r.Context(), 0, &request.Name, &request.Description, &request.Avatar)
	if err != nil {
		return err
	}
	allowGroups := request.AllowGroups == nil || *request.AllowGroups
	token, tokenHash, err := NewToken()
	if err != nil {
		return err
	}
	userID, err := p.store.CreateBot(r.Context(), NewBot{
		Account: request.Account, Name: request.Name, Avatar: *avatar, Description: request.Description, AllowGroups: allowGroups,
	}, tokenHash)
	switch {
	case errors.Is(err, ErrAccountTaken):
		return fail(http.StatusConflict, "account_taken", "账号已被使用")
	case errors.Is(err, ErrBotIDsExhausted):
		return fail(http.StatusConflict, "ids_exhausted", "机器人ID段已用完")
	case err != nil:
		return err
	}
	bot, err := p.store.Bot(r.Context(), userID)
	if err != nil {
		return err
	}
	logger.Sugar().Infow("创建机器人", "bot_user_id", userID, "account", request.Account)
	writeResult(w, http.StatusCreated, map[string]any{"bot": adminView(bot), "token": token})
	return nil
}

func pathBotID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || !db.IsBotUserID(id) {
		return 0, fail(http.StatusNotFound, "not_found", "机器人不存在")
	}
	return id, nil
}

func (p *Platform) handlePatchBot(w http.ResponseWriter, r *http.Request) error {
	id, err := pathBotID(r)
	if err != nil {
		return err
	}
	var request struct {
		Name        *string `json:"name"`
		Avatar      *string `json:"avatar"`
		Description *string `json:"description"`
		AllowGroups *bool   `json:"allow_groups"`
		Disabled    *bool   `json:"disabled"`
	}
	if err := decodeJSON(r, &request); err != nil {
		return err
	}
	avatar, err := p.validateProfile(r.Context(), 0, request.Name, request.Description, request.Avatar)
	if err != nil {
		return err
	}
	err = p.store.UpdateBot(r.Context(), id, BotPatch{
		Name: request.Name, Avatar: avatar, Description: request.Description, AllowGroups: request.AllowGroups, Disabled: request.Disabled,
	})
	if errors.Is(err, ErrNotFound) {
		return fail(http.StatusNotFound, "not_found", "机器人不存在")
	}
	if err != nil {
		return err
	}
	bot, err := p.store.Bot(r.Context(), id)
	if err != nil {
		return err
	}
	writeResult(w, http.StatusOK, adminView(bot))
	return nil
}

func (p *Platform) handleResetToken(w http.ResponseWriter, r *http.Request) error {
	id, err := pathBotID(r)
	if err != nil {
		return err
	}
	token, tokenHash, err := NewToken()
	if err != nil {
		return err
	}
	if err := p.store.SetToken(r.Context(), id, tokenHash); errors.Is(err, ErrNotFound) {
		return fail(http.StatusNotFound, "not_found", "机器人不存在或是内置机器人")
	} else if err != nil {
		return err
	}
	logger.Sugar().Infow("重置机器人令牌", "bot_user_id", id)
	writeResult(w, http.StatusOK, map[string]string{"token": token})
	return nil
}
