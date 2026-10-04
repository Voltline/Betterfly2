package deployment

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func serviceRoot(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	return filepath.Dir(filepath.Dir(file))
}

func cleanEnv(extra ...string) []string {
	env := []string{}
	for _, entry := range os.Environ() {
		key := strings.SplitN(entry, "=", 2)[0]
		if key == "METRICS_ENABLED" || key == "COMPOSE_PROFILES" || key == "COMPOSE_FILE" || key == "COMPOSE_PARALLEL_LIMIT" || strings.HasPrefix(key, "MOCK_") {
			continue
		}
		env = append(env, entry)
	}
	return append(env, extra...)
}

func TestEntryPointsSelectOnlyRequestedCapabilities(t *testing.T) {
	cases := []struct {
		name, script string
		args         []string
		want, absent []string
	}{
		{"daily", "build_docker_compose.sh", nil, []string{"Preset: standard", "--profile group-calls", "COMPOSE_PARALLEL_LIMIT=2", "METRICS_ENABLED=false"}, []string{"--profile observability", "--profile tools", "--profile redundancy", "Would run: make", "--force-recreate", "kafka-cluster.yml"}},
		{"distributed", "build_docker_compose.sh", []string{"distributed"}, []string{"--profile redundancy", "--profile group-calls"}, []string{"--profile tools", "kafka-cluster.yml"}},
		{"full", "deploy_docker_compose.sh", []string{"full"}, []string{"--profile observability", "--profile tools", "--profile redundancy", "METRICS_ENABLED=true"}, []string{"kafka-cluster.yml"}},
		{"minimal", "deploy_docker_compose.sh", []string{"minimal"}, []string{"METRICS_ENABLED=false"}, []string{"--profile storage", "--profile group-calls"}},
		{"one_to_one", "deploy_docker_compose.sh", []string{"standard", "--without-group-calls"}, []string{"--profile calls"}, []string{"--profile group-calls"}},
		{"diagnostics", "deploy_docker_compose.sh", []string{"standard", "--enable", "observability"}, []string{"METRICS_ENABLED=true", "--profile observability"}, []string{"--profile tools"}},
		{"cluster", "deploy_docker_compose.sh", []string{"distributed", "--kafka-cluster"}, []string{"docker-compose.kafka-cluster.yml", "--profile redundancy"}, nil},
		{"rebuild_daily", "rebuild_docker_compose.sh", []string{"dataforwarding"}, []string{"--no-deps df"}, []string{"--no-deps df df2", "--profile redundancy", "--force-recreate"}},
		{"rebuild_distributed", "rebuild_docker_compose.sh", []string{"--preset", "distributed", "dataforwarding"}, []string{"--no-deps df df2", "--profile redundancy"}, []string{"kafka-cluster.yml"}},
		{"explicit_df_all", "rebuild_docker_compose.sh", []string{"df-all"}, []string{"--no-deps df df2", "--profile redundancy"}, nil},
		{"rebuild_all_daily", "rebuild_docker_compose.sh", []string{"--all"}, []string{"Preset: standard"}, []string{"--profile redundancy", "--profile observability", "--force-recreate"}},
		{"explicit_force_proto", "rebuild_docker_compose.sh", []string{"--proto", "--force-recreate", "storage"}, []string{"Would run: make", "--force-recreate", "--no-deps storage_service"}, nil},
		{"cluster_rebuild", "rebuild_docker_compose.sh", []string{"--kafka-cluster", "kafka2"}, []string{"docker-compose.kafka-cluster.yml", "--no-deps kafka2"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{filepath.Join(serviceRoot(t), tc.script)}, tc.args...)
			args = append(args, "--no-sudo", "--dry-run")
			cmd := exec.Command("/bin/bash", args...)
			cmd.Env = cleanEnv("COMPOSE_PROFILES=redundancy,observability,tools")
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("%v: %s", err, output)
			}
			for _, want := range tc.want {
				if !strings.Contains(string(output), want) {
					t.Fatalf("missing %q: %s", want, output)
				}
			}
			for _, absent := range tc.absent {
				if strings.Contains(string(output), absent) {
					t.Fatalf("unexpected %q: %s", absent, output)
				}
			}
		})
	}
}

func TestInvalidDeploymentOptionsFail(t *testing.T) {
	for _, args := range [][]string{{"--preset"}, {"--preset", "wrong", "df"}, {"--enable", "unknown", "df"}, {"kafka2"}, {"--all", "df"}, {"--enable"}} {
		cmd := exec.Command("/bin/bash", append([]string{filepath.Join(serviceRoot(t), "rebuild_docker_compose.sh")}, append(args, "--dry-run", "--no-sudo")...)...)
		cmd.Env = cleanEnv()
		if output, err := cmd.CombinedOutput(); err == nil {
			t.Fatalf("accepted invalid options %v: %s", args, output)
		}
	}
}

func TestDisabledGroupCallsDoNotAdvertiseSFU(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		args := []string{"-c", `SCRIPT_DIR="$1"; PRESET=standard; GROUP_CALLS=0; KAFKA_CLUSTER=0; EXTRA_PROFILES=(); source "$SCRIPT_DIR/compose_environment.sh"; if [[ "$2" == true ]]; then add_profile group-calls; fi; select_compose_environment; printf '%s\n' "${LIVEKIT_API_URL:+enabled}"`, "test", serviceRoot(t), strconv.FormatBool(enabled)}
		cmd := exec.Command("/bin/bash", args...)
		cmd.Env = cleanEnv("LIVEKIT_API_URL=http://fixture:7880", "LIVEKIT_PUBLIC_URL=ws://fixture:7880", "LIVEKIT_API_KEY=test-only", "LIVEKIT_API_SECRET=test-only")
		output, err := cmd.CombinedOutput()
		if err != nil || (strings.TrimSpace(string(output)) == "enabled") != enabled {
			t.Fatalf("group-calls=%v: %v %s", enabled, err, output)
		}
	}
}

func TestTopologyGuardFailsClosedBeforeAnyMutation(t *testing.T) {
	cases := []struct {
		name, metadata string
		cluster, ok    bool
	}{
		{"fresh", "", false, true},
		{"same_single", "kafka1 single", false, true},
		{"same_cluster", "kafka1 cluster\nkafka2 cluster", true, true},
		{"legacy_unlabelled", "kafka1 <no value>\nkafka2 <no value>", false, false},
		{"cluster_to_single", "kafka1 cluster\nkafka2 cluster", false, false},
		{"single_to_cluster", "kafka1 single", true, false},
		{"orphan_second_broker", "kafka1 single\nkafka2 cluster", false, false},
		{"inspect_failure", "ERROR", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			trace := filepath.Join(dir, "trace")
			stub := `#!/bin/bash
printf '%s\n' "$*" >> "$MOCK_TRACE"
if [[ "$1" == "inspect" ]]; then
  [[ "$MOCK_METADATA" != "ERROR" ]] || exit 1
  printf '%s\n' "$MOCK_METADATA"
elif [[ "$*" == *" ps -a -q" ]]; then
  [[ -z "$MOCK_METADATA" ]] || echo test-container
fi
`
			if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(stub), 0755); err != nil {
				t.Fatal(err)
			}
			args := []string{filepath.Join(serviceRoot(t), "deploy_docker_compose.sh"), "standard", "--no-build", "--no-sudo"}
			if tc.cluster {
				args = append(args, "--kafka-cluster")
			}
			cmd := exec.Command("/bin/bash", args...)
			cmd.Env = cleanEnv("PATH="+dir+":"+os.Getenv("PATH"), "MOCK_TRACE="+trace, "MOCK_METADATA="+tc.metadata)
			output, err := cmd.CombinedOutput()
			if (err == nil) != tc.ok {
				t.Fatalf("success=%v want=%v: %s", err == nil, tc.ok, output)
			}
			commands, _ := os.ReadFile(trace)
			mutated := strings.Contains(string(commands), " up -d")
			if mutated != tc.ok {
				t.Fatalf("mutation=%v want=%v: %s", mutated, tc.ok, commands)
			}
		})
	}
}

type composeService struct {
	Environment map[string]string `json:"environment"`
	DependsOn   map[string]any    `json:"depends_on"`
	Labels      map[string]string `json:"labels"`
	Volumes     []struct {
		Source string `json:"source"`
		Target string `json:"target"`
	} `json:"volumes"`
	Logging struct {
		Driver  string            `json:"driver"`
		Options map[string]string `json:"options"`
	} `json:"logging"`
}

func TestComposeTopologiesPreserveFeaturesAndReliability(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("Docker CLI required to validate Compose models")
	}
	envFile := filepath.Join(t.TempDir(), "env")
	if err := os.WriteFile(envFile, []byte("PGSQL_DSN=not-used-by-config-test\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"standard", "distributed", "full", "cluster"} {
		t.Run(mode, func(t *testing.T) {
			args := []string{"compose", "--env-file", envFile, "-f", filepath.Join(serviceRoot(t), "docker-compose.yml")}
			if mode == "cluster" {
				args = append(args, "-f", filepath.Join(serviceRoot(t), "docker-compose.kafka-cluster.yml"))
			}
			for _, profile := range []string{"storage", "calls", "notifications", "group-calls", "experiments", "moments"} {
				args = append(args, "--profile", profile)
			}
			if mode != "standard" {
				args = append(args, "--profile", "redundancy")
			}
			if mode == "full" {
				args = append(args, "--profile", "observability", "--profile", "tools")
			}
			cmd := exec.Command("docker", append(args, "config", "--format", "json")...)
			cmd.Env = cleanEnv()
			output, err := cmd.Output()
			if err != nil {
				t.Fatal(err)
			}
			var model struct {
				Services map[string]composeService `json:"services"`
			}
			if err := json.Unmarshal(output, &model); err != nil {
				t.Fatal(err)
			}
			wantCount := map[string]int{"standard": 15, "distributed": 16, "full": 19, "cluster": 17}[mode]
			if len(model.Services) != wantCount {
				t.Fatalf("service count=%d want=%d (includes two one-shot jobs)", len(model.Services), wantCount)
			}
			for _, name := range []string{"df", "auth_service", "friend_service", "storage_service", "rustfs", "push_service", "call_service", "coturn", "livekit", "abtest_service", "moments_service", "redis"} {
				if _, ok := model.Services[name]; !ok {
					t.Fatalf("product capability lost: %s", name)
				}
			}
			broker, factor, topology := "kafka1:9092", "1", "single"
			if mode == "cluster" {
				broker, factor, topology = "kafka1:9092,kafka2:9094", "2", "cluster"
			}
			for _, name := range []string{"df", "df2", "friend_service", "storage_service", "call_service", "push_service", "kafka-init"} {
				if s, ok := model.Services[name]; ok && s.Environment["KAFKA_BROKER"] != broker {
					t.Fatalf("%s has wrong broker", name)
				}
			}
			init := model.Services["kafka-init"]
			if init.Environment["DLQ_REPLICATION_FACTOR"] != factor || init.Environment["DLQ_MIN_ISR"] != "1" || init.Environment["DLQ_PARTITIONS"] != "3" {
				t.Fatal("invalid DLQ replication/ISR/partition settings")
			}
			for _, prefix := range []string{"DF", "STORAGE", "FRIEND", "CALL", "PUSH"} {
				if init.Environment[prefix+"_DLQ_RETENTION_MS"] != "604800000" {
					t.Fatal("DLQ replay retention was reduced")
				}
			}
			kafka := model.Services["kafka1"]
			if kafka.Labels["betterfly.kafka.topology"] != topology || kafka.Environment["KAFKA_OFFSETS_TOPIC_REPLICATION_FACTOR"] != factor || kafka.Environment["KAFKA_TRANSACTION_STATE_LOG_REPLICATION_FACTOR"] != factor || kafka.Environment["KAFKA_NUM_PARTITIONS"] != "3" {
				t.Fatal("invalid broker topology")
			}
			if len(kafka.Volumes) != 1 || kafka.Volumes[0].Target != "/var/lib/kafka/data" || kafka.Environment["KAFKA_LOG_DIRS"] != "/var/lib/kafka/data/betterfly" {
				t.Fatal("Kafka data is not explicitly persisted")
			}
			if mode == "cluster" && strings.Contains(kafka.Volumes[0].Source, "single") {
				t.Fatal("cluster reused single-node metadata")
			}
			if mode != "cluster" {
				if _, ok := init.DependsOn["kafka2"]; ok {
					t.Fatal("daily initialization still requires kafka2")
				}
			}
			for name, service := range model.Services {
				if service.Logging.Driver != "local" || service.Logging.Options["max-size"] != "10m" || service.Logging.Options["max-file"] != "3" {
					t.Fatalf("unbounded container logs: %s", name)
				}
			}
		})
	}
}
