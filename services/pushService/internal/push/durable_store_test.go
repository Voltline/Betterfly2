package push

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	pushpb "Betterfly2/proto/push"
	"Betterfly2/shared/db"
	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

func TestMessageFanoutUsesFixedPlaceholderCountForTwentyThousandTargets(t *testing.T) {
	store, mock := newStoreMock(t)
	targets := make([]int64, 20000)
	for index := range targets {
		targets[index] = int64(index + 2)
	}
	request := &pushpb.RequestMessage{Payload: &pushpb.RequestMessage_MessagePush{MessagePush: &pushpb.MessagePushRequest{
		TargetUserIds: targets, SenderUserId: 1, ConversationId: 1, MessageType: "text", MessageId: 20000,
	}}}
	operationKey := "push-service/0/20000"
	jobID := stablePushJobID(operationKey)

	if placeholders := strings.Count(messageFanoutSQL, "?"); placeholders != 10 {
		t.Fatalf("fanout SQL placeholders scale with audience: got=%d want=10", placeholders)
	}
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT \* FROM "messages" WHERE message_id = \$1`).
		WithArgs(int64(20000), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"message_id", "from_user_id", "to_user_id", "is_group", "is_recalled"}).AddRow(20000, 1, 2, false, false))
	mock.ExpectExec(`INSERT INTO "push_jobs"`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`WITH targets AS`).WithArgs(
		sqlmock.AnyArg(), int64(1), PushTypeAPNs, false, int64(20000), jobID,
		DeliveryPending, sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
	).WillReturnResult(sqlmock.NewResult(0, 20000))
	mock.ExpectCommit()

	err := store.db.Transaction(func(tx *gorm.DB) error {
		_, _, persistErr := store.persistMessageJob(tx, operationKey, request, request.GetMessagePush())
		return persistErr
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPersistMessageRecallCancelsPendingOriginalAndFansOutDurably(t *testing.T) {
	store, mock := newStoreMock(t)
	operationKey := "push-service/1/77"
	recalledAt := "2026-07-21T05:00:00Z"
	request := &pushpb.RequestMessage{Payload: &pushpb.RequestMessage_MessageRecall{MessageRecall: &pushpb.MessageRecallPushRequest{
		TargetUserIds: []int64{2, 3}, MessageId: 77, ConversationId: 9001, IsGroup: true,
		OperatorUserId: 1, RecalledAt: recalledAt,
	}}}

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT \* FROM "messages" WHERE message_id = \$1`).
		WithArgs(int64(77), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{
			"message_id", "from_user_id", "to_user_id", "is_group", "is_recalled", "recalled_at", "recalled_by",
		}).AddRow(77, 1, 9001, true, true, recalledAt, 1))
	mock.ExpectExec(`INSERT INTO "push_jobs"`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE "push_message_deliveries" SET`).WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec(`UPDATE push_jobs SET status`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`WITH targets AS`).WithArgs(
		sqlmock.AnyArg(), int64(-77), stableMessageRecallJobID(77), DeliveryPending,
		sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
	).WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectCommit()

	err := store.db.Transaction(func(tx *gorm.DB) error {
		_, _, persistErr := store.persistMessageRecallJob(tx, operationKey, request, request.GetMessageRecall())
		return persistErr
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestOriginalMessagePushIsSuppressedAfterRecall(t *testing.T) {
	store, mock := newStoreMock(t)
	request := &pushpb.RequestMessage{Payload: &pushpb.RequestMessage_MessagePush{MessagePush: &pushpb.MessagePushRequest{
		TargetUserIds: []int64{2}, SenderUserId: 1, ConversationId: 1, MessageType: "text", MessageId: 78,
	}}}
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT \* FROM "messages" WHERE message_id = \$1`).
		WithArgs(int64(78), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{
			"message_id", "from_user_id", "to_user_id", "is_group", "is_recalled",
		}).AddRow(78, 1, 2, false, true))
	mock.ExpectCommit()

	err := store.db.Transaction(func(tx *gorm.DB) error {
		_, _, persistErr := store.persistMessageJob(tx, "push-service/1/78", request, request.GetMessagePush())
		return persistErr
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareMessageRecallDelivery(t *testing.T) {
	recalledAt := time.Date(2026, 7, 21, 5, 0, 0, 0, time.UTC)
	request := &pushpb.RequestMessage{Payload: &pushpb.RequestMessage_MessageRecall{MessageRecall: &pushpb.MessageRecallPushRequest{
		TargetUserIds: []int64{2}, MessageId: 79, ConversationId: 1, OperatorUserId: 1,
		RecalledAt: recalledAt.Format(time.RFC3339),
	}}}
	payload, err := proto.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(&memoryStore{}, &memorySender{}, "com.Voltline.Betterfly2")
	prepared := service.prepareDeliveries(context.Background(), deliveryKindMessage, []DurableDeliveryClaim{{
		JobID: stableMessageRecallJobID(79), MessageID: -79, RequestPayload: payload,
		Token: db.PushDeviceToken{ID: 9, UserID: 2, Token: "token", Environment: "production", PushType: PushTypeAPNs, IsActive: true},
	}})
	if len(prepared) != 1 || prepared[0].prepareErr != nil {
		t.Fatalf("unexpected prepared recall: %+v", prepared)
	}
	notification := prepared[0].notification
	if notification.Kind != NotificationRecall || notification.MessageID != 79 || notification.TargetUserID != 2 || notification.ConversationID != 1 || notification.SenderUserID != 1 || !notification.SentAt.Equal(recalledAt) {
		t.Fatalf("unexpected recall notification: %+v", notification)
	}
}

func TestDurableFinalizeRejectsExpiredWorkerClaim(t *testing.T) {
	store, mock := newStoreMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT \* FROM "push_jobs"`).WillReturnRows(
		sqlmock.NewRows([]string{"job_id", "status"}).AddRow("job-1", PushJobPending),
	)
	mock.ExpectExec(`UPDATE "push_message_deliveries"`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()

	err := store.FinalizeMessageDelivery(context.Background(), DurableDeliveryUpdate{
		DurableDeliveryClaim: DurableDeliveryClaim{
			JobID: "job-1", MessageID: 41, Token: db.PushDeviceToken{ID: 9}, Attempt: 1, ClaimToken: "expired-claim",
		},
		Status: DeliverySent,
	})
	if !errors.Is(err, ErrDeliveryFenced) {
		t.Fatalf("expired worker updated a newer claim: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRecallDeliveryWithNegativeLedgerKeyCanFinalize(t *testing.T) {
	store, mock := newStoreMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT \* FROM "push_jobs"`).WillReturnRows(
		sqlmock.NewRows([]string{"job_id", "status"}).AddRow(stableMessageRecallJobID(77), PushJobPending),
	)
	mock.ExpectExec(`UPDATE "push_message_deliveries"`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT count\(\*\) FROM "push_message_deliveries"`).WillReturnRows(
		sqlmock.NewRows([]string{"count"}).AddRow(0),
	)
	mock.ExpectExec(`UPDATE "push_jobs"`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	err := store.FinalizeMessageDelivery(context.Background(), DurableDeliveryUpdate{
		DurableDeliveryClaim: DurableDeliveryClaim{
			JobID: stableMessageRecallJobID(77), MessageID: -77, Token: db.PushDeviceToken{ID: 9},
			Attempt: 1, ClaimToken: "recall-claim",
		},
		Status: DeliverySent,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestExpiredFinalAttemptIsFencedIntoTerminalStateAfterRestart(t *testing.T) {
	store, mock := newStoreMock(t)
	now := time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)
	mock.ExpectBegin()
	mock.ExpectQuery(`UPDATE push_message_deliveries SET`).WillReturnRows(
		sqlmock.NewRows([]string{"job_id"}).AddRow("job-exhausted"),
	)
	mock.ExpectQuery(`SELECT \* FROM "push_jobs"`).WillReturnRows(
		sqlmock.NewRows([]string{"job_id", "status"}).AddRow("job-exhausted", PushJobPending),
	)
	mock.ExpectQuery(`SELECT count\(\*\) FROM "push_message_deliveries"`).WillReturnRows(
		sqlmock.NewRows([]string{"count"}).AddRow(0),
	)
	mock.ExpectExec(`UPDATE "push_jobs"`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`WITH candidates AS`).WillReturnRows(sqlmock.NewRows([]string{
		"message_id", "token_id", "job_id", "attempt", "claim_token", "delivery_created_at", "user_id", "device_id", "token", "environment", "push_type", "bundle_id", "is_active", "token_created_at", "token_updated_at", "request_payload",
	}))
	mock.ExpectCommit()

	claims, err := store.ClaimMessageDeliveryBatch(context.Background(), 10, now, 30*time.Second, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(claims) != 0 {
		t.Fatalf("exhausted delivery was sent again: %+v", claims)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestFinalizeSerializesTerminalJobDecisionWithRowLock(t *testing.T) {
	store, mock := newStoreMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT \* FROM "push_jobs"`).WillReturnRows(
		sqlmock.NewRows([]string{"job_id", "status"}).AddRow("job-last", PushJobPending),
	)
	mock.ExpectExec(`UPDATE "push_message_deliveries"`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT count\(\*\) FROM "push_message_deliveries"`).WillReturnRows(
		sqlmock.NewRows([]string{"count"}).AddRow(0),
	)
	mock.ExpectExec(`UPDATE "push_jobs"`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	err := store.FinalizeMessageDelivery(context.Background(), DurableDeliveryUpdate{
		DurableDeliveryClaim: DurableDeliveryClaim{
			JobID: "job-last", MessageID: 99, Token: db.PushDeviceToken{ID: 12}, Attempt: 2, ClaimToken: "current-claim",
		},
		Status: DeliverySent,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("job completion was not serialized before delivery finalization: %v", err)
	}
}

func TestRegisterTokenReplayUpdatesExistingIdentityWithoutReplacingID(t *testing.T) {
	store, mock := newStoreMock(t)
	now := time.Date(2026, 7, 15, 1, 0, 0, 0, time.UTC)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT \* FROM "push_device_tokens"`).WillReturnRows(sqlmock.NewRows([]string{
		"id", "user_id", "device_id", "token", "environment", "push_type", "bundle_id", "is_active", "created_at", "updated_at",
	}).AddRow(42, 7, "iphone", "old-token", "production", PushTypeAPNs, "com.Voltline.Betterfly2", true, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)))
	mock.ExpectExec(`DELETE FROM "push_device_tokens"`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`UPDATE "push_device_tokens"`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	err := store.db.Transaction(func(tx *gorm.DB) error {
		return registerTokenWithDB(tx, 7, "iphone", "new-token", "production", PushTypeAPNs, "com.Voltline.Betterfly2", now)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("register replay deleted and recreated the identity instead of updating ID 42: %v", err)
	}
}

type restartDurableStore struct {
	*memoryStore
	mu          sync.Mutex
	base        DurableDeliveryClaim
	status      string
	attempt     int
	claimToken  string
	leaseUntil  time.Time
	nextRetryAt time.Time
}

func (s *restartDurableStore) EnqueueRequest(context.Context, string, *pushpb.RequestMessage, string) error {
	return nil
}

func (s *restartDurableStore) ClaimMessageDeliveryBatch(_ context.Context, _ int, now time.Time, lease time.Duration, _ int) ([]DurableDeliveryClaim, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	eligible := s.status == DeliveryPending || s.status == DeliveryRetryable && !s.nextRetryAt.After(now) || s.status == DeliveryClaimed && !s.leaseUntil.After(now)
	if !eligible || s.status == DeliverySent || s.status == DeliveryPermanent || s.status == DeliveryFailed {
		return nil, nil
	}
	s.attempt++
	s.status = DeliveryClaimed
	s.claimToken = fmt.Sprintf("claim-%d", s.attempt)
	s.leaseUntil = now.Add(lease)
	claim := s.base
	claim.Attempt = s.attempt
	claim.ClaimToken = s.claimToken
	return []DurableDeliveryClaim{claim}, nil
}

func (s *restartDurableStore) ClaimVoIPDeliveryBatch(context.Context, int, time.Time, time.Duration, int) ([]DurableDeliveryClaim, error) {
	return nil, nil
}

func (s *restartDurableStore) FinalizeMessageDelivery(_ context.Context, update DurableDeliveryUpdate) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status != DeliveryClaimed || s.attempt != update.Attempt || s.claimToken != update.ClaimToken {
		return ErrDeliveryFenced
	}
	s.status = update.Status
	s.nextRetryAt = update.NextRetryAt
	return nil
}

func (s *restartDurableStore) FinalizeVoIPDelivery(context.Context, DurableDeliveryUpdate) error {
	return nil
}

func TestRetryableDeliveryResumesAfterServiceRestart(t *testing.T) {
	now := time.Date(2026, 7, 15, 2, 0, 0, 0, time.UTC)
	request := &pushpb.RequestMessage{Payload: &pushpb.RequestMessage_MessagePush{MessagePush: &pushpb.MessagePushRequest{
		TargetUserIds: []int64{2}, SenderUserId: 1, ConversationId: 1, MessageType: "text", MessageId: 51,
	}}}
	payload, err := proto.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	store := &restartDurableStore{
		memoryStore: &memoryStore{}, status: DeliveryPending,
		base: DurableDeliveryClaim{
			JobID: "job-restart", MessageID: 51, RequestPayload: payload,
			Token: db.PushDeviceToken{ID: 11, UserID: 2, Token: "device-token", Environment: "production", PushType: PushTypeAPNs, IsActive: true},
		},
	}

	first := NewService(store, &memorySender{err: errors.New("temporary APNs network error")}, "com.Voltline.Betterfly2")
	first.now = func() time.Time { return now }
	first.retryInitial = time.Second
	first.retryMax = time.Second
	claims, err := store.ClaimMessageDeliveryBatch(context.Background(), 1, now, first.deliveryLease, first.maxAttempts)
	if err != nil || len(claims) != 1 {
		t.Fatalf("initial claim failed: claims=%d err=%v", len(claims), err)
	}
	if err := first.processDurableBatch(context.Background(), store, deliveryKindMessage, first.prepareDeliveries(context.Background(), deliveryKindMessage, claims)); err != nil {
		t.Fatal(err)
	}
	if store.status != DeliveryRetryable {
		t.Fatalf("network failure was not persisted for restart: status=%s", store.status)
	}

	now = now.Add(2 * time.Second)
	secondSender := &memorySender{}
	second := NewService(store, secondSender, "com.Voltline.Betterfly2")
	second.now = func() time.Time { return now }
	claims, err = store.ClaimMessageDeliveryBatch(context.Background(), 1, now, second.deliveryLease, second.maxAttempts)
	if err != nil || len(claims) != 1 || claims[0].Attempt != 2 {
		t.Fatalf("restarted service did not reclaim retryable row: claims=%+v err=%v", claims, err)
	}
	if err := second.processDurableBatch(context.Background(), store, deliveryKindMessage, second.prepareDeliveries(context.Background(), deliveryKindMessage, claims)); err != nil {
		t.Fatal(err)
	}
	if store.status != DeliverySent || len(secondSender.sent) != 1 {
		t.Fatalf("restart recovery did not finish delivery: status=%s sends=%d", store.status, len(secondSender.sent))
	}
}

func TestPresentationDatabaseFailureIsPersistedAsRetryable(t *testing.T) {
	now := time.Date(2026, 7, 15, 3, 0, 0, 0, time.UTC)
	request := &pushpb.RequestMessage{Payload: &pushpb.RequestMessage_MessagePush{MessagePush: &pushpb.MessagePushRequest{
		TargetUserIds: []int64{2}, SenderUserId: 1, ConversationId: 1, MessageType: "text", MessageId: 52,
	}}}
	payload, err := proto.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	store := &restartDurableStore{
		memoryStore: &memoryStore{presentationErr: errors.New("database temporarily unavailable")}, status: DeliveryPending,
		base: DurableDeliveryClaim{
			JobID: "job-presentation-retry", MessageID: 52, RequestPayload: payload,
			Token: db.PushDeviceToken{ID: 12, UserID: 2, Token: "device-token", Environment: "production", PushType: PushTypeAPNs, IsActive: true},
		},
	}
	service := NewService(store, &memorySender{}, "com.Voltline.Betterfly2")
	service.now = func() time.Time { return now }
	service.retryInitial = time.Second
	service.retryMax = time.Second

	claims, err := store.ClaimMessageDeliveryBatch(context.Background(), 1, now, service.deliveryLease, service.maxAttempts)
	if err != nil || len(claims) != 1 {
		t.Fatalf("claim failed: claims=%d err=%v", len(claims), err)
	}
	prepared := service.prepareDeliveries(context.Background(), deliveryKindMessage, claims)
	if len(prepared) != 1 || !prepared[0].prepareTransient {
		t.Fatalf("database preparation failure was not classified transient: %+v", prepared)
	}
	if err := service.processDurableBatch(context.Background(), store, deliveryKindMessage, prepared); err != nil {
		t.Fatal(err)
	}
	if store.status != DeliveryRetryable {
		t.Fatalf("database preparation failure became terminal: status=%s", store.status)
	}

	store.presentationErr = nil
	now = now.Add(2 * time.Second)
	claims, err = store.ClaimMessageDeliveryBatch(context.Background(), 1, now, service.deliveryLease, service.maxAttempts)
	if err != nil || len(claims) != 1 {
		t.Fatalf("retry claim failed: claims=%d err=%v", len(claims), err)
	}
	if err := service.processDurableBatch(context.Background(), store, deliveryKindMessage, service.prepareDeliveries(context.Background(), deliveryKindMessage, claims)); err != nil {
		t.Fatal(err)
	}
	if store.status != DeliverySent {
		t.Fatalf("delivery did not recover after database returned: status=%s", store.status)
	}
}

type boundaryDelivery struct {
	claim      DurableDeliveryClaim
	status     string
	attempt    int
	claimToken string
	leaseUntil time.Time
}

type boundaryStore struct {
	*memoryStore
	mu         sync.Mutex
	deliveries []*boundaryDelivery
	claimCount map[int64]int
	maxLimit   int
}

func (s *boundaryStore) EnqueueRequest(context.Context, string, *pushpb.RequestMessage, string) error {
	return nil
}

func (s *boundaryStore) ClaimMessageDeliveryBatch(_ context.Context, limit int, now time.Time, lease time.Duration, _ int) ([]DurableDeliveryClaim, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit > s.maxLimit {
		s.maxLimit = limit
	}
	claims := make([]DurableDeliveryClaim, 0, limit)
	for _, delivery := range s.deliveries {
		eligible := delivery.status == DeliveryPending || delivery.status == DeliveryClaimed && !delivery.leaseUntil.After(now)
		if !eligible || len(claims) == limit {
			continue
		}
		delivery.status = DeliveryClaimed
		delivery.attempt++
		delivery.claimToken = fmt.Sprintf("claim-%d-%d", delivery.claim.Token.ID, delivery.attempt)
		delivery.leaseUntil = now.Add(lease)
		claim := delivery.claim
		claim.Attempt = delivery.attempt
		claim.ClaimToken = delivery.claimToken
		claims = append(claims, claim)
		s.claimCount[claim.Token.ID]++
	}
	return claims, nil
}

func (s *boundaryStore) ClaimVoIPDeliveryBatch(context.Context, int, time.Time, time.Duration, int) ([]DurableDeliveryClaim, error) {
	return nil, nil
}

func (s *boundaryStore) FinalizeMessageDelivery(_ context.Context, update DurableDeliveryUpdate) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, delivery := range s.deliveries {
		if delivery.claim.Token.ID != update.Token.ID {
			continue
		}
		if delivery.status != DeliveryClaimed || delivery.attempt != update.Attempt || delivery.claimToken != update.ClaimToken {
			return ErrDeliveryFenced
		}
		delivery.status = update.Status
		return nil
	}
	return ErrDeliveryFenced
}

func (s *boundaryStore) FinalizeVoIPDelivery(context.Context, DurableDeliveryUpdate) error {
	return nil
}

type blockedSender struct {
	started chan struct{}
	release chan struct{}
}

func (s *blockedSender) Ready() error { return nil }
func (s *blockedSender) Send(ctx context.Context, _ Notification) (SendResult, error) {
	select {
	case s.started <- struct{}{}:
	default:
	}
	select {
	case <-s.release:
		return SendResult{APNSID: "released"}, nil
	case <-ctx.Done():
		return SendResult{}, ctx.Err()
	}
}

func TestTwoWorkersDoNotReclaimQueuedDeliveryNearLeaseBoundary(t *testing.T) {
	request := &pushpb.RequestMessage{Payload: &pushpb.RequestMessage_MessagePush{MessagePush: &pushpb.MessagePushRequest{
		TargetUserIds: []int64{2, 3}, SenderUserId: 1, ConversationId: 1, MessageType: "text", MessageId: 88,
	}}}
	payload, err := proto.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	store := &boundaryStore{
		memoryStore: &memoryStore{}, claimCount: make(map[int64]int),
		deliveries: []*boundaryDelivery{
			{status: DeliveryPending, claim: DurableDeliveryClaim{JobID: "job-boundary", MessageID: 88, RequestPayload: payload, Token: db.PushDeviceToken{ID: 1, UserID: 2, Token: "one", Environment: "production", PushType: PushTypeAPNs, IsActive: true}}},
			{status: DeliveryPending, claim: DurableDeliveryClaim{JobID: "job-boundary", MessageID: 88, RequestPayload: payload, Token: db.PushDeviceToken{ID: 2, UserID: 3, Token: "two", Environment: "production", PushType: PushTypeAPNs, IsActive: true}}},
		},
	}
	blocked := &blockedSender{started: make(chan struct{}, 1), release: make(chan struct{})}
	first := NewService(store, blocked, "com.Voltline.Betterfly2")
	secondSender := &memorySender{}
	second := NewService(store, secondSender, "com.Voltline.Betterfly2")
	for _, service := range []*Service{first, second} {
		service.maxConcurrency = 1
		service.deliveryLease = 200 * time.Millisecond
		service.sendTimeout = 150 * time.Millisecond
		service.workerPoll = 5 * time.Millisecond
	}
	firstCtx, cancelFirst := context.WithCancel(context.Background())
	secondCtx, cancelSecond := context.WithCancel(context.Background())
	defer cancelFirst()
	defer cancelSecond()
	firstDone := make(chan struct{})
	go func() {
		_ = first.runDeliveryLoop(firstCtx, store, deliveryKindMessage)
		close(firstDone)
	}()
	select {
	case <-blocked.started:
	case <-time.After(time.Second):
		t.Fatal("first worker did not enter APNs send slot")
	}

	time.Sleep(120 * time.Millisecond)
	secondDone := make(chan struct{})
	go func() {
		_ = second.runDeliveryLoop(secondCtx, store, deliveryKindMessage)
		close(secondDone)
	}()
	deadline := time.Now().Add(50 * time.Millisecond)
	for time.Now().Before(deadline) {
		secondSender.mu.Lock()
		sent := len(secondSender.sent)
		secondSender.mu.Unlock()
		if sent > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	close(blocked.release)
	secondSender.mu.Lock()
	secondSent := append([]Notification(nil), secondSender.sent...)
	secondSender.mu.Unlock()
	if len(secondSent) != 1 || secondSent[0].Token != "two" {
		t.Fatalf("second worker did not take the unclaimed delivery: %+v", secondSent)
	}
	cancelFirst()
	cancelSecond()
	for _, done := range []<-chan struct{}{firstDone, secondDone} {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("worker did not stop after cancellation")
		}
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.maxLimit != 1 || store.claimCount[1] != 1 || store.claimCount[2] != 1 {
		t.Fatalf("claimed work waited outside sender slots or was reclaimed early: limit=%d claims=%v", store.maxLimit, store.claimCount)
	}
}
