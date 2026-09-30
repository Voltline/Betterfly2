package db

import (
	"github.com/DATA-DOG/go-sqlmock"
	"testing"
	"time"
)

func TestLoadPoolConfigDefaults(t *testing.T) {
	for _, key := range []string{"DB_MAX_OPEN_CONNS", "DB_MAX_IDLE_CONNS", "DB_CONN_MAX_LIFETIME", "DB_CONN_MAX_IDLE_TIME"} {
		t.Setenv(key, "")
	}
	config, err := LoadPoolConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.MaxOpenConns != 50 || config.MaxIdleConns != 10 || config.ConnMaxLifetime != time.Hour || config.ConnMaxIdleTime != 10*time.Minute {
		t.Fatalf("unexpected pool defaults: %+v", config)
	}
}

func TestStartupRequiresPublishedRecallSchema(t *testing.T) {
	plan := migrationPlan()
	if CurrentSchemaVersion != plan[len(plan)-1].Version {
		t.Fatalf("startup version %d differs from migrations %d", CurrentSchemaVersion, plan[len(plan)-1].Version)
	}
	for _, version := range []int{4, 5, 6} {
		database, mock := newInboxDatabase(t)
		mock.ExpectQuery(`(?s)SELECT count\(\*\) FROM information_schema.tables`).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
		mock.ExpectQuery(`SELECT COALESCE\(MAX\(version\), 0\) FROM "schema_migrations"`).WillReturnRows(sqlmock.NewRows([]string{"version"}).AddRow(version))
		err := CheckSchemaVersion(database)
		if (err != nil) != (version < 5) {
			t.Fatalf("schema %d error=%v", version, err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLoadPoolConfigUsesEnvironmentAndValidatesRelationships(t *testing.T) {
	t.Setenv("DB_MAX_OPEN_CONNS", "24")
	t.Setenv("DB_MAX_IDLE_CONNS", "8")
	t.Setenv("DB_CONN_MAX_LIFETIME", "45m")
	t.Setenv("DB_CONN_MAX_IDLE_TIME", "5m")
	config, err := LoadPoolConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.MaxOpenConns != 24 || config.MaxIdleConns != 8 || config.ConnMaxLifetime != 45*time.Minute || config.ConnMaxIdleTime != 5*time.Minute {
		t.Fatalf("environment was not applied: %+v", config)
	}

	t.Setenv("DB_MAX_IDLE_CONNS", "25")
	if _, err := LoadPoolConfig(); err == nil {
		t.Fatal("idle connections above max open were accepted")
	}
	t.Setenv("DB_MAX_IDLE_CONNS", "8")
	t.Setenv("DB_CONN_MAX_IDLE_TIME", "1h")
	if _, err := LoadPoolConfig(); err == nil {
		t.Fatal("idle lifetime above maximum lifetime was accepted")
	}
}
