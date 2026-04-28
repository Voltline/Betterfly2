package handlers

import (
	pb "Betterfly2/proto/data_forwarding"
	"data_forwarding_service/internal/utils"
	"fmt"
	"reflect"
)

func authenticatedPayload[T any](
	fromID int64,
	message *pb.RequestMessage,
	action string,
	payloadName string,
	getter func(*pb.RequestMessage) T,
) (T, error) {
	var zero T

	if message.GetJwt() == "" {
		return zero, fmt.Errorf("用户未携带有效JWT，无法%s", action)
	}
	if err := utils.ValidateAndParseJWT(fromID, message.GetJwt()); err != nil {
		return zero, err
	}

	payload := getter(message)
	if isNilPayload(payload) {
		return zero, fmt.Errorf("%s消息为空", payloadName)
	}
	return payload, nil
}

func isNilPayload(payload any) bool {
	if payload == nil {
		return true
	}

	value := reflect.ValueOf(payload)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func requirePositiveID(name string, id int64) error {
	if id <= 0 {
		return fmt.Errorf("%s非法", name)
	}
	return nil
}

func requireNonSelfID(name string, id int64, fromID int64) error {
	if id <= 0 || id == fromID {
		return fmt.Errorf("%s非法", name)
	}
	return nil
}
