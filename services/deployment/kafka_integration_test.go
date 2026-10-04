package deployment

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSingleKafkaPersistenceAndDLQ(t *testing.T) {
	if os.Getenv("BETTERFLY_TEST_COMPOSE") != "true" {
		t.Skip("set BETTERFLY_TEST_COMPOSE=true to start disposable, isolated Kafka")
	}
	dir := t.TempDir()
	envFile, override := filepath.Join(dir, "env"), filepath.Join(dir, "isolated.yml")
	if err := os.WriteFile(envFile, []byte("PGSQL_DSN=not-used-by-kafka-test\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// No production container names, published ports, credentials or data volumes.
	if err := os.WriteFile(override, []byte(`services:
  kafka1:
    container_name: !reset null
    ports: !reset []
  kafka-init:
    container_name: !reset null
`), 0600); err != nil {
		t.Fatal(err)
	}
	project := fmt.Sprintf("bf-trim-test-%d", time.Now().UnixNano())
	base := []string{"compose", "--env-file", envFile, "-p", project, "-f", filepath.Join(serviceRoot(t), "docker-compose.yml"), "-f", override}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	run := func(ctx context.Context, input string, args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "docker", append(append([]string{}, base...), args...)...)
		cmd.Env = cleanEnv()
		cmd.Stdin = strings.NewReader(input)
		output, err := cmd.CombinedOutput()
		return string(output), err
	}
	must := func(input string, args ...string) string {
		t.Helper()
		output, err := run(ctx, input, args...)
		if err != nil {
			t.Fatalf("Docker integration %v: %v\n%s", args, err, output)
		}
		return output
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		if output, err := run(cleanup, "", "down", "-v", "--remove-orphans"); err != nil {
			t.Errorf("isolated test cleanup failed: %v\n%s", err, output)
		}
	})
	waitReady := func() {
		t.Helper()
		for i := 0; i < 30; i++ {
			attempt, stop := context.WithTimeout(ctx, 12*time.Second)
			_, err := run(attempt, "", "exec", "-T", "kafka1", "/opt/kafka/bin/kafka-topics.sh", "--bootstrap-server", "kafka1:9092", "--list")
			stop()
			if err == nil {
				return
			}
			select {
			case <-ctx.Done():
				t.Fatal("isolated Kafka readiness timed out")
			case <-time.After(500 * time.Millisecond):
			}
		}
		t.Fatal("isolated Kafka did not become ready")
	}
	must("", "up", "-d", "--no-deps", "kafka1")
	waitReady()
	must("", "run", "--rm", "--no-deps", "kafka-init")
	for _, topic := range []string{"data-forwarding-dlq", "storage-service-dlq", "friend-service-dlq", "call-service-dlq", "push-service-dlq"} {
		output := must("", "exec", "-T", "kafka1", "/opt/kafka/bin/kafka-topics.sh", "--bootstrap-server", "kafka1:9092", "--describe", "--topic", topic)
		for _, expected := range []string{"PartitionCount: 3", "ReplicationFactor: 1", "retention.ms=604800000", "min.insync.replicas=1"} {
			if !strings.Contains(output, expected) {
				t.Fatalf("%s missing %s: %s", topic, expected, output)
			}
		}
	}
	must("", "exec", "-T", "kafka1", "/opt/kafka/bin/kafka-topics.sh", "--bootstrap-server", "kafka1:9092", "--create", "--topic", "trim-regression", "--partitions", "3", "--replication-factor", "1")
	produce := func(data string) {
		must(data, "exec", "-T", "kafka1", "/opt/kafka/bin/kafka-console-producer.sh", "--bootstrap-server", "kafka1:9092", "--topic", "trim-regression")
	}
	consume := func(count string) string {
		return must("", "exec", "-T", "kafka1", "/opt/kafka/bin/kafka-console-consumer.sh", "--bootstrap-server", "kafka1:9092", "--topic", "trim-regression", "--group", "trim-test-group", "--from-beginning", "--max-messages", count, "--timeout-ms", "20000", "--consumer-property", "auto.commit.interval.ms=100")
	}
	offsets := func() int {
		output := must("", "exec", "-T", "kafka1", "/opt/kafka/bin/kafka-consumer-groups.sh", "--bootstrap-server", "kafka1:9092", "--describe", "--group", "trim-test-group")
		total := 0
		for _, line := range strings.Split(output, "\n") {
			fields := strings.Fields(line)
			if len(fields) > 4 && fields[1] == "trim-regression" {
				value, _ := strconv.Atoi(fields[3])
				total += value
			}
		}
		return total
	}
	produce("trim-persist-1\ntrim-persist-2\n")
	before := consume("2")
	if !strings.Contains(before, "trim-persist-1") || !strings.Contains(before, "trim-persist-2") || offsets() != 2 {
		t.Fatal("Kafka did not persist initial delivery and committed offsets")
	}
	must("", "up", "-d", "--no-deps", "--force-recreate", "kafka1")
	waitReady()
	if offsets() != 2 {
		t.Fatal("consumer offsets were lost when recreating the broker")
	}
	produce("trim-persist-3\n")
	after := consume("1")
	if !strings.Contains(after, "trim-persist-3") || strings.Contains(after, "trim-persist-1") || strings.Contains(after, "trim-persist-2") {
		t.Fatalf("recreated Kafka replayed consumed records or lost new delivery: %s", after)
	}
	// Replay retained data with a new group to verify records, not only offsets.
	retained := must("", "exec", "-T", "kafka1", "/opt/kafka/bin/kafka-console-consumer.sh", "--bootstrap-server", "kafka1:9092", "--topic", "trim-regression", "--group", "trim-replay-group", "--from-beginning", "--max-messages", "3", "--timeout-ms", "20000")
	for _, marker := range []string{"trim-persist-1", "trim-persist-2", "trim-persist-3"} {
		if !strings.Contains(retained, marker) {
			t.Fatalf("record missing after broker recreation: %s", marker)
		}
	}
}
