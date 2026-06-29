package payment

import (
	"Betterfly2/shared/db"
	"Betterfly2/shared/utils"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrNotFound       = errors.New("not found")
	ErrForbidden      = errors.New("forbidden")
	ErrInvalidRequest = errors.New("invalid request")
)

type Service struct {
	store    Store
	provider Provider
}

func NewService(store Store, provider Provider) *Service {
	return &Service{store: store, provider: provider}
}

func (s *Service) CreateOrder(req CreateOrderRequest) (OrderResponse, error) {
	req.Provider = firstNonEmpty(strings.TrimSpace(req.Provider), s.provider.Name())
	req.Currency = firstNonEmpty(strings.TrimSpace(req.Currency), DefaultCurrency)
	req.Subject = strings.TrimSpace(req.Subject)
	req.IdempotencyKey = strings.TrimSpace(req.IdempotencyKey)
	req.Description = strings.TrimSpace(req.Description)

	if req.UserID <= 0 || req.Subject == "" || req.AmountCents <= 0 || req.IdempotencyKey == "" {
		return OrderResponse{}, fmt.Errorf("%w: user_id, subject, positive amount_cents and idempotency_key are required", ErrInvalidRequest)
	}
	if req.Provider != s.provider.Name() {
		return OrderResponse{}, fmt.Errorf("%w: unsupported provider %s", ErrInvalidRequest, req.Provider)
	}

	existing, err := s.store.FindOrderByIdempotency(req.UserID, req.IdempotencyKey)
	if err == nil {
		return s.toResponse(existing), nil
	}
	if !errors.Is(err, ErrNotFound) {
		return OrderResponse{}, err
	}

	now := utils.NowTime()
	expiresAt := time.Now().UTC().Add(expireDuration(req.ExpireSeconds)).Format(time.RFC3339)
	order := db.PaymentOrder{
		OrderNo:           newOrderNo(),
		UserID:            req.UserID,
		Subject:           req.Subject,
		Description:       req.Description,
		AmountCents:       req.AmountCents,
		Currency:          req.Currency,
		Status:            StatusCreated,
		Provider:          req.Provider,
		IdempotencyKey:    req.IdempotencyKey,
		ClientPayloadJSON: encodeStringMap(req.ClientPayload),
		ExpiresAt:         expiresAt,
		CreatedAt:         now,
		UpdatedAt:         now,
	}

	if err := s.store.CreateOrder(&order); err != nil {
		if existing, findErr := s.store.FindOrderByIdempotency(req.UserID, req.IdempotencyKey); findErr == nil {
			return s.toResponse(existing), nil
		}
		return OrderResponse{}, err
	}

	providerResp, err := s.provider.CreateOrder(ProviderCreateOrderRequest{
		OrderNo:       order.OrderNo,
		Subject:       order.Subject,
		AmountCents:   order.AmountCents,
		Currency:      order.Currency,
		ExpireAt:      order.ExpiresAt,
		ClientPayload: req.ClientPayload,
	})
	if err != nil {
		order.Status = StatusFailed
		order.UpdatedAt = utils.NowTime()
		_ = s.store.UpdateOrder(&order)
		return OrderResponse{}, err
	}
	order.Status = StatusPending
	order.ProviderTradeNo = providerResp.ProviderTradeNo
	order.PayURL = providerResp.PayURL
	order.ProviderPayloadJSON = encodeStringMap(providerResp.RawPayload)
	order.UpdatedAt = utils.NowTime()

	if err := s.store.UpdateOrder(&order); err != nil {
		return OrderResponse{}, err
	}
	return s.toResponse(order), nil
}

func (s *Service) GetOrder(userID int64, orderNo string) (OrderResponse, error) {
	order, err := s.store.FindOrderByNo(strings.TrimSpace(orderNo))
	if err != nil {
		return OrderResponse{}, err
	}
	if userID > 0 && order.UserID != userID {
		return OrderResponse{}, ErrForbidden
	}
	return s.toResponse(order), nil
}

func (s *Service) ListOrders(userID int64, limit int) ([]OrderResponse, error) {
	orders, err := s.store.ListOrders(userID, limit)
	if err != nil {
		return nil, err
	}
	result := make([]OrderResponse, 0, len(orders))
	for _, order := range orders {
		result = append(result, s.toResponse(order))
	}
	return result, nil
}

func (s *Service) HandleCallback(req CallbackRequest, rawPayload []byte, signature string) (OrderResponse, error) {
	req.Provider = firstNonEmpty(strings.TrimSpace(req.Provider), s.provider.Name())
	req.Status = strings.TrimSpace(req.Status)
	req.EventID = strings.TrimSpace(req.EventID)
	req.EventType = strings.TrimSpace(req.EventType)
	req.OrderNo = strings.TrimSpace(req.OrderNo)

	if req.Provider != s.provider.Name() {
		return OrderResponse{}, fmt.Errorf("%w: unsupported provider %s", ErrInvalidRequest, req.Provider)
	}
	if req.EventID == "" || req.EventType == "" || req.OrderNo == "" {
		return OrderResponse{}, fmt.Errorf("%w: event_id, event_type and order_no are required", ErrInvalidRequest)
	}
	if err := s.provider.VerifyCallback(rawPayload, signature); err != nil {
		return OrderResponse{}, err
	}

	if _, err := s.store.FindEvent(req.Provider, req.EventID); err == nil {
		order, err := s.store.FindOrderByNo(req.OrderNo)
		if err != nil {
			return OrderResponse{}, err
		}
		return s.toResponse(order), nil
	} else if !errors.Is(err, ErrNotFound) {
		return OrderResponse{}, err
	}

	order, err := s.store.FindOrderByNo(req.OrderNo)
	if err != nil {
		_ = s.recordEvent(req, rawPayload, EventStatusError, err)
		return OrderResponse{}, err
	}
	if req.AmountCents > 0 && req.AmountCents != order.AmountCents {
		err := fmt.Errorf("%w: callback amount mismatch", ErrInvalidRequest)
		_ = s.recordEvent(req, rawPayload, EventStatusError, err)
		return OrderResponse{}, err
	}

	now := utils.NowTime()
	switch req.Status {
	case StatusPaid:
		if order.Status != StatusPaid {
			order.Status = StatusPaid
			order.ProviderTradeNo = firstNonEmpty(strings.TrimSpace(req.ProviderTradeNo), order.ProviderTradeNo)
			order.PaidAt = firstNonEmpty(strings.TrimSpace(req.PaidAt), now)
			order.UpdatedAt = now
			if err := s.store.UpdateOrder(&order); err != nil {
				_ = s.recordEvent(req, rawPayload, EventStatusError, err)
				return OrderResponse{}, err
			}
		}
	case StatusFailed, StatusClosed:
		if order.Status != StatusPaid {
			order.Status = req.Status
			order.UpdatedAt = now
			if err := s.store.UpdateOrder(&order); err != nil {
				_ = s.recordEvent(req, rawPayload, EventStatusError, err)
				return OrderResponse{}, err
			}
		}
	default:
		err := fmt.Errorf("%w: unsupported callback status %s", ErrInvalidRequest, req.Status)
		_ = s.recordEvent(req, rawPayload, EventStatusError, err)
		return OrderResponse{}, err
	}

	if err := s.recordEvent(req, rawPayload, EventStatusOK, nil); err != nil {
		return OrderResponse{}, err
	}
	return s.toResponse(order), nil
}

func (s *Service) MockPay(orderNo string) (CallbackRequest, []byte, string, error) {
	order, err := s.store.FindOrderByNo(strings.TrimSpace(orderNo))
	if err != nil {
		return CallbackRequest{}, nil, "", err
	}
	req := CallbackRequest{
		Provider:        s.provider.Name(),
		EventID:         "mock_" + newRandomHex(8),
		EventType:       "payment.succeeded",
		OrderNo:         order.OrderNo,
		ProviderTradeNo: "mock_trade_" + newRandomHex(8),
		AmountCents:     order.AmountCents,
		Status:          StatusPaid,
		PaidAt:          utils.NowTime(),
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return CallbackRequest{}, nil, "", err
	}
	mockProvider, ok := s.provider.(*MockProvider)
	if !ok {
		return CallbackRequest{}, nil, "", errors.New("mock pay is only available for mock provider")
	}
	return req, payload, mockProvider.Sign(payload), nil
}

func (s *Service) recordEvent(req CallbackRequest, rawPayload []byte, status string, eventErr error) error {
	errText := ""
	if eventErr != nil {
		errText = eventErr.Error()
	}
	raw := string(rawPayload)
	if strings.TrimSpace(req.RawPayload) != "" {
		raw = req.RawPayload
	}
	return s.store.RecordEvent(&db.PaymentEvent{
		OrderNo:    req.OrderNo,
		Provider:   req.Provider,
		EventID:    req.EventID,
		EventType:  req.EventType,
		Status:     status,
		RawPayload: raw,
		Error:      errText,
		CreatedAt:  utils.NowTime(),
	})
}

func (s *Service) toResponse(order db.PaymentOrder) OrderResponse {
	return orderFromModel(order, decodeStringMap(order.ClientPayloadJSON))
}

func expireDuration(seconds int64) time.Duration {
	if seconds <= 0 {
		return 30 * time.Minute
	}
	if seconds > 7*24*3600 {
		seconds = 7 * 24 * 3600
	}
	return time.Duration(seconds) * time.Second
}

func newOrderNo() string {
	return "pay_" + time.Now().UTC().Format("20060102150405") + "_" + newRandomHex(6)
}

func newRandomHex(size int) string {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
