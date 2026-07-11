package push

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"Betterfly2/shared/db"
	"Betterfly2/shared/logger"
)

const maxAdminTargets = 50

type AdminTokenView struct {
	ID          int64  `json:"id"`
	UserID      int64  `json:"user_id"`
	DeviceID    string `json:"device_id"`
	TokenMasked string `json:"token_masked"`
	Environment string `json:"environment"`
	PushType    string `json:"push_type"`
	BundleID    string `json:"bundle_id"`
	IsActive    bool   `json:"is_active"`
	UpdatedAt   string `json:"updated_at"`
}

type AdminMessageRequest struct {
	TargetUserIDs     []int64        `json:"target_user_ids"`
	TokenID           int64          `json:"token_id"`
	SenderUserID      int64          `json:"sender_user_id"`
	ConversationID    int64          `json:"conversation_id"`
	IsGroup           bool           `json:"is_group"`
	MessageType       string         `json:"message_type"`
	Title             string         `json:"title"`
	Body              string         `json:"body"`
	CustomData        map[string]any `json:"custom_data"`
	Environment       string         `json:"environment"`
	IgnorePreferences bool           `json:"ignore_preferences"`
}

type AdminVoIPRequest struct {
	TargetUserID int64  `json:"target_user_id"`
	TokenID      int64  `json:"token_id"`
	CallerUserID int64  `json:"caller_user_id"`
	CallID       string `json:"call_id"`
	CallType     string `json:"call_type"`
	Environment  string `json:"environment"`
	ExpiresIn    int    `json:"expires_in_seconds"`
}

type DeliveryResult struct {
	TokenID     int64  `json:"token_id"`
	UserID      int64  `json:"user_id"`
	DeviceID    string `json:"device_id"`
	TokenMasked string `json:"token_masked"`
	Environment string `json:"environment"`
	PushType    string `json:"push_type"`
	Accepted    bool   `json:"accepted"`
	APNSID      string `json:"apns_id,omitempty"`
	Error       string `json:"error,omitempty"`
}

type DeliveryReport struct {
	Kind      string           `json:"kind"`
	Accepted  int              `json:"accepted"`
	Failed    int              `json:"failed"`
	Skipped   int              `json:"skipped"`
	Results   []DeliveryResult `json:"results"`
	Timestamp string           `json:"timestamp"`
}

type AdminAuditView struct {
	ID            int64  `json:"id"`
	Kind          string `json:"kind"`
	Operator      string `json:"operator"`
	TargetSummary string `json:"target_summary"`
	AcceptedCount int    `json:"accepted_count"`
	FailedCount   int    `json:"failed_count"`
	Status        string `json:"status"`
	DetailsJSON   string `json:"details_json"`
	CreatedAt     string `json:"created_at"`
}

func (s *Service) AdminSummary(ctx context.Context) (TokenSummary, error) {
	return s.store.TokenSummary(ctx)
}

func (s *Service) AdminTokens(ctx context.Context, filter TokenFilter) ([]AdminTokenView, error) {
	tokens, err := s.store.FindTokens(ctx, filter)
	if err != nil {
		return nil, err
	}
	views := make([]AdminTokenView, 0, len(tokens))
	for _, token := range tokens {
		views = append(views, tokenView(token))
	}
	return views, nil
}

func (s *Service) AdminAudits(ctx context.Context, limit int) ([]AdminAuditView, error) {
	audits, err := s.store.ListDebugAudits(ctx, limit)
	if err != nil {
		return nil, err
	}
	views := make([]AdminAuditView, 0, len(audits))
	for _, audit := range audits {
		views = append(views, AdminAuditView{
			ID: audit.ID, Kind: audit.Kind, Operator: audit.Operator, TargetSummary: audit.TargetSummary,
			AcceptedCount: audit.AcceptedCount, FailedCount: audit.FailedCount, Status: audit.Status,
			DetailsJSON: audit.DetailsJSON, CreatedAt: audit.CreatedAt,
		})
	}
	return views, nil
}

func (s *Service) AdminSendMessage(ctx context.Context, request AdminMessageRequest, operator string) (DeliveryReport, error) {
	if err := validateAdminMessage(request); err != nil {
		return DeliveryReport{}, err
	}
	tokens, skipped, err := s.adminMessageTokens(ctx, request)
	if err != nil {
		return DeliveryReport{}, err
	}
	if len(tokens) == 0 {
		if skipped > 0 {
			return DeliveryReport{}, fmt.Errorf("all targets were excluded by notification preferences")
		}
		return DeliveryReport{}, fmt.Errorf("no active compatible APNs token found")
	}
	now := s.now().UTC()
	report := s.sendAdmin(ctx, "message", tokens, func(token db.PushDeviceToken) Notification {
		return Notification{
			Kind: NotificationMessage, Token: token.Token, Environment: parseEnvironment(token.Environment),
			SenderUserID: request.SenderUserID, TargetUserID: token.UserID,
			ConversationID: request.ConversationID, IsGroup: request.IsGroup,
			MessageType: strings.TrimSpace(request.MessageType), SentAt: now, ExpiresAt: now.Add(24 * time.Hour),
			Title: strings.TrimSpace(request.Title), Body: strings.TrimSpace(request.Body), CustomData: request.CustomData,
		}
	})
	report.Skipped = skipped
	s.auditAdminDelivery(ctx, operator, targetSummary(request.TargetUserIDs, request.TokenID), report)
	return report, nil
}

func (s *Service) AdminSendVoIP(ctx context.Context, request AdminVoIPRequest, operator string) (DeliveryReport, error) {
	if !validAdminEnvironment(request.Environment) {
		return DeliveryReport{}, fmt.Errorf("environment must be sandbox, production or empty")
	}
	if request.TargetUserID <= 0 && request.TokenID <= 0 {
		return DeliveryReport{}, fmt.Errorf("target_user_id or token_id is required")
	}
	if request.TargetUserID > 0 && request.TokenID > 0 {
		return DeliveryReport{}, fmt.Errorf("target_user_id and token_id are mutually exclusive")
	}
	if request.CallerUserID <= 0 {
		return DeliveryReport{}, fmt.Errorf("caller_user_id is required")
	}
	request.CallType = strings.ToLower(strings.TrimSpace(request.CallType))
	if request.CallType != "audio" && request.CallType != "video" {
		return DeliveryReport{}, fmt.Errorf("call_type must be audio or video")
	}
	if strings.TrimSpace(request.CallID) == "" {
		request.CallID = newDebugCallID()
	}
	if request.ExpiresIn <= 0 {
		request.ExpiresIn = 60
	}
	if request.ExpiresIn > 300 {
		return DeliveryReport{}, fmt.Errorf("expires_in_seconds cannot exceed 300")
	}
	tokens, err := s.adminTargetTokens(ctx, request.TargetUserID, request.TokenID, PushTypeVoIP, request.Environment)
	if err != nil {
		return DeliveryReport{}, err
	}
	if len(tokens) == 0 {
		return DeliveryReport{}, fmt.Errorf("no active compatible VoIP token found")
	}
	expiresAt := s.now().UTC().Add(time.Duration(request.ExpiresIn) * time.Second)
	report := s.sendAdmin(ctx, "voip", tokens, func(token db.PushDeviceToken) Notification {
		return Notification{
			Kind: NotificationVoIP, Token: token.Token, Environment: parseEnvironment(token.Environment),
			CallID: request.CallID, CallerUserID: request.CallerUserID, CalleeUserID: token.UserID,
			CallType: request.CallType, ExpiresAt: expiresAt,
		}
	})
	s.auditAdminDelivery(ctx, operator, targetSummary([]int64{request.TargetUserID}, request.TokenID), report)
	return report, nil
}

func (s *Service) adminMessageTokens(ctx context.Context, request AdminMessageRequest) ([]db.PushDeviceToken, int, error) {
	if request.TokenID > 0 {
		tokens, err := s.adminTargetTokens(ctx, 0, request.TokenID, PushTypeAPNs, request.Environment)
		return tokens, 0, err
	}
	seen := make(map[int64]struct{}, len(request.TargetUserIDs))
	var tokens []db.PushDeviceToken
	skipped := 0
	for _, userID := range request.TargetUserIDs {
		if userID <= 0 {
			continue
		}
		if _, ok := seen[userID]; ok {
			continue
		}
		seen[userID] = struct{}{}
		if !request.IgnorePreferences {
			enabled, err := s.store.MessageNotificationsEnabled(ctx, userID, request.SenderUserID, request.IsGroup)
			if err != nil {
				return nil, 0, err
			}
			if !enabled {
				skipped++
				continue
			}
		}
		userTokens, err := s.store.ListActiveTokens(ctx, userID, PushTypeAPNs)
		if err != nil {
			return nil, 0, err
		}
		for _, token := range userTokens {
			if environmentMatches(token.Environment, request.Environment) {
				tokens = append(tokens, token)
			}
		}
	}
	return tokens, skipped, nil
}

func (s *Service) adminTargetTokens(ctx context.Context, userID, tokenID int64, pushType, environment string) ([]db.PushDeviceToken, error) {
	if tokenID > 0 {
		token, err := s.store.GetToken(ctx, tokenID)
		if err != nil {
			return nil, err
		}
		if !token.IsActive || token.PushType != pushType || !environmentMatches(token.Environment, environment) {
			return nil, fmt.Errorf("token is inactive or incompatible with requested push")
		}
		return []db.PushDeviceToken{token}, nil
	}
	tokens, err := s.store.ListActiveTokens(ctx, userID, pushType)
	if err != nil {
		return nil, err
	}
	filtered := tokens[:0]
	for _, token := range tokens {
		if environmentMatches(token.Environment, environment) {
			filtered = append(filtered, token)
		}
	}
	return filtered, nil
}

func (s *Service) sendAdmin(ctx context.Context, kind string, tokens []db.PushDeviceToken, build func(db.PushDeviceToken) Notification) DeliveryReport {
	report := DeliveryReport{Kind: kind, Results: make([]DeliveryResult, 0, len(tokens)), Timestamp: s.now().UTC().Format(time.RFC3339Nano)}
	for _, token := range tokens {
		result := DeliveryResult{TokenID: token.ID, UserID: token.UserID, DeviceID: token.DeviceID, TokenMasked: maskToken(token.Token), Environment: token.Environment, PushType: token.PushType}
		sent, err := s.sender.Send(ctx, build(token))
		if err == nil {
			result.Accepted = true
			result.APNSID = sent.APNSID
			report.Accepted++
		} else {
			result.Error = err.Error()
			report.Failed++
			var apnsErr *APNSError
			if errors.As(err, &apnsErr) && apnsErr.InvalidatesToken() {
				_ = s.store.DeactivateToken(ctx, token.ID)
			}
		}
		report.Results = append(report.Results, result)
	}
	return report
}

func (s *Service) auditAdminDelivery(ctx context.Context, operator, target string, report DeliveryReport) {
	status := "success"
	if report.Accepted == 0 {
		status = "failed"
	} else if report.Failed > 0 {
		status = "partial"
	}
	details, _ := json.Marshal(report.Results)
	audit := &db.PushDebugAudit{
		Kind: report.Kind, Operator: strings.TrimSpace(operator), TargetSummary: target,
		AcceptedCount: report.Accepted, FailedCount: report.Failed, Status: status,
		DetailsJSON: string(details), CreatedAt: s.now().UTC().Format(time.RFC3339Nano),
	}
	if err := s.store.CreateDebugAudit(ctx, audit); err != nil {
		logger.Sugar().Warnf("保存调试推送审计失败: %v", err)
	}
}

func validateAdminMessage(request AdminMessageRequest) error {
	if request.TokenID <= 0 && len(request.TargetUserIDs) == 0 {
		return fmt.Errorf("target_user_ids or token_id is required")
	}
	if request.TokenID > 0 && len(request.TargetUserIDs) > 0 {
		return fmt.Errorf("target_user_ids and token_id are mutually exclusive")
	}
	if len(request.TargetUserIDs) > maxAdminTargets {
		return fmt.Errorf("target count cannot exceed %d", maxAdminTargets)
	}
	if request.SenderUserID <= 0 || request.ConversationID <= 0 || strings.TrimSpace(request.MessageType) == "" {
		return fmt.Errorf("sender_user_id, conversation_id and message_type are required")
	}
	if len([]rune(request.Title)) > 100 || len([]rune(request.Body)) > 500 {
		return fmt.Errorf("title or body is too long")
	}
	data, err := json.Marshal(request.CustomData)
	if err != nil || len(data) > 2048 {
		return fmt.Errorf("custom_data must be valid and at most 2048 bytes")
	}
	if !validAdminEnvironment(request.Environment) {
		return fmt.Errorf("environment must be sandbox, production or empty")
	}
	return nil
}

func tokenView(token db.PushDeviceToken) AdminTokenView {
	return AdminTokenView{ID: token.ID, UserID: token.UserID, DeviceID: token.DeviceID, TokenMasked: maskToken(token.Token), Environment: token.Environment, PushType: token.PushType, BundleID: token.BundleID, IsActive: token.IsActive, UpdatedAt: token.UpdatedAt}
}

func maskToken(token string) string {
	token = strings.TrimSpace(token)
	if len(token) <= 12 {
		return "***"
	}
	return token[:6] + "..." + token[len(token)-6:]
}

func environmentMatches(tokenEnvironment, requested string) bool {
	requested = strings.ToLower(strings.TrimSpace(requested))
	return requested == "" || strings.EqualFold(tokenEnvironment, requested)
}

func validAdminEnvironment(environment string) bool {
	environment = strings.ToLower(strings.TrimSpace(environment))
	return environment == "" || environment == "sandbox" || environment == "production"
}

func targetSummary(userIDs []int64, tokenID int64) string {
	if tokenID > 0 {
		return "token_id:" + strconv.FormatInt(tokenID, 10)
	}
	parts := make([]string, 0, len(userIDs))
	for _, id := range userIDs {
		if id > 0 {
			parts = append(parts, strconv.FormatInt(id, 10))
		}
	}
	return "user_ids:" + strings.Join(parts, ",")
}

func newDebugCallID() string {
	data := make([]byte, 16)
	if _, err := rand.Read(data); err != nil {
		return fmt.Sprintf("%032x", time.Now().UnixNano())
	}
	return hex.EncodeToString(data)
}
