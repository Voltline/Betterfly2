package payment

import "Betterfly2/shared/db"

const (
	StatusCreated = "created"
	StatusPending = "pending"
	StatusPaid    = "paid"
	StatusClosed  = "closed"
	StatusFailed  = "failed"

	EventStatusOK    = "ok"
	EventStatusError = "error"

	DefaultCurrency = "CNY"
	DefaultProvider = "mock"
)

type CreateOrderRequest struct {
	UserID         int64             `json:"user_id,omitempty"`
	Subject        string            `json:"subject"`
	Description    string            `json:"description,omitempty"`
	AmountCents    int64             `json:"amount_cents"`
	Currency       string            `json:"currency,omitempty"`
	Provider       string            `json:"provider,omitempty"`
	IdempotencyKey string            `json:"idempotency_key,omitempty"`
	ClientPayload  map[string]string `json:"client_payload,omitempty"`
	ExpireSeconds  int64             `json:"expire_seconds,omitempty"`
}

type OrderResponse struct {
	OrderNo         string            `json:"order_no"`
	UserID          int64             `json:"user_id"`
	Subject         string            `json:"subject"`
	Description     string            `json:"description,omitempty"`
	AmountCents     int64             `json:"amount_cents"`
	Currency        string            `json:"currency"`
	Status          string            `json:"status"`
	Provider        string            `json:"provider"`
	ProviderTradeNo string            `json:"provider_trade_no,omitempty"`
	PayURL          string            `json:"pay_url,omitempty"`
	ClientPayload   map[string]string `json:"client_payload,omitempty"`
	ExpiresAt       string            `json:"expires_at,omitempty"`
	PaidAt          string            `json:"paid_at,omitempty"`
	CreatedAt       string            `json:"created_at"`
	UpdatedAt       string            `json:"updated_at"`
}

type CallbackRequest struct {
	Provider        string `json:"provider,omitempty"`
	EventID         string `json:"event_id"`
	EventType       string `json:"event_type"`
	OrderNo         string `json:"order_no"`
	ProviderTradeNo string `json:"provider_trade_no,omitempty"`
	AmountCents     int64  `json:"amount_cents"`
	Status          string `json:"status"`
	PaidAt          string `json:"paid_at,omitempty"`
	RawPayload      string `json:"raw_payload,omitempty"`
}

type CallbackResponse struct {
	Accepted bool          `json:"accepted"`
	Order    OrderResponse `json:"order,omitempty"`
}

type ProviderCreateOrderRequest struct {
	OrderNo       string
	Subject       string
	AmountCents   int64
	Currency      string
	ExpireAt      string
	ClientPayload map[string]string
}

type ProviderCreateOrderResponse struct {
	Provider        string            `json:"provider"`
	ProviderTradeNo string            `json:"provider_trade_no,omitempty"`
	PayURL          string            `json:"pay_url,omitempty"`
	RawPayload      map[string]string `json:"raw_payload,omitempty"`
}

type Provider interface {
	Name() string
	CreateOrder(req ProviderCreateOrderRequest) (ProviderCreateOrderResponse, error)
	VerifyCallback(payload []byte, signature string) error
}

func orderFromModel(order db.PaymentOrder, clientPayload map[string]string) OrderResponse {
	return OrderResponse{
		OrderNo:         order.OrderNo,
		UserID:          order.UserID,
		Subject:         order.Subject,
		Description:     order.Description,
		AmountCents:     order.AmountCents,
		Currency:        order.Currency,
		Status:          order.Status,
		Provider:        order.Provider,
		ProviderTradeNo: order.ProviderTradeNo,
		PayURL:          order.PayURL,
		ClientPayload:   clientPayload,
		ExpiresAt:       order.ExpiresAt,
		PaidAt:          order.PaidAt,
		CreatedAt:       order.CreatedAt,
		UpdatedAt:       order.UpdatedAt,
	}
}
