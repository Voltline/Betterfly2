package db

import (
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

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
		CreatedAt:       FormatReliabilityTime(time.Now()),
	}).Error; err != nil {
		return err
	}
	return nil
}
