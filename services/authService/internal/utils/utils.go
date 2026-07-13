package utils

import (
	"Betterfly2/shared/db"
	"errors"
	"fmt"
	goJwt "github.com/golang-jwt/jwt/v5"
	"time"
)

const (
	jwtExp       = time.Hour * 24 * 30 // 有效期：30天
	jwtClockSkew = 30 * time.Second
)

type BetterflyClaims struct {
	ID      int64  `json:"id"`
	Account string `json:"account"`
	goJwt.RegisteredClaims
}

func GenerateJWT(user *db.User) (string, error) {
	now := time.Now().UTC()
	claims := BetterflyClaims{
		ID:      user.ID,
		Account: user.Account,
		RegisteredClaims: goJwt.RegisteredClaims{
			ExpiresAt: goJwt.NewNumericDate(now.Add(jwtExp)),
			IssuedAt:  goJwt.NewNumericDate(now),
			NotBefore: goJwt.NewNumericDate(now),
		},
	}
	return goJwt.NewWithClaims(goJwt.SigningMethodHS256, claims).SignedString(user.JwtKey)
}

func ValidateJWT(jwtStr string, jwtKey []byte) (*BetterflyClaims, error) {
	var parser = goJwt.NewParser(
		goJwt.WithValidMethods([]string{"HS256"}), // 明确指定签名算法
		goJwt.WithExpirationRequired(),            // 必须含有 exp 字段
		goJwt.WithLeeway(jwtClockSkew),            // 容忍多实例间轻微时钟偏差
	)

	jwt, err := parser.ParseWithClaims(jwtStr, &BetterflyClaims{}, func(_ *goJwt.Token) (interface{}, error) {
		return jwtKey, nil
	})

	if err != nil {
		return nil, fmt.Errorf("jwt parse error: %w", err)
	}

	claims, ok := jwt.Claims.(*BetterflyClaims)
	if !ok || !jwt.Valid {
		return nil, errors.New("invalid jwt claims")
	}

	return claims, nil
}
