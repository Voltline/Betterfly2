package db

import (
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jackc/pgx/v5/pgconn"
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
		if (err != nil) != (version < CurrentSchemaVersion) {
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

func TestWithConnectTimeoutAddsDefaultOnlyWhenUnset(t *testing.T) {
	t.Setenv("PGCONNECT_TIMEOUT", "")
	for _, dsn := range []string{
		"host=127.0.0.1 user=betterfly dbname=Betterfly2 sslmode=disable",
		"postgres://betterfly@127.0.0.1:5432/Betterfly2?sslmode=disable",
	} {
		withTimeout, err := withConnectTimeout(dsn, 4500*time.Millisecond)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := pgconn.ParseConfig(withTimeout)
		if err != nil {
			t.Fatalf("parse %q: %v", withTimeout, err)
		}
		if parsed.ConnectTimeout != 5*time.Second || parsed.Database != "Betterfly2" {
			t.Fatalf("connect timeout not applied to %q: timeout=%s database=%s", withTimeout, parsed.ConnectTimeout, parsed.Database)
		}
	}

	explicit := "host=127.0.0.1 dbname=Betterfly2 connect_timeout=2"
	if got, err := withConnectTimeout(explicit, 5*time.Second); err != nil || got != explicit {
		t.Fatalf("explicit connect_timeout overridden: %q err=%v", got, err)
	}
	if got, err := withConnectTimeout("host=127.0.0.1", 0); err != nil || got != "host=127.0.0.1" {
		t.Fatalf("zero timeout should leave DSN unchanged: %q err=%v", got, err)
	}
	if _, err := withConnectTimeout("host=127.0.0.1", -1); err == nil {
		t.Fatal("invalid DB_CONNECT_TIMEOUT accepted")
	}
	t.Setenv("PGCONNECT_TIMEOUT", "3")
	if got, err := withConnectTimeout("host=127.0.0.1", 5*time.Second); err != nil || got != "host=127.0.0.1" {
		t.Fatalf("PGCONNECT_TIMEOUT should take precedence: %q err=%v", got, err)
	}
}
