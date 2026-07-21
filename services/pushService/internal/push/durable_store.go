package push

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	envelope "Betterfly2/proto/envelope"
	pushpb "Betterfly2/proto/push"
	"Betterfly2/shared/db"
	"Betterfly2/shared/mq"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const messageFanoutSQL = `WITH targets AS (
  SELECT DISTINCT value::bigint AS user_id
  FROM jsonb_array_elements_text(CAST(? AS jsonb))
), eligible AS (
  SELECT token.id
  FROM targets
  JOIN push_device_tokens AS token ON token.user_id = targets.user_id
  LEFT JOIN friends ON friends.user_id = token.user_id
    AND friends.friend_id = ? AND friends.is_delete = FALSE
  WHERE token.push_type = ? AND token.is_active = TRUE
    AND (? = TRUE OR friends.user_id IS NULL OR friends.is_notify = TRUE)
)
INSERT INTO push_message_deliveries
  (message_id, token_id, job_id, status, attempt, claim_token, lease_until, next_retry_at, created_at, updated_at)
SELECT ?, id, ?, ?, 0, '', '', ?, ?, ? FROM eligible
ON CONFLICT (message_id, token_id) DO NOTHING`

const messageRecallFanoutSQL = `WITH targets AS (
  SELECT DISTINCT value::bigint AS user_id
  FROM jsonb_array_elements_text(CAST(? AS jsonb))
)
INSERT INTO push_message_deliveries
  (message_id, token_id, job_id, status, attempt, claim_token, lease_until, next_retry_at, created_at, updated_at)
SELECT ?, token.id, ?, ?, 0, '', '', ?, ?, ?
FROM targets
JOIN push_device_tokens AS token ON token.user_id = targets.user_id
WHERE token.push_type = 'apns' AND token.is_active = TRUE
ON CONFLICT (message_id, token_id) DO UPDATE SET
  job_id = EXCLUDED.job_id, status = EXCLUDED.status, attempt = 0,
  claim_token = '', lease_until = '', next_retry_at = EXCLUDED.next_retry_at,
  last_error = '', apns_id = '', updated_at = EXCLUDED.updated_at`

const voipFanoutSQL = `INSERT INTO push_vo_ip_deliveries
  (call_id, token_id, job_id, status, attempt, claim_token, lease_until, next_retry_at, created_at, updated_at)
SELECT ?, id, ?, ?, 0, '', '', ?, ?, ?
FROM push_device_tokens
WHERE user_id = ? AND push_type = ? AND is_active = TRUE
ON CONFLICT (call_id, token_id) DO NOTHING`

func (s *GormStore) EnqueueRequest(ctx context.Context, operationKey string, request *pushpb.RequestMessage, bundleID string) error {
	if strings.TrimSpace(operationKey) == "" || request == nil || request.GetPayload() == nil {
		return ErrInvalidRequest
	}
	_, err := db.ExecuteInboxOutbox(ctx, s.db, "push", operationKey, func(tx *gorm.DB) ([]byte, []db.PendingOutboxEvent, error) {
		switch payload := request.Payload.(type) {
		case *pushpb.RequestMessage_ClientCommand:
			return s.persistClientCommand(tx, operationKey, payload.ClientCommand, bundleID)
		case *pushpb.RequestMessage_MessagePush:
			return s.persistMessageJob(tx, operationKey, request, payload.MessagePush)
		case *pushpb.RequestMessage_MessageRecall:
			return s.persistMessageRecallJob(tx, operationKey, request, payload.MessageRecall)
		case *pushpb.RequestMessage_VoipCall:
			return s.persistVoIPJob(tx, operationKey, request, payload.VoipCall)
		default:
			return nil, nil, ErrInvalidRequest
		}
	})
	return err
}

func (s *GormStore) persistClientCommand(tx *gorm.DB, operationKey string, command *pushpb.ClientCommand, bundleID string) ([]byte, []db.PendingOutboxEvent, error) {
	if command == nil || command.GetUserId() <= 0 || strings.TrimSpace(command.GetFromKafkaTopic()) == "" || command.GetRequest() == nil {
		return nil, nil, ErrInvalidRequest
	}
	var event *pushpb.ClientEvent
	now := time.Now().UTC()
	switch payload := command.GetRequest().Payload.(type) {
	case *pushpb.ClientRequest_RegisterVoipToken:
		request := payload.RegisterVoipToken
		event = persistRegisterToken(tx, command.GetUserId(), request.GetDeviceId(), request.GetToken(), request.GetEnvironment(), PushTypeVoIP, "register_voip_token", bundleID, now)
	case *pushpb.ClientRequest_UnregisterVoipToken:
		request := payload.UnregisterVoipToken
		event = persistUnregisterToken(tx, command.GetUserId(), request.GetDeviceId(), request.GetEnvironment(), PushTypeVoIP, "unregister_voip_token", now)
	case *pushpb.ClientRequest_RegisterApnsToken:
		request := payload.RegisterApnsToken
		event = persistRegisterToken(tx, command.GetUserId(), request.GetDeviceId(), request.GetToken(), request.GetEnvironment(), PushTypeAPNs, "register_apns_token", bundleID, now)
	case *pushpb.ClientRequest_UnregisterApnsToken:
		request := payload.UnregisterApnsToken
		event = persistUnregisterToken(tx, command.GetUserId(), request.GetDeviceId(), request.GetEnvironment(), PushTypeAPNs, "unregister_apns_token", now)
	default:
		return nil, nil, ErrInvalidRequest
	}
	if event.GetResult() == pushpb.PushResult_PUSH_INTERNAL_ERROR {
		return nil, nil, errors.New("persist push client command")
	}
	response := &pushpb.ResponseMessage{Payload: &pushpb.ResponseMessage_ClientDelivery{ClientDelivery: &pushpb.ClientDelivery{
		TargetUserId: command.GetUserId(), Event: event,
	}}}
	responsePayload, err := proto.Marshal(response)
	if err != nil {
		return nil, nil, err
	}
	envelopePayload, err := mq.MarshalEnvelope(envelope.MessageType_PUSH_RESPONSE, response)
	if err != nil {
		return nil, nil, err
	}
	return responsePayload, []db.PendingOutboxEvent{{
		EventID: "push:" + stablePushJobID(operationKey) + ":client-response",
		Topic:   command.GetFromKafkaTopic(), Payload: envelopePayload,
	}}, nil
}

func persistRegisterToken(tx *gorm.DB, userID int64, rawDeviceID, rawToken string, environment pushpb.PushEnvironment, pushType, operation, bundleID string, now time.Time) *pushpb.ClientEvent {
	if !validDeviceID(rawDeviceID) || !validToken(rawToken) || !validEnvironment(environment) {
		return clientEvent(operation, pushpb.PushResult_INVALID_ARGUMENT, strings.TrimSpace(rawDeviceID), environment, ErrInvalidRequest.Error(), now)
	}
	deviceID := strings.TrimSpace(rawDeviceID)
	token := strings.ToLower(strings.TrimSpace(rawToken))
	if err := registerTokenWithDB(tx, userID, deviceID, token, environmentName(environment), pushType, bundleID, now); err != nil {
		return clientEvent(operation, pushpb.PushResult_PUSH_INTERNAL_ERROR, deviceID, environment, "register token failed", now)
	}
	return clientEvent(operation, pushpb.PushResult_PUSH_OK, deviceID, environment, "registered", now)
}

func persistUnregisterToken(tx *gorm.DB, userID int64, rawDeviceID string, environment pushpb.PushEnvironment, pushType, operation string, now time.Time) *pushpb.ClientEvent {
	if !validDeviceID(rawDeviceID) || !validEnvironment(environment) {
		return clientEvent(operation, pushpb.PushResult_INVALID_ARGUMENT, strings.TrimSpace(rawDeviceID), environment, ErrInvalidRequest.Error(), now)
	}
	deviceID := strings.TrimSpace(rawDeviceID)
	result := tx.Where("user_id = ? AND device_id = ? AND environment = ? AND push_type = ?", userID, deviceID, environmentName(environment), pushType).Delete(&db.PushDeviceToken{})
	if result.Error != nil {
		return clientEvent(operation, pushpb.PushResult_PUSH_INTERNAL_ERROR, deviceID, environment, "unregister token failed", now)
	}
	if result.RowsAffected == 0 {
		return clientEvent(operation, pushpb.PushResult_TOKEN_NOT_FOUND, deviceID, environment, ErrTokenNotFound.Error(), now)
	}
	return clientEvent(operation, pushpb.PushResult_PUSH_OK, deviceID, environment, "unregistered", now)
}

func registerTokenWithDB(tx *gorm.DB, userID int64, deviceID, token, environment, pushType, bundleID string, now time.Time) error {
	nowValue := db.FormatReliabilityTime(now)
	var row db.PushDeviceToken
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("user_id = ? AND device_id = ? AND environment = ? AND push_type = ?", userID, deviceID, environment, pushType).
		First(&row).Error
	if err == nil {
		if err := tx.Where("environment = ? AND push_type = ? AND token = ? AND id <> ?", environment, pushType, token, row.ID).Delete(&db.PushDeviceToken{}).Error; err != nil {
			return err
		}
		return tx.Model(&db.PushDeviceToken{}).Where("id = ?", row.ID).Updates(map[string]any{
			"token": token, "bundle_id": bundleID, "is_active": true, "updated_at": nowValue,
		}).Error
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}

	err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("environment = ? AND push_type = ? AND token = ?", environment, pushType, token).
		First(&row).Error
	if err == nil {
		return tx.Model(&db.PushDeviceToken{}).Where("id = ?", row.ID).Updates(map[string]any{
			"user_id": userID, "device_id": deviceID, "bundle_id": bundleID, "is_active": true, "updated_at": nowValue,
		}).Error
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	return tx.Create(&db.PushDeviceToken{
		UserID: userID, DeviceID: deviceID, Token: token, Environment: environment,
		PushType: pushType, BundleID: bundleID, IsActive: true, CreatedAt: nowValue, UpdatedAt: nowValue,
	}).Error
}

func (s *GormStore) persistMessageJob(tx *gorm.DB, operationKey string, request *pushpb.RequestMessage, message *pushpb.MessagePushRequest) ([]byte, []db.PendingOutboxEvent, error) {
	if message == nil || message.GetMessageId() <= 0 || message.GetSenderUserId() <= 0 || message.GetConversationId() <= 0 || strings.TrimSpace(message.GetMessageType()) == "" || len(message.GetTargetUserIds()) == 0 {
		return nil, nil, ErrInvalidRequest
	}
	messageState, err := lockMessageForPush(tx, message.GetMessageId())
	if err != nil {
		return nil, nil, err
	}
	if messageState.FromUserID != message.GetSenderUserId() || messageState.IsGroup != message.GetIsGroup() ||
		message.GetIsGroup() && messageState.ToUserID != message.GetConversationId() ||
		!message.GetIsGroup() && message.GetConversationId() != message.GetSenderUserId() {
		return nil, nil, ErrInvalidRequest
	}
	if messageState.IsRecalled {
		return nil, nil, nil
	}
	targets := uniquePushTargets(message.GetTargetUserIds(), message.GetSenderUserId())
	targetJSON, err := json.Marshal(targets)
	if err != nil {
		return nil, nil, err
	}
	payload, err := proto.Marshal(request)
	if err != nil {
		return nil, nil, err
	}
	now := db.FormatReliabilityTime(time.Now())
	job := db.PushJob{JobID: stablePushJobID(operationKey), OperationKey: operationKey, Kind: "message", RequestPayload: payload, Status: PushJobPending, CreatedAt: now, UpdatedAt: now}
	if err := tx.Create(&job).Error; err != nil {
		return nil, nil, err
	}
	result := tx.Exec(messageFanoutSQL, string(targetJSON), message.GetSenderUserId(), PushTypeAPNs, message.GetIsGroup(), message.GetMessageId(), job.JobID, DeliveryPending, now, now, now)
	if result.Error != nil {
		return nil, nil, result.Error
	}
	if result.RowsAffected == 0 {
		if err := tx.Model(&db.PushJob{}).Where("job_id = ?", job.JobID).Updates(map[string]any{"status": PushJobCompleted, "completed_at": now, "updated_at": now}).Error; err != nil {
			return nil, nil, err
		}
	}
	return nil, nil, nil
}

func (s *GormStore) persistMessageRecallJob(tx *gorm.DB, operationKey string, request *pushpb.RequestMessage, recall *pushpb.MessageRecallPushRequest) ([]byte, []db.PendingOutboxEvent, error) {
	if recall == nil || recall.GetMessageId() <= 0 || recall.GetConversationId() <= 0 || recall.GetOperatorUserId() <= 0 || len(recall.GetTargetUserIds()) == 0 {
		return nil, nil, ErrInvalidRequest
	}
	recalledAt, err := time.Parse(time.RFC3339Nano, recall.GetRecalledAt())
	if err != nil {
		return nil, nil, ErrInvalidRequest
	}
	message, err := lockMessageForPush(tx, recall.GetMessageId())
	if err != nil {
		return nil, nil, err
	}
	if !message.IsRecalled || message.RecalledBy != recall.GetOperatorUserId() || message.RecalledAt != recalledAt.UTC().Format(time.RFC3339) ||
		message.IsGroup != recall.GetIsGroup() || recall.GetIsGroup() && message.ToUserID != recall.GetConversationId() ||
		!recall.GetIsGroup() && message.FromUserID != recall.GetConversationId() {
		return nil, nil, ErrInvalidRequest
	}

	targets := uniquePushTargets(recall.GetTargetUserIds(), recall.GetOperatorUserId())
	if len(targets) == 0 {
		return nil, nil, nil
	}
	targetJSON, err := json.Marshal(targets)
	if err != nil {
		return nil, nil, err
	}
	payload, err := proto.Marshal(request)
	if err != nil {
		return nil, nil, err
	}
	now := db.FormatReliabilityTime(time.Now())
	job := db.PushJob{
		JobID: stableMessageRecallJobID(recall.GetMessageId()), OperationKey: operationKey, Kind: "message_recall",
		RequestPayload: payload, Status: PushJobPending, CreatedAt: now, UpdatedAt: now,
	}
	created := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "job_id"}}, DoNothing: true}).Create(&job)
	if created.Error != nil {
		return nil, nil, created.Error
	}
	if created.RowsAffected == 0 {
		return nil, nil, nil
	}

	terminalError := "message_recalled"
	if err := tx.Model(&db.PushMessageDelivery{}).
		Where("message_id = ? AND status IN ?", recall.GetMessageId(), []string{DeliveryPending, DeliveryClaimed, DeliveryRetryable}).
		Updates(map[string]any{"status": DeliveryPermanent, "claim_token": "", "lease_until": "", "next_retry_at": "", "last_error": terminalError, "updated_at": now}).Error; err != nil {
		return nil, nil, err
	}
	if err := tx.Exec(`UPDATE push_jobs SET status = ?, completed_at = ?, updated_at = ?
WHERE kind = 'message' AND job_id IN (SELECT job_id FROM push_message_deliveries WHERE message_id = ?)`,
		PushJobCompleted, now, now, recall.GetMessageId()).Error; err != nil {
		return nil, nil, err
	}

	deliveryKey := -recall.GetMessageId()
	result := tx.Exec(messageRecallFanoutSQL, string(targetJSON), deliveryKey, job.JobID, DeliveryPending, now, now, now)
	if result.Error != nil {
		return nil, nil, result.Error
	}
	if result.RowsAffected == 0 {
		if err := tx.Model(&db.PushJob{}).Where("job_id = ?", job.JobID).Updates(map[string]any{
			"status": PushJobCompleted, "completed_at": now, "updated_at": now,
		}).Error; err != nil {
			return nil, nil, err
		}
	}
	return nil, nil, nil
}

func lockMessageForPush(tx *gorm.DB, messageID int64) (*db.Message, error) {
	var message db.Message
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&message, "message_id = ?", messageID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrInvalidRequest
		}
		return nil, err
	}
	return &message, nil
}

func (s *GormStore) persistVoIPJob(tx *gorm.DB, operationKey string, request *pushpb.RequestMessage, call *pushpb.VoIPCallRequest) ([]byte, []db.PendingOutboxEvent, error) {
	if call == nil || strings.TrimSpace(call.GetCallId()) == "" || call.GetCallerUserId() <= 0 || call.GetCalleeUserId() <= 0 || call.GetCallerUserId() == call.GetCalleeUserId() || strings.TrimSpace(call.GetResultKafkaTopic()) == "" {
		return nil, nil, ErrInvalidRequest
	}
	payload, err := proto.Marshal(request)
	if err != nil {
		return nil, nil, err
	}
	nowTime := time.Now().UTC()
	now := db.FormatReliabilityTime(nowTime)
	job := db.PushJob{JobID: stablePushJobID(operationKey), OperationKey: operationKey, Kind: "voip", RequestPayload: payload, Status: PushJobPending, CreatedAt: now, UpdatedAt: now}
	if err := tx.Create(&job).Error; err != nil {
		return nil, nil, err
	}
	expiresAt, parseErr := time.Parse(time.RFC3339Nano, call.GetExpiresAt())
	if parseErr != nil || !expiresAt.After(nowTime) {
		return s.completeVoIPWithoutDelivery(tx, job, call, "call_expired", nowTime)
	}
	result := tx.Exec(voipFanoutSQL, call.GetCallId(), job.JobID, DeliveryPending, now, now, now, call.GetCalleeUserId(), PushTypeVoIP)
	if result.Error != nil {
		return nil, nil, result.Error
	}
	if result.RowsAffected == 0 {
		return s.completeVoIPWithoutDelivery(tx, job, call, "no_active_voip_token", nowTime)
	}
	return nil, nil, nil
}

func (s *GormStore) completeVoIPWithoutDelivery(tx *gorm.DB, job db.PushJob, call *pushpb.VoIPCallRequest, reason string, now time.Time) ([]byte, []db.PendingOutboxEvent, error) {
	nowValue := db.FormatReliabilityTime(now)
	if err := tx.Model(&db.PushJob{}).Where("job_id = ?", job.JobID).Updates(map[string]any{"status": PushJobCompleted, "completed_at": nowValue, "updated_at": nowValue}).Error; err != nil {
		return nil, nil, err
	}
	response, payload, err := voipResultPayload(call, false, reason, now)
	if err != nil {
		return nil, nil, err
	}
	encoded, err := proto.Marshal(response)
	if err != nil {
		return nil, nil, err
	}
	return encoded, []db.PendingOutboxEvent{{EventID: "push:" + job.JobID + ":voip-result", Topic: call.GetResultKafkaTopic(), Payload: payload}}, nil
}

func voipResultPayload(call *pushpb.VoIPCallRequest, accepted bool, reason string, now time.Time) (*pushpb.ResponseMessage, []byte, error) {
	response := &pushpb.ResponseMessage{Payload: &pushpb.ResponseMessage_VoipResult{VoipResult: &pushpb.VoIPPushResult{
		CallId: call.GetCallId(), CallerUserId: call.GetCallerUserId(), CalleeUserId: call.GetCalleeUserId(),
		Accepted: accepted, Reason: reason, Timestamp: now.UTC().Format(time.RFC3339Nano), Required: call.GetRequired(),
	}}}
	payload, err := mq.MarshalEnvelope(envelope.MessageType_PUSH_RESPONSE, response)
	return response, payload, err
}

func stablePushJobID(operationKey string) string {
	digest := sha256.Sum256([]byte("betterfly-push:" + operationKey))
	return "push-" + hex.EncodeToString(digest[:])
}

func stableMessageRecallJobID(messageID int64) string {
	return fmt.Sprintf("push-recall-%d", messageID)
}

func validateRequestPayload(payload []byte) (*pushpb.RequestMessage, error) {
	request := &pushpb.RequestMessage{}
	if err := proto.Unmarshal(payload, request); err != nil {
		return nil, fmt.Errorf("decode durable push request: %w", err)
	}
	return request, nil
}
