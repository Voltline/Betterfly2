package db

import (
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
