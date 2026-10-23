package push

import (
	pushpb "Betterfly2/proto/push"
	"Betterfly2/shared/db"
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func tokenRevocationPostgres(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("BETTERFLY_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("requires isolated BETTERFLY_TEST_POSTGRES_DSN")
	}
	base, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("push_revoke_%d", time.Now().UnixNano())
	if err := base.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { base.Exec("DROP SCHEMA " + schema + " CASCADE"); sql, _ := base.DB(); sql.Close() })
	database, err := gorm.Open(postgres.Open(dsn+" search_path="+schema), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sql, _ := database.DB(); sql.Close() })
	if err := db.RunMigrations(database); err != nil {
		t.Fatal(err)
	}
	return database
}

func revokeTokensRequest(userID int64, revokedBefore string) *pushpb.RequestMessage {
	return &pushpb.RequestMessage{Payload: &pushpb.RequestMessage_RevokeUserTokens{RevokeUserTokens: &pushpb.RevokeUserTokens{
		UserId: userID, RevokedBefore: revokedBefore, Reason: "revoke_sessions",
	}}}
}

func TestRevokeUserTokensPostgresKeepsLaterRegistrations(t *testing.T) {
	database := tokenRevocationPostgres(t)
	cutoff := time.Date(2026, 10, 23, 8, 0, 0, 0, time.UTC)
	tokens := []db.PushDeviceToken{
		{UserID: 1, DeviceID: "old-phone", Token: "a1", PushType: PushTypeAPNs, UpdatedAt: db.FormatReliabilityTime(cutoff.Add(-time.Hour))},
		{UserID: 1, DeviceID: "old-phone", Token: "v1", PushType: PushTypeVoIP, UpdatedAt: db.FormatReliabilityTime(cutoff.Add(-time.Microsecond))},
		{UserID: 1, DeviceID: "current-phone", Token: "a2", PushType: PushTypeAPNs, UpdatedAt: db.FormatReliabilityTime(cutoff)},
		{UserID: 1, DeviceID: "current-phone", Token: "v2", PushType: PushTypeVoIP, UpdatedAt: db.FormatReliabilityTime(cutoff.Add(time.Second))},
		{UserID: 2, DeviceID: "other-user", Token: "a3", PushType: PushTypeAPNs, UpdatedAt: db.FormatReliabilityTime(cutoff.Add(-time.Hour))},
	}
	for _, token := range tokens {
		token.Environment, token.IsActive, token.CreatedAt = "sandbox", true, token.UpdatedAt
		if err := database.Create(&token).Error; err != nil {
			t.Fatal(err)
		}
	}
	store := &GormStore{db: database}
	request := revokeTokensRequest(1, db.FormatReliabilityTime(cutoff))
	// 同一条Kafka消息重放时按操作键去重，删除只执行一次也不报错。
	for i := 0; i < 2; i++ {
		if err := store.EnqueueRequest(context.Background(), "push-requests/0/1", request, "bundle"); err != nil {
			t.Fatal(err)
		}
	}
	var remaining []string
	if err := database.Model(&db.PushDeviceToken{}).Order("token").Pluck("token", &remaining).Error; err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(remaining) != "[a2 a3 v2]" {
		t.Fatalf("remaining tokens = %v", remaining)
	}
}

func TestRevokeUserTokensRejectsMalformedRequest(t *testing.T) {
	for _, request := range []*pushpb.RequestMessage{
		revokeTokensRequest(0, db.FormatReliabilityTime(time.Now())),
		revokeTokensRequest(1, ""),
		revokeTokensRequest(1, "2026-10-23T08:00:00Z"),
	} {
		if err := revokeUserTokensWithDB(nil, request.GetRevokeUserTokens()); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("%v: err=%v", request, err)
		}
	}
}
