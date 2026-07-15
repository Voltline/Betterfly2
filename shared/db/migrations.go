package db

import (
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func RunMigrations(database *gorm.DB) error {
	models := []interface{}{
		&SchemaMigration{},
		&ConsumerOperationResult{},
		&User{}, &Friend{}, &Group{}, &GroupMember{}, &RelationshipRequest{},
		&Message{}, &FileMetadata{},
		&ABExperiment{}, &ABExperimentGroup{}, &ABExperimentOverride{},
		&PushDeviceToken{}, &PushMessageDelivery{}, &PushDebugAudit{},
	}
	if err := database.AutoMigrate(models...); err != nil {
		return err
	}
	if database.Dialector.Name() == "postgres" {
		if err := MigratePostgresSchema(database); err != nil {
			return err
		}
	}
	return database.Transaction(func(tx *gorm.DB) error {
		if err := BackfillGroupMemberJoinedAtWithDB(tx); err != nil {
			return err
		}
		if err := tx.Model(&PushMessageDelivery{}).
			Where("status IS NULL OR status = ''").
			Updates(map[string]interface{}{
				"status":     "sent",
				"updated_at": gorm.Expr("created_at"),
			}).Error; err != nil {
			return err
		}
		return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&SchemaMigration{
			Version: CurrentSchemaVersion, AppliedAt: time.Now().UTC().Format(time.RFC3339Nano),
		}).Error
	})
}

// MigratePostgresSchema repairs defaults and indexes that AutoMigrate cannot
// reliably add to databases created by older Betterfly2 versions.
func MigratePostgresSchema(database *gorm.DB) error {
	return database.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtext('betterfly2_schema_migration'))`).Error; err != nil {
			return err
		}
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
	})
}
