package push

import (
	"Betterfly2/shared/db"
	"context"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"os"
	"strings"
	"testing"
	"time"
)

func TestChannelPushEligibilityPostgres(t *testing.T) {
	if os.Getenv("BETTERFLY_ACCEPTANCE") != "1" || os.Getenv("PGSQL_DSN") == "" {
		t.Skip("requires explicit acceptance opt-in and PostgreSQL DSN")
	}
	t.Setenv("PGSQL_DSN", strings.Trim(strings.TrimSpace(os.Getenv("PGSQL_DSN")), "\"'"))
	database, err := db.Open()
	if err != nil {
		t.Fatal("cannot connect to acceptance PostgreSQL; DSN omitted")
	}
	sqlDB, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rollback := errors.New("rollback invisible push fixture")
	err = database.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// All rows remain uncommitted: the running Push worker cannot see or send them.
		users := []db.User{{Account: fmt.Sprintf("pushch_a_%d", time.Now().UnixNano())}, {Account: fmt.Sprintf("pushch_b_%d", time.Now().UnixNano())}, {Account: fmt.Sprintf("pushch_c_%d", time.Now().UnixNano())}}
		if err := tx.Create(&users).Error; err != nil {
			return err
		}
		id := time.Now().UnixMilli()*1000 + 7
		if _, err := db.CreateChannelWithDB(tx, users[0].ID, id, "private push test", "", "", "", false); err != nil {
			return err
		}
		if err := tx.Create(&db.GroupMember{GroupID: id, UserID: users[2].ID, Role: db.GroupRoleMember, JoinedAt: "2000-01-01T00:00:00Z", NotificationsMuted: true}).Error; err != nil {
			return err
		}
		message := db.Message{FromUserID: users[0].ID, ToUserID: id, IsGroup: true, Content: "test", Timestamp: "2000-01-01T00:00:00Z", MessageType: "text"}
		if err := tx.Create(&message).Error; err != nil {
			return err
		}
		tokens := []db.PushDeviceToken{
			{UserID: users[0].ID, DeviceID: "channel-pg-a", Token: fmt.Sprintf("test_a_%d", time.Now().UnixNano()), Environment: "sandbox", PushType: PushTypeAPNs, IsActive: true},
			{UserID: users[1].ID, DeviceID: "channel-pg-b", Token: fmt.Sprintf("test_b_%d", time.Now().UnixNano()), Environment: "sandbox", PushType: PushTypeAPNs, IsActive: true},
			{UserID: users[2].ID, DeviceID: "channel-pg-c", Token: fmt.Sprintf("test_c_%d", time.Now().UnixNano()), Environment: "sandbox", PushType: PushTypeAPNs, IsActive: true},
		}
		if err := tx.Create(&tokens).Error; err != nil {
			return err
		}
		now := db.FormatReliabilityTime(time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC))
		job := db.PushJob{JobID: fmt.Sprintf("channel-fixture-%d", id), OperationKey: fmt.Sprintf("channel-fixture-%d", id), Kind: "message", Status: PushJobPending, CreatedAt: now, UpdatedAt: now}
		if err := tx.Create(&job).Error; err != nil {
			return err
		}
		fanout := tx.Exec(messageFanoutSQL, fmt.Sprintf("[%d,%d,%d]", users[0].ID, users[1].ID, users[2].ID), users[0].ID, id, PushTypeAPNs, true, message.MessageID, job.JobID, DeliveryPending, now, now, now)
		if fanout.Error != nil {
			return fanout.Error
		}
		if fanout.RowsAffected != 1 {
			return fmt.Errorf("muted/nonmember enqueue eligibility rows=%d want1", fanout.RowsAffected)
		}
		if err := tx.Where("job_id = ?", job.JobID).Delete(&db.PushMessageDelivery{}).Error; err != nil {
			return err
		}
		var rows []db.PushMessageDelivery
		for _, token := range tokens {
			rows = append(rows, db.PushMessageDelivery{MessageID: message.MessageID, TokenID: token.ID, JobID: job.JobID, Status: DeliveryPending, CreatedAt: now, UpdatedAt: now})
		}
		rows = append(rows, db.PushMessageDelivery{MessageID: -message.MessageID, TokenID: tokens[1].ID, JobID: job.JobID, Status: DeliveryPending, CreatedAt: now, UpdatedAt: now})
		rows = append(rows, db.PushMessageDelivery{MessageID: -message.MessageID, TokenID: tokens[2].ID, JobID: job.JobID, Status: DeliveryPending, CreatedAt: now, UpdatedAt: now})
		if err := tx.Create(&rows).Error; err != nil {
			return err
		}
		var claims []durableClaimRow
		err := tx.Raw(claimMessageDeliverySQL, PushJobPending, 10, DeliveryPending, DeliveryRetryable, now, DeliveryClaimed, now, 5, DeliveryClaimed, "fixture-claim", now, now).Scan(&claims).Error
		if err != nil {
			return err
		}
		if len(claims) != 5 {
			return fmt.Errorf("claims=%d want5", len(claims))
		}
		for _, claim := range claims {
			wantExcluded := claim.UserID == users[1].ID || claim.UserID == users[2].ID && claim.MessageID > 0
			if !claim.IsChannel || claim.RecipientExcluded != wantExcluded {
				return fmt.Errorf("channel subscription projection incorrect for token_id=%d", claim.TokenID)
			}
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatalf("PostgreSQL channel eligibility: %v", err)
	}
	t.Log("PASS actual PostgreSQL: muted/nonmember excluded at enqueue and claim; muted current subscriber still receives recall replacements; entire fixture rolled back")
}
