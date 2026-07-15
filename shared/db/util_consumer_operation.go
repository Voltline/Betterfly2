package db

import (
	"errors"
	"sync/atomic"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var consumerOperationWrites atomic.Uint64

func LoadConsumerOperationResult(service, operationKey string) ([]byte, error) {
	var result ConsumerOperationResult
	err := DB().Where("service = ? AND operation_key = ?", service, operationKey).First(&result).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return append([]byte(nil), result.ResponsePayload...), nil
}

func SaveConsumerOperationResult(service, operationKey string, payload []byte) error {
	database := DB()
	if err := database.Clauses(clause.OnConflict{DoNothing: true}).Create(&ConsumerOperationResult{
		Service: service, OperationKey: operationKey,
		ResponsePayload: append([]byte(nil), payload...),
		CreatedAt:       time.Now().UTC().Format(time.RFC3339Nano),
	}).Error; err != nil {
		return err
	}
	if consumerOperationWrites.Add(1)%1000 == 0 {
		cutoff := time.Now().UTC().Add(-7 * 24 * time.Hour).Format(time.RFC3339Nano)
		_ = database.Where("created_at < ?", cutoff).Delete(&ConsumerOperationResult{}).Error
	}
	return nil
}
