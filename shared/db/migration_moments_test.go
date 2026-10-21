package db

import (
	"errors"
	"sync"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"gorm.io/gorm"
)

func TestMomentsMigrationIsExplicitAdditiveAndRetryable(t *testing.T) {
	plan := migrationPlan()
	versions := []int{1, 2, 3, 4, 5, 6, 7, 8, 9}
	pending, err := pendingMigrations(plan, versions)
	if err != nil || len(pending) != 2 || pending[0].Version != 10 {
		t.Fatal(pending, err)
	}
	if pending, err := pendingMigrations(plan, append(versions, 10)); err != nil || len(pending) != 1 || pending[0].Version != 11 {
		t.Fatal(pending, err)
	}
	database, mock := newInboxDatabase(t)
	failure := errors.New("DDL interrupted")
	mock.ExpectExec(`CREATE TABLE IF NOT EXISTS moment_posts`).WillReturnError(failure)
	if err := migrateMomentsSchema(database); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	for range 2 {
		for _, statement := range []string{"CREATE TABLE IF NOT EXISTS moment_posts", "CREATE INDEX IF NOT EXISTS idx_moment_posts_author_page", "CREATE INDEX IF NOT EXISTS idx_moment_posts_page", "CREATE TABLE IF NOT EXISTS moment_assets", "CREATE TABLE IF NOT EXISTS moment_comments", "CREATE INDEX IF NOT EXISTS idx_moment_comments_page", "CREATE TABLE IF NOT EXISTS moment_likes", "CREATE INDEX IF NOT EXISTS idx_moment_likes_page"} {
			mock.ExpectExec(statement).WillReturnResult(sqlmock.NewResult(0, 0))
		}
		if err := migrateMomentsSchema(database); err != nil {
			t.Fatal(err)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMomentsPostgresV9UpgradeFailureRetryAndConcurrentRunners(t *testing.T) {
	database := discussionPostgres(t)
	legacy := Message{FromUserID: 1, ToUserID: 2, Content: "legacy preserved", MessageType: "text", Timestamp: "2026-10-04T01:00:00Z"}
	if err := database.Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"file_access_grants", "moment_likes", "moment_comments", "moment_assets", "moment_posts"} {
		if err := database.Exec("DROP TABLE " + table).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := database.Exec("DELETE FROM schema_migrations WHERE version >= 10").Error; err != nil {
		t.Fatal(err)
	}
	plan := migrationPlan()
	plan[9].Apply = func(tx *gorm.DB) error {
		if err := tx.Exec("CREATE TABLE moment_posts(id bigint PRIMARY KEY)").Error; err != nil {
			return err
		}
		return errors.New("injected DDL interruption")
	}
	if err := runMigrationPlan(database, plan); err == nil {
		t.Fatal("interrupted migration accepted")
	}
	if database.Migrator().HasTable(&MomentPost{}) {
		t.Fatal("failed DDL did not roll back")
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); results <- RunMigrations(database) }()
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := RunMigrations(database); err != nil {
		t.Fatal(err)
	}
	var count int64
	database.Model(&SchemaMigration{}).Where("version = 10").Count(&count)
	if count != 1 {
		t.Fatal("migration ledger duplicate", count)
	}
	for _, model := range []any{&MomentPost{}, &MomentAsset{}, &MomentComment{}, &MomentLike{}} {
		if !database.Migrator().HasTable(model) {
			t.Fatal("missing table", model)
		}
	}
	var after Message
	if err := database.First(&after, "message_id = ?", legacy.MessageID).Error; err != nil || after.Content != legacy.Content || after.Timestamp != legacy.Timestamp {
		t.Fatal("legacy rewritten", after, err)
	}
}
