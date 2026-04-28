package dispatch

import (
	"errors"
	"fmt"
	"reflect"
)

var (
	ErrNilPayload          = errors.New("dispatch: nil payload")
	ErrUnregisteredPayload = errors.New("dispatch: unregistered payload type")
	ErrPayloadTypeMismatch = errors.New("dispatch: payload type mismatch")
)

type OneofHandler[Ctx any, Resp any] func(Ctx, any) (Resp, error)

type OneofRouter[Ctx any, Resp any] struct {
	handlers map[reflect.Type]OneofHandler[Ctx, Resp]
}

func NewOneofRouter[Ctx any, Resp any]() *OneofRouter[Ctx, Resp] {
	return &OneofRouter[Ctx, Resp]{
		handlers: make(map[reflect.Type]OneofHandler[Ctx, Resp]),
	}
}

func Register[Ctx any, Resp any, Payload any](router *OneofRouter[Ctx, Resp], handler func(Ctx, Payload) (Resp, error)) {
	var payload Payload
	payloadType := reflect.TypeOf(payload)
	if payloadType == nil {
		panic("dispatch: payload type must be a concrete typed value")
	}
	if _, exists := router.handlers[payloadType]; exists {
		panic(fmt.Sprintf("dispatch: duplicate payload registration for %s", payloadType))
	}

	router.handlers[payloadType] = func(ctx Ctx, rawPayload any) (Resp, error) {
		payload, ok := rawPayload.(Payload)
		if !ok {
			var zero Resp
			return zero, fmt.Errorf("%w: expected %s, got %T", ErrPayloadTypeMismatch, payloadType, rawPayload)
		}
		return handler(ctx, payload)
	}
}

func (r *OneofRouter[Ctx, Resp]) Dispatch(ctx Ctx, payload any) (Resp, error) {
	if payload == nil {
		var zero Resp
		return zero, ErrNilPayload
	}

	payloadType := reflect.TypeOf(payload)
	handler, ok := r.handlers[payloadType]
	if !ok {
		var zero Resp
		return zero, fmt.Errorf("%w: %s", ErrUnregisteredPayload, payloadType)
	}

	return handler(ctx, payload)
}
