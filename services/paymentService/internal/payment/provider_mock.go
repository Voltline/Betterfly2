package payment

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"strings"
)

type MockProvider struct {
	baseURL string
	secret  string
}

func NewMockProviderFromEnv() *MockProvider {
	baseURL := strings.TrimRight(os.Getenv("PAYMENT_PUBLIC_BASE_URL"), "/")
	if baseURL == "" {
		baseURL = "http://localhost:8084"
	}
	secret := os.Getenv("PAYMENT_MOCK_CALLBACK_SECRET")
	if secret == "" {
		secret = "dev-payment-secret"
	}
	return &MockProvider{baseURL: baseURL, secret: secret}
}

func (p *MockProvider) Name() string {
	return DefaultProvider
}

func (p *MockProvider) CreateOrder(req ProviderCreateOrderRequest) (ProviderCreateOrderResponse, error) {
	return ProviderCreateOrderResponse{
		Provider: DefaultProvider,
		PayURL:   p.baseURL + "/payment/v1/mock/pay/" + req.OrderNo,
		RawPayload: map[string]string{
			"mode":     "mock",
			"order_no": req.OrderNo,
		},
	}, nil
}

func (p *MockProvider) VerifyCallback(payload []byte, signature string) error {
	signature = strings.TrimSpace(signature)
	if signature == "" {
		return errors.New("missing payment callback signature")
	}
	expected := p.Sign(payload)
	if !hmac.Equal([]byte(signature), []byte(expected)) {
		return errors.New("invalid payment callback signature")
	}
	return nil
}

func (p *MockProvider) Sign(payload []byte) string {
	mac := hmac.New(sha256.New, []byte(p.secret))
	_, _ = mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}
