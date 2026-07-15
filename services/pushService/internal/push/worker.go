package push

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"math"
	"strings"
	"sync"
	"time"

	pushpb "Betterfly2/proto/push"
	"Betterfly2/shared/logger"
	"Betterfly2/shared/metrics"
)

type deliveryKind string

const (
	deliveryKindMessage deliveryKind = "message"
	deliveryKindVoIP    deliveryKind = "voip"
)

type preparedDelivery struct {
	claim            DurableDeliveryClaim
	notification     Notification
	prepareErr       error
	prepareTransient bool
}

func (s *Service) RunWorkers(ctx context.Context) error {
	if s.durable == nil {
		return errors.New("push durable store is not configured")
	}
	errCh := make(chan error, 2)
	var workers sync.WaitGroup
	for _, kind := range []deliveryKind{deliveryKindMessage, deliveryKindVoIP} {
		kind := kind
		workers.Add(1)
		go func() {
			defer workers.Done()
			errCh <- s.runDeliveryLoop(ctx, s.durable, kind)
		}()
	}
	select {
	case <-ctx.Done():
		workers.Wait()
		return nil
	case err := <-errCh:
		return err
	}
}

func (s *Service) runDeliveryLoop(ctx context.Context, store DurableStore, kind deliveryKind) error {
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		var (
			claims []DurableDeliveryClaim
			err    error
		)
		now := s.now().UTC()
		if kind == deliveryKindMessage {
			claims, err = store.ClaimMessageDeliveryBatch(ctx, s.maxConcurrency, now, s.deliveryLease, s.maxAttempts)
		} else {
			claims, err = store.ClaimVoIPDeliveryBatch(ctx, s.maxConcurrency, now, s.deliveryLease, s.maxAttempts)
		}
		if err != nil {
			logger.Sugar().Errorw("领取Push投递任务失败", "kind", kind, "error", err)
			if !waitForWorker(ctx, s.workerPoll) {
				return nil
			}
			continue
		}
		if len(claims) == 0 {
			if !waitForWorker(ctx, s.workerPoll) {
				return nil
			}
			continue
		}
		metrics.RecordPushBatchSize(len(claims))
		prepared := s.prepareDeliveries(ctx, kind, claims)
		if err := s.processDurableBatch(ctx, store, kind, prepared); err != nil && ctx.Err() == nil {
			logger.Sugar().Errorw("Push投递批次存在持久化失败，将由租约恢复", "kind", kind, "error", err)
		}
	}
}

func (s *Service) prepareDeliveries(ctx context.Context, kind deliveryKind, claims []DurableDeliveryClaim) []preparedDelivery {
	prepared := make([]preparedDelivery, 0, len(claims))
	messageCache := make(map[string]struct {
		request      *pushpb.MessagePushRequest
		presentation MessagePresentation
		err          error
	})
	for _, claim := range claims {
		request, err := decodeClaimRequest(claim)
		if err != nil {
			prepared = append(prepared, preparedDelivery{claim: claim, prepareErr: err})
			continue
		}
		if claim.Token.Token == "" || !claim.Token.IsActive {
			prepared = append(prepared, preparedDelivery{claim: claim, prepareErr: ErrTokenNotFound})
			continue
		}
		if kind == deliveryKindVoIP {
			call := request.GetVoipCall()
			if call == nil {
				prepared = append(prepared, preparedDelivery{claim: claim, prepareErr: ErrInvalidRequest})
				continue
			}
			expiresAt, parseErr := time.Parse(time.RFC3339Nano, call.GetExpiresAt())
			if parseErr != nil || !expiresAt.After(s.now()) {
				prepared = append(prepared, preparedDelivery{claim: claim, prepareErr: errors.New("call_expired")})
				continue
			}
			prepared = append(prepared, preparedDelivery{claim: claim, notification: Notification{
				Kind: NotificationVoIP, Token: claim.Token.Token, Environment: parseEnvironment(claim.Token.Environment),
				CallID: call.GetCallId(), CallerUserID: call.GetCallerUserId(), CalleeUserID: call.GetCalleeUserId(),
				CallType: call.GetCallType(), ExpiresAt: expiresAt,
			}})
			continue
		}

		cached, exists := messageCache[claim.JobID]
		if !exists {
			message := request.GetMessagePush()
			if message == nil {
				cached.err = ErrInvalidRequest
			} else {
				cached.request = message
				cached.presentation, cached.err = s.store.MessagePresentation(ctx, message.GetSenderUserId(), message.GetConversationId(), message.GetIsGroup())
			}
			messageCache[claim.JobID] = cached
		}
		if cached.err != nil {
			prepared = append(prepared, preparedDelivery{claim: claim, prepareErr: cached.err, prepareTransient: true})
			continue
		}
		message := cached.request
		sentAt, parseErr := time.Parse(time.RFC3339Nano, message.GetSentAt())
		if parseErr != nil {
			sentAt = s.now().UTC()
		}
		preview := strings.TrimSpace(message.GetPreview())
		if preview == "" {
			preview = defaultMessagePreview(message.GetMessageType())
		}
		body := preview
		if message.GetIsGroup() && strings.TrimSpace(cached.presentation.SenderName) != "" {
			body = cached.presentation.SenderName + "：" + preview
		}
		prepared = append(prepared, preparedDelivery{claim: claim, notification: Notification{
			Kind: NotificationMessage, Token: claim.Token.Token, Environment: parseEnvironment(claim.Token.Environment),
			SenderUserID: message.GetSenderUserId(), TargetUserID: claim.Token.UserID,
			ConversationID: message.GetConversationId(), IsGroup: message.GetIsGroup(), MessageType: strings.TrimSpace(message.GetMessageType()),
			SentAt: sentAt, MessageID: message.GetMessageId(), ExpiresAt: sentAt.Add(24 * time.Hour),
			Title: cached.presentation.Title, Body: body, SenderName: cached.presentation.SenderName, SenderAvatar: cached.presentation.SenderAvatar,
			GroupName: cached.presentation.GroupName, Avatar: cached.presentation.Avatar, AvatarIsGroup: cached.presentation.AvatarIsGroup,
			ConversationName: cached.presentation.ConversationName, ConversationAvatar: cached.presentation.ConversationAvatar,
		}})
	}
	return prepared
}

func (s *Service) processDurableBatch(ctx context.Context, store DurableStore, kind deliveryKind, prepared []preparedDelivery) error {
	errCh := make(chan error, len(prepared))
	var workers sync.WaitGroup
	for _, item := range prepared {
		item := item
		workers.Add(1)
		go func() {
			defer workers.Done()
			if !item.claim.QueuedAt.IsZero() {
				delay := s.now().Sub(item.claim.QueuedAt)
				if delay >= 0 {
					metrics.RecordPushQueueDelay(delay)
				}
			}
			update := s.sendDurableDelivery(ctx, item)
			var err error
			if kind == deliveryKindMessage {
				err = store.FinalizeMessageDelivery(ctx, update)
			} else {
				err = store.FinalizeVoIPDelivery(ctx, update)
			}
			if errors.Is(err, ErrDeliveryFenced) {
				logger.Sugar().Debugw("忽略已过期Push worker的Finalize", "job_id", item.claim.JobID, "token_id", item.claim.Token.ID, "attempt", item.claim.Attempt)
				return
			}
			if err != nil {
				errCh <- err
			}
		}()
	}
	workers.Wait()
	close(errCh)
	for err := range errCh {
		return err
	}
	return nil
}

func sanitizedPushError(err error) string {
	var apnsErr *APNSError
	if errors.As(err, &apnsErr) {
		value := fmt.Sprintf("apns_status=%d reason=%s", apnsErr.StatusCode, apnsErr.Reason)
		if len(value) > 255 {
			return value[:255]
		}
		return value
	}
	return "network_or_sender_error"
}

func (s *Service) sendDurableDelivery(ctx context.Context, item preparedDelivery) DurableDeliveryUpdate {
	update := DurableDeliveryUpdate{DurableDeliveryClaim: item.claim}
	if item.prepareErr != nil {
		update.LastError = sanitizedPushError(item.prepareErr)
		if item.prepareTransient {
			return s.retryableDeliveryUpdate(update)
		}
		update.Status = DeliveryPermanent
		metrics.RecordPushDelivery(DeliveryPermanent)
		return update
	}
	sendCtx, cancel := context.WithTimeout(ctx, s.sendTimeout)
	start := time.Now()
	result, sendErr := s.sender.Send(sendCtx, item.notification)
	cancel()
	metrics.RecordPushAPNSLatency(start)
	update.APNSID = result.APNSID
	if sendErr == nil {
		update.Status = DeliverySent
		metrics.RecordPushDelivery(DeliverySent)
		return update
	}
	update.LastError = sanitizedPushError(sendErr)
	var apnsErr *APNSError
	if errors.As(sendErr, &apnsErr) && apnsErr.InvalidatesToken() {
		update.Status = DeliveryPermanent
		update.DeactivateToken = true
		update.APNSID = apnsErr.APNSID
		metrics.RecordPushDelivery(DeliveryPermanent)
		return update
	}
	if errors.As(sendErr, &apnsErr) && !apnsErr.Retryable() || errors.Is(sendErr, ErrInvalidRequest) {
		update.Status = DeliveryPermanent
		metrics.RecordPushDelivery(DeliveryPermanent)
		return update
	}
	return s.retryableDeliveryUpdate(update)
}

func (s *Service) retryableDeliveryUpdate(update DurableDeliveryUpdate) DurableDeliveryUpdate {
	if update.Attempt >= s.maxAttempts {
		update.Status = DeliveryFailed
		metrics.RecordPushDelivery(DeliveryFailed)
		return update
	}
	update.Status = DeliveryRetryable
	update.NextRetryAt = s.now().UTC().Add(s.retryDelay(update.DurableDeliveryClaim))
	metrics.RecordPushDelivery(DeliveryRetryable)
	return update
}

func (s *Service) retryDelay(claim DurableDeliveryClaim) time.Duration {
	exponent := claim.Attempt - 1
	if exponent < 0 {
		exponent = 0
	}
	delay := time.Duration(float64(s.retryInitial) * math.Pow(2, float64(exponent)))
	if delay <= 0 || delay > s.retryMax {
		delay = s.retryMax
	}
	hash := fnv.New32a()
	_, _ = fmt.Fprintf(hash, "%s:%d:%d", claim.JobID, claim.Token.ID, claim.Attempt)
	jitter := 0.8 + float64(hash.Sum32()%401)/1000
	return time.Duration(float64(delay) * jitter)
}

func waitForWorker(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
