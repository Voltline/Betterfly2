package payment

import (
	"Betterfly2/shared/db"
	"errors"

	"gorm.io/gorm"
)

type Store interface {
	CreateOrder(order *db.PaymentOrder) error
	FindOrderByNo(orderNo string) (db.PaymentOrder, error)
	FindOrderByIdempotency(userID int64, idempotencyKey string) (db.PaymentOrder, error)
	UpdateOrder(order *db.PaymentOrder) error
	RecordEvent(event *db.PaymentEvent) error
	FindEvent(provider, eventID string) (db.PaymentEvent, error)
	ListOrders(userID int64, limit int) ([]db.PaymentOrder, error)
}

type GormStore struct{}

func NewGormStore() *GormStore {
	_ = db.DB(&db.PaymentOrder{}, &db.PaymentEvent{})
	return &GormStore{}
}

func (s *GormStore) CreateOrder(order *db.PaymentOrder) error {
	return db.DB().Create(order).Error
}

func (s *GormStore) FindOrderByNo(orderNo string) (db.PaymentOrder, error) {
	var order db.PaymentOrder
	err := db.DB().Where("order_no = ?", orderNo).First(&order).Error
	return order, normalizeNotFound(err)
}

func (s *GormStore) FindOrderByIdempotency(userID int64, idempotencyKey string) (db.PaymentOrder, error) {
	var order db.PaymentOrder
	err := db.DB().
		Where("user_id = ? AND idempotency_key = ?", userID, idempotencyKey).
		Order("created_at DESC").
		First(&order).Error
	return order, normalizeNotFound(err)
}

func (s *GormStore) UpdateOrder(order *db.PaymentOrder) error {
	return db.DB().Save(order).Error
}

func (s *GormStore) RecordEvent(event *db.PaymentEvent) error {
	return db.DB().Create(event).Error
}

func (s *GormStore) FindEvent(provider, eventID string) (db.PaymentEvent, error) {
	var event db.PaymentEvent
	err := db.DB().Where("provider = ? AND event_id = ?", provider, eventID).First(&event).Error
	return event, normalizeNotFound(err)
}

func (s *GormStore) ListOrders(userID int64, limit int) ([]db.PaymentOrder, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	var orders []db.PaymentOrder
	query := db.DB().Order("created_at DESC").Limit(limit)
	if userID > 0 {
		query = query.Where("user_id = ?", userID)
	}
	return orders, query.Find(&orders).Error
}

func normalizeNotFound(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrNotFound
	}
	return err
}
