package payment

import (
	"Betterfly2/shared/db"
	"errors"
	"testing"
)

type memoryStore struct {
	orders []db.PaymentOrder
	events []db.PaymentEvent
}

func (s *memoryStore) CreateOrder(order *db.PaymentOrder) error {
	order.ID = int64(len(s.orders) + 1)
	s.orders = append(s.orders, *order)
	return nil
}

func (s *memoryStore) FindOrderByNo(orderNo string) (db.PaymentOrder, error) {
	for _, order := range s.orders {
		if order.OrderNo == orderNo {
			return order, nil
		}
	}
	return db.PaymentOrder{}, ErrNotFound
}

func (s *memoryStore) FindOrderByIdempotency(userID int64, idempotencyKey string) (db.PaymentOrder, error) {
	for _, order := range s.orders {
		if order.UserID == userID && order.IdempotencyKey == idempotencyKey {
			return order, nil
		}
	}
	return db.PaymentOrder{}, ErrNotFound
}

func (s *memoryStore) UpdateOrder(order *db.PaymentOrder) error {
	for i := range s.orders {
		if s.orders[i].OrderNo == order.OrderNo {
			s.orders[i] = *order
			return nil
		}
	}
	return ErrNotFound
}

func (s *memoryStore) RecordEvent(event *db.PaymentEvent) error {
	for _, existing := range s.events {
		if existing.Provider == event.Provider && existing.EventID == event.EventID {
			return errors.New("duplicate event")
		}
	}
	event.ID = int64(len(s.events) + 1)
	s.events = append(s.events, *event)
	return nil
}

func (s *memoryStore) FindEvent(provider, eventID string) (db.PaymentEvent, error) {
	for _, event := range s.events {
		if event.Provider == provider && event.EventID == eventID {
			return event, nil
		}
	}
	return db.PaymentEvent{}, ErrNotFound
}

func (s *memoryStore) ListOrders(userID int64, limit int) ([]db.PaymentOrder, error) {
	var result []db.PaymentOrder
	for _, order := range s.orders {
		if userID == 0 || order.UserID == userID {
			result = append(result, order)
		}
	}
	return result, nil
}

func newTestService() (*Service, *memoryStore) {
	store := &memoryStore{}
	provider := &MockProvider{
		baseURL: "http://localhost:8084",
		secret:  "test-secret",
	}
	return NewService(store, provider), store
}

func TestCreateOrderIsIdempotent(t *testing.T) {
	service, store := newTestService()
	req := CreateOrderRequest{
		UserID:         1,
		Subject:        "premium",
		AmountCents:    1200,
		IdempotencyKey: "req-001",
	}

	first, err := service.CreateOrder(req)
	if err != nil {
		t.Fatalf("create first order: %v", err)
	}
	second, err := service.CreateOrder(req)
	if err != nil {
		t.Fatalf("create second order: %v", err)
	}

	if first.OrderNo != second.OrderNo {
		t.Fatalf("expected same order for same idempotency key, got %s and %s", first.OrderNo, second.OrderNo)
	}
	if len(store.orders) != 1 {
		t.Fatalf("expected one persisted order, got %d", len(store.orders))
	}
	if first.Status != StatusPending {
		t.Fatalf("expected pending order, got %s", first.Status)
	}
}

func TestMockPayMarksOrderPaid(t *testing.T) {
	service, store := newTestService()
	order, err := service.CreateOrder(CreateOrderRequest{
		UserID:         1,
		Subject:        "premium",
		AmountCents:    1200,
		IdempotencyKey: "req-001",
	})
	if err != nil {
		t.Fatalf("create order: %v", err)
	}

	req, payload, signature, err := service.MockPay(order.OrderNo)
	if err != nil {
		t.Fatalf("mock pay: %v", err)
	}
	paid, err := service.HandleCallback(req, payload, signature)
	if err != nil {
		t.Fatalf("handle callback: %v", err)
	}

	if paid.Status != StatusPaid {
		t.Fatalf("expected paid order, got %s", paid.Status)
	}
	if len(store.events) != 1 {
		t.Fatalf("expected one payment event, got %d", len(store.events))
	}
}

func TestCallbackRejectsAmountMismatch(t *testing.T) {
	service, store := newTestService()
	order, err := service.CreateOrder(CreateOrderRequest{
		UserID:         1,
		Subject:        "premium",
		AmountCents:    1200,
		IdempotencyKey: "req-001",
	})
	if err != nil {
		t.Fatalf("create order: %v", err)
	}

	req, payload, signature, err := service.MockPay(order.OrderNo)
	if err != nil {
		t.Fatalf("mock pay: %v", err)
	}
	req.AmountCents = 1300
	payload = []byte(`{"event_id":"` + req.EventID + `","event_type":"payment.succeeded","order_no":"` + req.OrderNo + `","amount_cents":1300,"status":"paid"}`)
	signature = service.provider.(*MockProvider).Sign(payload)

	if _, err := service.HandleCallback(req, payload, signature); err == nil {
		t.Fatal("expected amount mismatch error")
	}
	saved, err := store.FindOrderByNo(order.OrderNo)
	if err != nil {
		t.Fatalf("find saved order: %v", err)
	}
	if saved.Status == StatusPaid {
		t.Fatal("amount mismatch should not mark order paid")
	}
}
