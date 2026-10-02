package db

import (
	"context"
	"errors"
	"fmt"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"os"
	"sync"
	"testing"
	"time"
)

func discussionPostgres(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("BETTERFLY_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("requires an isolated BETTERFLY_TEST_POSTGRES_DSN")
	}
	base, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("discussion_%d", time.Now().UnixNano())
	if err := base.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { base.Exec("DROP SCHEMA " + schema + " CASCADE"); sql, _ := base.DB(); sql.Close() })
	database, err := gorm.Open(postgres.Open(dsn+" search_path="+schema), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sql, _ := database.DB()
	sql.SetMaxOpenConns(12)
	t.Cleanup(func() { sql.Close() })
	if err := RunMigrations(database); err != nil {
		t.Fatal(err)
	}
	return database
}

func discussionFixture(t *testing.T, database *gorm.DB) {
	t.Helper()
	for id := int64(1); id <= 4; id++ {
		if err := database.Create(&User{ID: id, Account: fmt.Sprintf("u%d", id), Name: "user"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if _, err := CreateChannelWithDB(database, 1, 9001, "channel", "", "", "", true); err != nil {
		t.Fatal(err)
	}
	if _, _, err := CreateGroupWithOwnerWithDB(database, 1, 9002, "discussion"); err != nil {
		t.Fatal(err)
	}
	if _, err := SetDiscussionGroupWithDB(database, 1, 9001, 9002); err != nil {
		t.Fatal(err)
	}
}

func TestDiscussionPostgresAtomicPublicationHistoryAndRecall(t *testing.T) {
	database := discussionPostgres(t)
	discussionFixture(t, database)
	var post, root *Message
	execute := func(tx *gorm.DB) ([]byte, []PendingOutboxEvent, error) {
		m, created, err := StoreNewMessageWithDB(tx, 1, 9001, "hash", "image", "", true, "post-id", "caption", 0)
		if err != nil {
			return nil, nil, err
		}
		post = m
		if created {
			root, err = CreateDiscussionRootWithDB(tx, post)
			if err != nil {
				return nil, nil, err
			}
		}
		return []byte("ok"), []PendingOutboxEvent{{EventID: "post", Topic: "df", Payload: []byte("post")}, {EventID: "root", Topic: "df", Payload: []byte("root")}}, nil
	}
	for range 2 {
		if _, err := ExecuteInboxOutbox(context.Background(), database, "storage", "publish/0/1", execute); err != nil {
			t.Fatal(err)
		}
	}
	if root == nil || root.Content != "" || root.Caption != "" || root.SourceChannelMessageID != post.MessageID || post.DiscussionRootMessageID != root.MessageID {
		t.Fatal(post, root)
	}
	var count int64
	database.Model(&Message{}).Count(&count)
	if count != 2 {
		t.Fatal("duplicate publication", count)
	}
	database.Model(&OutboxEvent{}).Count(&count)
	if count != 2 {
		t.Fatal("duplicate outbox", count)
	}
	// A new source offset with the same client ID also returns canonical content.
	m, created, err := StoreNewMessageWithDB(database, 1, 9001, "changed", "image", "", true, "post-id", "changed", 0)
	if err != nil || created || m.Caption != "caption" || m.DiscussionRootMessageID != root.MessageID {
		t.Fatal(m, err)
	}
	if err := database.Create(newGroupMember(9002, 2, GroupRoleMember, "2100-01-01T00:00:00Z")).Error; err != nil {
		t.Fatal(err)
	}
	var comment *Message
	if err := database.Transaction(func(tx *gorm.DB) error {
		var err error
		comment, _, err = StoreNewMessageWithDB(tx, 2, 9002, "comment", "text", "", true, "comment-id", "", root.MessageID, root.MessageID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, actor := range []int64{1, 2, 3} {
		allowed, err := CanUserReadMessageWithDB(database, actor, comment)
		if err != nil || allowed != (actor != 3) {
			t.Fatal(actor, allowed, err)
		}
	}
	ordinary := &Message{FromUserID: 1, ToUserID: 9002, IsGroup: true, Timestamp: post.Timestamp, MessageType: "text", Content: "old ordinary chat"}
	if err := database.Create(ordinary).Error; err != nil {
		t.Fatal(err)
	}
	if allowed, err := CanUserReadMessageWithDB(database, 2, ordinary); err != nil || allowed {
		t.Fatal("ordinary history leaked", err)
	}
	page, err := GetSyncMessagesPageWithDB(database, 2, "2000-01-01T00:00:00Z", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[int64]bool{}
	for _, msg := range page.Messages {
		seen[msg.MessageID] = true
	}
	if !seen[root.MessageID] || !seen[comment.MessageID] || seen[ordinary.MessageID] {
		t.Fatal("history scope", seen)
	}
	// A group member does not acquire access to a newly private channel.
	if _, err := UpdateChannelWithDB(database, 1, 9001, map[string]any{}, map[string]any{"is_public": false}); err != nil {
		t.Fatal(err)
	}
	if allowed, err := CanUserReadMessageWithDB(database, 2, comment); err != nil || allowed {
		t.Fatal("private source leaked", err)
	}
	if err := database.Create(newGroupMember(9001, 2, GroupRoleMember, "2100-01-01T00:00:00Z")).Error; err != nil {
		t.Fatal(err)
	}
	if allowed, err := CanUserReadMessageWithDB(database, 2, comment); err != nil || !allowed {
		t.Fatal("channel subscriber denied", err)
	}
	var outcome *MessageRecallOutcome
	if err := database.Transaction(func(tx *gorm.DB) error {
		var err error
		outcome, err = RecallMessageWithDB(tx, 1, post.MessageID, time.Now().Add(time.Hour))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if outcome.DiscussionRoot == nil || !outcome.DiscussionRoot.IsRecalled {
		t.Fatal("root recall not atomic")
	}
	if err := database.Transaction(func(tx *gorm.DB) error {
		_, _, err := StoreNewMessageWithDB(tx, 2, 9002, "closed", "text", "", true, "closed-id", "", 0, root.MessageID)
		return err
	}); !errors.Is(err, ErrInvalidReply) {
		t.Fatal("closed thread accepted comment", err)
	}
	if allowed, err := CanUserReadMessageWithDB(database, 2, comment); err != nil || !allowed {
		t.Fatal("existing comment lost", err)
	}
	if err := database.Where("group_id = ? AND user_id = ?", 9002, 2).Delete(&GroupMember{}).Error; err != nil {
		t.Fatal(err)
	}
	if allowed, err := CanUserReadMessageWithDB(database, 2, comment); err != nil || allowed {
		t.Fatal("departed author bypassed membership", err)
	}
}

func TestDiscussionPostgresFailureRetryAndConcurrentBinding(t *testing.T) {
	database := discussionPostgres(t)
	discussionFixture(t, database)
	injected := errors.New("crash before inbox completion")
	err := database.Transaction(func(tx *gorm.DB) error {
		post, _, err := StoreNewMessageWithDB(tx, 1, 9001, "post", "text", "", true, "retry-id", "", 0)
		if err != nil {
			return err
		}
		if _, err := CreateDiscussionRootWithDB(tx, post); err != nil {
			return err
		}
		return injected
	})
	if !errors.Is(err, injected) {
		t.Fatal(err)
	}
	var count int64
	database.Model(&Message{}).Count(&count)
	if count != 0 {
		t.Fatal("partial post committed", count)
	}
	if _, err := CreateChannelWithDB(database, 1, 9003, "other", "", "", "", true); err != nil {
		t.Fatal(err)
	}
	if _, err := SetDiscussionGroupWithDB(database, 1, 9001, 0); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	start := make(chan struct{})
	for _, id := range []int64{9001, 9003} {
		wg.Add(1)
		go func(id int64) {
			defer wg.Done()
			<-start
			_, err := SetDiscussionGroupWithDB(database, 1, id, 9002)
			results <- err
		}(id)
	}
	close(start)
	wg.Wait()
	close(results)
	ok := 0
	for err := range results {
		if err == nil {
			ok++
		} else if !errors.Is(err, ErrChannelAlreadyExists) {
			t.Fatal(err)
		}
	}
	if ok != 1 {
		t.Fatal("multiple channels bound same group", ok)
	}
	// Concurrent operation replay runs business once and persists one root.
	var channel ChannelSettings
	if err := database.First(&channel, "discussion_group_id = ?", 9002).Error; err != nil {
		t.Fatal(err)
	}
	results = make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := ExecuteInboxOutbox(context.Background(), database, "storage", "same-op", func(tx *gorm.DB) ([]byte, []PendingOutboxEvent, error) {
				post, _, err := StoreNewMessageWithDB(tx, 1, channel.GroupID, "post", "text", "", true, "retry-id", "", 0)
				if err != nil {
					return nil, nil, err
				}
				_, err = CreateDiscussionRootWithDB(tx, post)
				return []byte("ok"), []PendingOutboxEvent{{EventID: "once", Topic: "df", Payload: []byte("event")}}, err
			})
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	database.Model(&Message{}).Count(&count)
	if count != 2 {
		t.Fatal("duplicate roots", count)
	}
	if err := RunMigrations(database); err != nil {
		t.Fatal(err)
	}
}

func TestDiscussionPostgresUpgradeFromV8AndScopeValidation(t *testing.T) {
	database := discussionPostgres(t)
	// Simulate the deployed v8 schema, not a fresh AutoMigrate of new models.
	for _, statement := range []string{
		`DELETE FROM schema_migrations WHERE version = 9`,
		`ALTER TABLE messages DROP COLUMN discussion_root_message_id CASCADE`,
		`ALTER TABLE messages DROP COLUMN source_channel_message_id CASCADE`,
		`ALTER TABLE messages DROP COLUMN discussion_closed CASCADE`,
		`ALTER TABLE channel_settings DROP COLUMN discussion_group_id CASCADE`,
	} {
		if err := database.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := database.Exec(`INSERT INTO messages(from_user_id,to_user_id,content,message_type,timestamp,is_group) VALUES(1,2,'legacy','text','2026-01-01T00:00:00Z',false)`).Error; err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := RunMigrations(database); err != nil {
			t.Fatal(err)
		}
	}
	var legacy Message
	database.First(&legacy, "content = ?", "legacy")
	if legacy.Content != "legacy" || legacy.DiscussionRootMessageID != 0 || legacy.SourceChannelMessageID != 0 {
		t.Fatal(legacy)
	}
	discussionFixture(t, database)
	var post, root *Message
	if err := database.Transaction(func(tx *gorm.DB) error {
		var err error
		post, _, err = StoreNewMessageWithDB(tx, 1, 9001, "post", "text", "", true, "post", "", 0)
		if err != nil {
			return err
		}
		root, err = CreateDiscussionRootWithDB(tx, post)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// Channel administrators retain normal same-channel quotation behavior.
	if _, _, err := StoreNewMessageWithDB(database, 1, 9001, "quote", "text", "", true, "quote", "", post.MessageID); err != nil {
		t.Fatal("linked channel quotation regressed", err)
	}
	for _, tc := range []struct{ actor, group, reply int64 }{{3, 9002, 0}, {1, 9999, 0}, {1, 9002, post.MessageID}} {
		err := database.Transaction(func(tx *gorm.DB) error {
			_, _, err := StoreNewMessageWithDB(tx, tc.actor, tc.group, "bad", "text", "", true, "bad", "", tc.reply, root.MessageID)
			return err
		})
		if !errors.Is(err, ErrInvalidReply) {
			t.Fatal("cross-thread or nonmember comment accepted", tc, err)
		}
	}
	for _, groupID := range []int64{0, 9002} {
		if _, err := SetDiscussionGroupWithDB(database, 1, 9001, groupID); err != nil {
			t.Fatal(err)
		}
	}
	view, err := GetDiscussionWithDB(database, 1, root.MessageID)
	if err != nil || !view.Closed || !view.Root.DiscussionClosed {
		t.Fatal("relink reopened an old discussion", view, err)
	}
	if err := database.Transaction(func(tx *gorm.DB) error {
		_, _, err := StoreNewMessageWithDB(tx, 1, 9002, "closed", "text", "", true, "closed", "", 0, root.MessageID)
		return err
	}); !errors.Is(err, ErrInvalidReply) {
		t.Fatal("relinked old thread accepted comment", err)
	}
}

func TestDiscussionPostgresOwnerBindingAndModeration(t *testing.T) {
	database := discussionPostgres(t)
	discussionFixture(t, database)
	for _, id := range []int64{9001, 9002} {
		if err := database.Create(newGroupMember(id, 3, GroupRoleAdmin, "2026-01-01T00:00:00Z")).Error; err != nil {
			t.Fatal(err)
		}
	}
	if _, err := SetDiscussionGroupWithDB(database, 3, 9001, 9002); !errors.Is(err, ErrChannelForbidden) {
		t.Fatal("admin linked group", err)
	}
	if _, _, err := CreateGroupWithOwnerWithDB(database, 2, 9004, "another owner's group"); err != nil {
		t.Fatal(err)
	}
	if _, err := SetDiscussionGroupWithDB(database, 1, 9001, 9004); !errors.Is(err, ErrChannelForbidden) {
		t.Fatal("linked another owner's group", err)
	}
	if err := database.Create(newGroupMember(9002, 2, GroupRoleMember, "2026-01-01T00:00:00Z")).Error; err != nil {
		t.Fatal(err)
	}
	var source, root, comment *Message
	if err := database.Transaction(func(tx *gorm.DB) error {
		var err error
		source, _, err = StoreNewMessageWithDB(tx, 1, 9001, "announcement", "text", "", true, "source", "", 0)
		if err != nil {
			return err
		}
		root, err = CreateDiscussionRootWithDB(tx, source)
		if err != nil {
			return err
		}
		comment, _, err = StoreNewMessageWithDB(tx, 2, 9002, "comment", "text", "", true, "comment", "", 0, root.MessageID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	recall := func(actor, id int64) *MessageRecallOutcome {
		t.Helper()
		var outcome *MessageRecallOutcome
		if err := database.Transaction(func(tx *gorm.DB) error {
			var err error
			outcome, err = RecallMessageWithDB(tx, actor, id, time.Now().Add(time.Hour))
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return outcome
	}
	if r := recall(2, root.MessageID); r.Status != MessageRecallForbidden {
		t.Fatal("member recalled generated card", r)
	}
	if r := recall(2, comment.MessageID); r.Status != MessageRecallExpired {
		t.Fatal("author recall window changed", r)
	}
	if r := recall(3, comment.MessageID); r.Status != MessageRecallOK {
		t.Fatal("discussion admin could not moderate", r)
	}
	if r := recall(3, root.MessageID); r.Status != MessageRecallOK {
		t.Fatal("discussion admin could not close card", r)
	}
	view, err := GetDiscussionWithDB(database, 2, root.MessageID)
	if err != nil || !view.Closed || view.Post.IsRecalled || view.ReplyCount != 0 {
		t.Fatal("card moderation changed original channel post", view, err)
	}
	if err := DeleteChannelWithDB(database, 1, 9001); err != nil {
		t.Fatal(err)
	}
	if _, err := GetDiscussionWithDB(database, 2, root.MessageID); !errors.Is(err, ErrChannelNotFound) {
		t.Fatal("deleted channel thread still readable", err)
	}
	if _, err := CreateChannelWithDB(database, 1, 9005, "replacement", "", "", "", true); err != nil {
		t.Fatal(err)
	}
	if _, err := SetDiscussionGroupWithDB(database, 1, 9005, 9002); err != nil {
		t.Fatal("deleted channel retained unique discussion binding", err)
	}
}
