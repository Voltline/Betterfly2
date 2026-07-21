package db

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Migration struct {
	Version int
	Name    string
	Apply   func(*gorm.DB) error
}

var nonPostgresMigrationLock sync.Mutex

// Releases through schema v3 used one all-model AutoMigrate and recorded only
// the current version. A v3 marker therefore attests that v1-v3 all succeeded,
// even when the older ledger does not contain the preceding rows.
const legacySnapshotMigrationVersion = 3

func migrationPlan() []Migration {
	// Versions 1-5 are published history. Do not add newly introduced models to
	// these functions; the next schema change must be an explicit new version.
	return []Migration{
		{Version: 1, Name: "core schema", Apply: migrateCoreSchema},
		{Version: 2, Name: "experiments push and idempotency schema", Apply: migrateServiceSchema},
		{Version: 3, Name: "legacy compatibility and query indexes", Apply: migrateLegacyCompatibility},
		{Version: 4, Name: "transactional inbox outbox and durable push", Apply: migrateReliabilitySchema},
		{Version: 5, Name: "message recall state", Apply: migrateMessageRecallSchema},
	}
}

// RunMigrations owns one database session for the whole migration run. The
// PostgreSQL advisory lock therefore fences every DDL statement and version
// record, not just the final schema_migrations insert.
func RunMigrations(database *gorm.DB) error {
	if database == nil {
		return fmt.Errorf("migration database is nil")
	}
	if database.Dialector.Name() != "postgres" {
		nonPostgresMigrationLock.Lock()
		defer nonPostgresMigrationLock.Unlock()
		return runMigrationPlan(database, migrationPlan())
	}
	return database.Connection(func(connection *gorm.DB) error {
		if err := connection.Exec(`SELECT pg_advisory_lock(hashtext('betterfly2_schema_migration'))`).Error; err != nil {
			return fmt.Errorf("acquire schema migration lock: %w", err)
		}
		defer connection.Exec(`SELECT pg_advisory_unlock(hashtext('betterfly2_schema_migration'))`)
		return runMigrationPlan(connection, migrationPlan())
	})
}

func runMigrationPlan(database *gorm.DB, plan []Migration) error {
	if err := migrateModelsAdditive(database, &SchemaMigration{}); err != nil {
		return fmt.Errorf("create schema migration ledger: %w", err)
	}
	var applied []int
	if err := migrationSession(database).Model(&SchemaMigration{}).Order("version ASC").Pluck("version", &applied).Error; err != nil {
		return fmt.Errorf("load applied migrations: %w", err)
	}
	var err error
	applied, err = normalizeLegacyMigrationLedger(database, applied)
	if err != nil {
		return err
	}
	pending, err := pendingMigrations(plan, applied)
	if err != nil {
		return err
	}
	for _, migration := range pending {
		migrationRunner := migrationSession(database)
		if err := migrationRunner.Transaction(func(tx *gorm.DB) error {
			cleanTx := migrationSession(tx)
			if err := migration.Apply(cleanTx); err != nil {
				return fmt.Errorf("apply migration %d (%s): %w", migration.Version, migration.Name, err)
			}
			return cleanTx.Clauses(clause.OnConflict{DoNothing: true}).Create(&SchemaMigration{
				Version: migration.Version, AppliedAt: time.Now().UTC().Format(time.RFC3339Nano),
			}).Error
		}); err != nil {
			return err
		}
	}
	return nil
}

func normalizeLegacyMigrationLedger(database *gorm.DB, applied []int) ([]int, error) {
	missing, normalized := legacyMigrationBackfill(applied)
	if len(missing) == 0 {
		return normalized, nil
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	err := migrationSession(database).Transaction(func(tx *gorm.DB) error {
		cleanTx := migrationSession(tx)
		for _, version := range missing {
			if err := cleanTx.Clauses(clause.OnConflict{DoNothing: true}).Create(&SchemaMigration{
				Version: version, AppliedAt: now,
			}).Error; err != nil {
				return fmt.Errorf("backfill legacy migration %d: %w", version, err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return normalized, nil
}

func legacyMigrationBackfill(applied []int) ([]int, []int) {
	appliedSet := make(map[int]struct{}, len(applied))
	maxVersion := 0
	for _, version := range applied {
		appliedSet[version] = struct{}{}
		if version > maxVersion {
			maxVersion = version
		}
	}
	if maxVersion <= 0 || maxVersion > legacySnapshotMigrationVersion {
		return nil, append([]int(nil), applied...)
	}
	missing := make([]int, 0, maxVersion)
	for version := 1; version <= maxVersion; version++ {
		if _, exists := appliedSet[version]; exists {
			continue
		}
		missing = append(missing, version)
		appliedSet[version] = struct{}{}
	}
	normalized := make([]int, 0, len(appliedSet))
	for version := range appliedSet {
		normalized = append(normalized, version)
	}
	sort.Ints(normalized)
	return missing, normalized
}

func pendingMigrations(plan []Migration, applied []int) ([]Migration, error) {
	ordered := append([]Migration(nil), plan...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Version < ordered[j].Version })
	for index, migration := range ordered {
		expected := index + 1
		if migration.Version != expected {
			return nil, fmt.Errorf("migration plan gap: expected version %d, got %d", expected, migration.Version)
		}
		if migration.Apply == nil {
			return nil, fmt.Errorf("migration %d has no apply function", migration.Version)
		}
	}
	appliedSet := make(map[int]struct{}, len(applied))
	for _, version := range applied {
		if version <= 0 || version > len(ordered) {
			return nil, fmt.Errorf("applied migration version %d is outside this migration plan", version)
		}
		appliedSet[version] = struct{}{}
	}
	for version := 1; version <= len(appliedSet); version++ {
		if _, ok := appliedSet[version]; !ok {
			return nil, fmt.Errorf("applied migration ledger has a gap at version %d", version)
		}
	}
	pending := make([]Migration, 0, len(ordered)-len(appliedSet))
	for _, migration := range ordered {
		if _, ok := appliedSet[migration.Version]; !ok {
			pending = append(pending, migration)
		}
	}
	return pending, nil
}

func migrateCoreSchema(tx *gorm.DB) error {
	return migrateModelsAdditive(tx,
		&User{}, &Friend{}, &Group{}, &GroupMember{}, &RelationshipRequest{},
		&Message{}, &FileMetadata{},
	)
}

func migrateServiceSchema(tx *gorm.DB) error {
	return migrateModelsAdditive(tx,
		&ConsumerOperationResult{},
		&ABExperiment{}, &ABExperimentGroup{}, &ABExperimentOverride{},
		&PushDeviceToken{}, &PushMessageDelivery{}, &PushDebugAudit{},
	)
}

func migrateLegacyCompatibility(tx *gorm.DB) error {
	if tx.Dialector.Name() == "postgres" {
		if err := migratePostgresSchemaStatements(tx); err != nil {
			return err
		}
	}
	if err := BackfillGroupMemberJoinedAtWithDB(tx); err != nil {
		return err
	}
	return tx.Model(&PushMessageDelivery{}).
		Where("status IS NULL OR status = ''").
		Updates(map[string]any{"status": DeliveryStatusLegacySent, "updated_at": gorm.Expr("created_at")}).Error
}

const DeliveryStatusLegacySent = "sent"

func migrateReliabilitySchema(tx *gorm.DB) error {
	if err := migrateModelsAdditive(tx,
		&ConsumerInbox{}, &OutboxEvent{},
		&PushJob{}, &PushMessageDelivery{}, &PushVoIPDelivery{},
		&ABExperimentOverride{},
	); err != nil {
		return err
	}
	if tx.Dialector.Name() == "postgres" {
		if err := tx.Exec(`
DO $$
BEGIN
  IF to_regclass('public.push_message_deliveries') IS NOT NULL THEN
    UPDATE push_message_deliveries SET
      lease_until = CASE WHEN NULLIF(lease_until, '') IS NULL THEN '' ELSE to_char(lease_until::timestamptz AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"') END,
      next_retry_at = CASE WHEN NULLIF(next_retry_at, '') IS NULL THEN '' ELSE to_char(next_retry_at::timestamptz AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"') END,
      created_at = CASE WHEN NULLIF(created_at, '') IS NULL THEN '' ELSE to_char(created_at::timestamptz AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"') END,
      updated_at = CASE WHEN NULLIF(updated_at, '') IS NULL THEN '' ELSE to_char(updated_at::timestamptz AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"') END;
  END IF;
  IF to_regclass('public.consumer_operation_results') IS NOT NULL THEN
    UPDATE consumer_operation_results SET
      created_at = CASE WHEN NULLIF(created_at, '') IS NULL THEN '' ELSE to_char(created_at::timestamptz AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"') END;
  END IF;
END $$`).Error; err != nil {
			return err
		}
		if err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_ab_overrides_subject_experiment ON ab_experiment_overrides (subject_type, subject_id, experiment_id)`).Error; err != nil {
			return err
		}
	}
	return nil
}

func migrateMessageRecallSchema(tx *gorm.DB) error {
	return migrateModelsAdditive(tx, &Message{})
}

type additiveSchemaMigrator interface {
	HasTable(dst interface{}) bool
	CreateTable(dst ...interface{}) error
	HasColumn(dst interface{}, field string) bool
	AddColumn(dst interface{}, field string) error
	HasIndex(dst interface{}, name string) bool
	CreateIndex(dst interface{}, name string) error
}

// Versioned migrations are additive by default. Existing column types are only
// changed by explicit migration SQL, avoiding GORM's smart-column comparison
// and making upgrades deterministic across Go/database driver versions.
func migrateModelsAdditive(database *gorm.DB, models ...interface{}) error {
	for _, model := range models {
		// A migration transaction may inherit the ledger query's table on its
		// Statement. Start a clean statement while retaining the same ConnPool,
		// transaction and advisory-lock ownership.
		modelDB := migrationSession(database)
		statement := &gorm.Statement{DB: modelDB}
		if err := statement.Parse(model); err != nil {
			return fmt.Errorf("parse migration model %T: %w", model, err)
		}
		columns := make([]string, 0, len(statement.Schema.DBNames))
		for _, name := range statement.Schema.DBNames {
			field := statement.Schema.FieldsByDBName[name]
			if field != nil && !field.IgnoreMigration {
				columns = append(columns, name)
			}
		}
		indexes := statement.Schema.ParseIndexes()
		indexNames := make([]string, 0, len(indexes))
		for _, index := range indexes {
			indexNames = append(indexNames, index.Name)
		}
		if err := applyAdditiveModel(modelDB.Migrator(), model, columns, indexNames); err != nil {
			return err
		}
	}
	return nil
}

func migrationSession(database *gorm.DB) *gorm.DB {
	return database.Session(&gorm.Session{NewDB: true})
}

func applyAdditiveModel(migrator additiveSchemaMigrator, model interface{}, columns, indexes []string) error {
	if !migrator.HasTable(model) {
		if err := migrator.CreateTable(model); err != nil {
			return fmt.Errorf("create migration table %T: %w", model, err)
		}
		return nil
	}
	for _, column := range columns {
		if migrator.HasColumn(model, column) {
			continue
		}
		if err := migrator.AddColumn(model, column); err != nil {
			return fmt.Errorf("add migration column %T.%s: %w", model, column, err)
		}
	}
	for _, index := range indexes {
		if migrator.HasIndex(model, index) {
			continue
		}
		if err := migrator.CreateIndex(model, index); err != nil {
			return fmt.Errorf("create migration index %T.%s: %w", model, index, err)
		}
	}
	return nil
}

func migratePostgresSchemaStatements(tx *gorm.DB) error {
	if err := tx.Exec(`
DO $$
DECLARE
  max_id BIGINT;
  sequence_last BIGINT;
  sequence_called BOOLEAN;
BEGIN
  IF to_regclass('public.users') IS NOT NULL THEN
    CREATE SEQUENCE IF NOT EXISTS users_id_seq;
    ALTER SEQUENCE users_id_seq OWNED BY users.id;
    SELECT COALESCE(MAX(id), 0) INTO max_id FROM users;
    SELECT last_value, is_called INTO sequence_last, sequence_called FROM users_id_seq;
    IF max_id = 0 AND NOT sequence_called THEN
      PERFORM setval('users_id_seq', 1, FALSE);
    ELSIF NOT sequence_called OR max_id > sequence_last THEN
      PERFORM setval('users_id_seq', GREATEST(max_id, 1), max_id > 0);
    END IF;
    ALTER TABLE users ALTER COLUMN id SET DEFAULT nextval('users_id_seq');
  END IF;
END $$`).Error; err != nil {
		return err
	}
	return tx.Exec(`DO $$ BEGIN IF to_regclass('public.messages') IS NOT NULL THEN CREATE INDEX IF NOT EXISTS idx_messages_sync_target_time_id ON messages (is_group, to_user_id, timestamp, message_id); END IF; END $$`).Error
}
