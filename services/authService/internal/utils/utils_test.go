package utils

import (
	"Betterfly2/shared/db"
	"strings"
	"testing"
	"time"

	goJwt "github.com/golang-jwt/jwt/v5"
)

func TestGenerateAndValidateJWTRoundTrip(t *testing.T) {
	user := &db.User{ID: 42, Account: "alice", JwtKey: []byte("test-secret-with-enough-entropy")}
	token, err := GenerateJWT(user)
	if err != nil {
		t.Fatal(err)
	}

	claims, err := ValidateJWT(token, user.JwtKey)
	if err != nil {
		t.Fatal(err)
	}
	if claims.ID != user.ID || claims.Account != user.Account {
		t.Fatalf("unexpected claims: %+v", claims)
	}
	if claims.ExpiresAt == nil || claims.IssuedAt == nil || claims.NotBefore == nil {
		t.Fatalf("registered time claims are required: %+v", claims.RegisteredClaims)
	}
	remaining := time.Until(claims.ExpiresAt.Time)
	if remaining < 29*24*time.Hour || remaining > 30*24*time.Hour+time.Minute {
		t.Fatalf("unexpected token lifetime: %v", remaining)
	}
}

func TestValidateJWTRejectsWrongKeyExpiredAndMissingExpiration(t *testing.T) {
	key := []byte("correct-secret")
	tests := []struct {
		name  string
		token func() string
		key   []byte
	}{
		{
			name: "wrong key",
			token: func() string {
				token, _ := GenerateJWT(&db.User{ID: 1, Account: "alice", JwtKey: key})
				return token
			},
			key: []byte("wrong-secret"),
		},
		{
			name: "expired",
			token: func() string {
				return signedTestToken(t, key, goJwt.RegisteredClaims{ExpiresAt: goJwt.NewNumericDate(time.Now().Add(-time.Minute))})
			},
			key: key,
		},
		{
			name: "missing expiration",
			token: func() string {
				return signedTestToken(t, key, goJwt.RegisteredClaims{})
			},
			key: key,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ValidateJWT(tt.token(), tt.key); err == nil || !strings.Contains(err.Error(), "jwt parse error") {
				t.Fatalf("expected parse rejection, got %v", err)
			}
		})
	}
}

func TestValidateJWTRejectsUnexpectedSigningAlgorithm(t *testing.T) {
	claims := BetterflyClaims{
		ID:      1,
		Account: "alice",
		RegisteredClaims: goJwt.RegisteredClaims{
			ExpiresAt: goJwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	}
	token, err := goJwt.NewWithClaims(goJwt.SigningMethodHS384, claims).SignedString([]byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateJWT(token, []byte("secret")); err == nil {
		t.Fatal("expected non-HS256 token to be rejected")
	}
}

func TestValidateJWTAllowsOnlySmallClockSkew(t *testing.T) {
	key := []byte("clock-skew-secret")
	withinLeeway := signedTestToken(t, key, goJwt.RegisteredClaims{
		ExpiresAt: goJwt.NewNumericDate(time.Now().Add(time.Hour)),
		NotBefore: goJwt.NewNumericDate(time.Now().Add(15 * time.Second)),
	})
	if _, err := ValidateJWT(withinLeeway, key); err != nil {
		t.Fatalf("token within clock skew was rejected: %v", err)
	}

	beyondLeeway := signedTestToken(t, key, goJwt.RegisteredClaims{
		ExpiresAt: goJwt.NewNumericDate(time.Now().Add(time.Hour)),
		NotBefore: goJwt.NewNumericDate(time.Now().Add(2 * time.Minute)),
	})
	if _, err := ValidateJWT(beyondLeeway, key); err == nil {
		t.Fatal("token beyond clock skew was accepted")
	}
}

func signedTestToken(t *testing.T, key []byte, registered goJwt.RegisteredClaims) string {
	t.Helper()
	token, err := goJwt.NewWithClaims(goJwt.SigningMethodHS256, BetterflyClaims{
		ID: 1, Account: "alice", RegisteredClaims: registered,
	}).SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return token
}
