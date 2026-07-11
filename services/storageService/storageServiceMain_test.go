package main

import "testing"

func TestEnvBool(t *testing.T) {
	t.Setenv("METRICS_ENABLED", "false")
	if envBool("METRICS_ENABLED", true) {
		t.Fatal("expected explicit false to disable metrics")
	}

	t.Setenv("METRICS_ENABLED", "invalid")
	if !envBool("METRICS_ENABLED", true) {
		t.Fatal("expected invalid value to use fallback")
	}
}
