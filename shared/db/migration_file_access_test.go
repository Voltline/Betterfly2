package db

import (
	"errors"
	"sync"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestFileAccessMigrationIsExplicitAdditiveAndRetryable(t *testing.T) {
	plan := migrationPlan()
	versions := []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	pending, err := pendingMigrations(plan, versions)
	if err != nil || len(pending) != 1 || pending[0].Version != 11 {
		t.Fatal(pending, err)
	}
	if pending, err := pendingMigrations(plan, append(versions, 11)); err != nil || len(pending) != 0 {
		t.Fatal(pending, err)
	}
	database, mock := newInboxDatabase(t)
	failure := errors.New("DDL interrupted")
	mock.ExpectExec(`CREATE TABLE IF NOT EXISTS file_access_grants`).WillReturnError(failure)
	if err := migrateFileAccessSchema(database); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	for range 2 {
		for _, statement := range []string{"CREATE TABLE IF NOT EXISTS file_access_grants", "CREATE INDEX IF NOT EXISTS idx_file_access_grants_user"} {
			mock.ExpectExec(statement).WillReturnResult(sqlmock.NewResult(0, 0))
		}
		if err := migrateFileAccessSchema(database); err != nil {
			t.Fatal(err)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestFileAccessGrantsPostgres(t *testing.T) {
	database := discussionPostgres(t)
	const fileHash = "abc123"

	if granted, err := HasFileAccessWithDB(database, fileHash, 1); err != nil || granted {
		t.Fatalf("unexpected grant before upload: granted=%v err=%v", granted, err)
	}
	if err := StoreVerifiedFileWithDB(database, fileHash, 7, "ab/"+fileHash, 1); err != nil {
		t.Fatal(err)
	}
	metadata, err := GetFileMetadataWithDB(database, fileHash)
	if err != nil || metadata == nil || !metadata.IsVerified || metadata.FileSize != 7 {
		t.Fatalf("verified metadata was not stored: %+v err=%v", metadata, err)
	}
	if granted, err := HasFileAccessWithDB(database, fileHash, 1); err != nil || !granted {
		t.Fatalf("uploader was not granted: granted=%v err=%v", granted, err)
	}
	if granted, err := HasFileAccessWithDB(database, fileHash, 2); err != nil || granted {
		t.Fatalf("another user was granted: granted=%v err=%v", granted, err)
	}

	// 重复校验与重复授权都是幂等的。
	if err := StoreVerifiedFileWithDB(database, fileHash, 7, "ab/"+fileHash, 1); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := GrantFileAccessWithDB(database, fileHash, 2); err != nil {
			t.Fatal(err)
		}
	}
	var count int64
	if err := database.Model(&FileAccessGrant{}).Where("file_hash = ?", fileHash).Count(&count).Error; err != nil || count != 2 {
		t.Fatalf("expected two grants, got %d err=%v", count, err)
	}

	// 授权写入失败时元数据也必须回滚，不能留下无主的已校验文件。
	if err := database.Exec("ALTER TABLE file_access_grants ADD CONSTRAINT reject_user_3 CHECK (user_id <> 3)").Error; err != nil {
		t.Fatal(err)
	}
	if err := StoreVerifiedFileWithDB(database, "def456", 9, "de/def456", 3); err == nil {
		t.Fatal("expected grant failure")
	}
	if metadata, err := GetFileMetadataWithDB(database, "def456"); err != nil || metadata != nil {
		t.Fatalf("metadata survived a failed grant: %+v err=%v", metadata, err)
	}
}

func TestFileAccessPostgresV10UpgradeWithConcurrentRunners(t *testing.T) {
	database := discussionPostgres(t)
	if err := database.Exec("DROP TABLE file_access_grants").Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Exec("DELETE FROM schema_migrations WHERE version = 11").Error; err != nil {
		t.Fatal(err)
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
	var count int64
	database.Model(&SchemaMigration{}).Where("version = 11").Count(&count)
	if count != 1 || !database.Migrator().HasTable(&FileAccessGrant{}) {
		t.Fatalf("v11 upgrade incomplete: ledger=%d", count)
	}
	if err := CheckSchemaVersion(database); err != nil {
		t.Fatal(err)
	}
}
