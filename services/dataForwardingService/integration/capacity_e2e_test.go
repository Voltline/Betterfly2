package integration

import (
	pb "Betterfly2/proto/data_forwarding"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type capacitySample struct{ ack, delivery time.Duration }
type capacityPair struct{ sender, receiver *networkProbe }
type capacityStage struct {
	Pairs             int      `json:"pairs"`
	Connections       int      `json:"connections"`
	Completed         int      `json:"completed"`
	Errors            []string `json:"errors"`
	ElapsedSeconds    float64  `json:"elapsed_seconds"`
	MessagesPerSecond float64  `json:"messages_per_second"`
	ACKP50MS          float64  `json:"ack_p50_ms"`
	ACKP95MS          float64  `json:"ack_p95_ms"`
	ACKP99MS          float64  `json:"ack_p99_ms"`
	DeliveryP95MS     float64  `json:"delivery_p95_ms"`
}

func percentile(values []time.Duration, percentile float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	index := int(float64(len(values)-1) * percentile)
	return float64(values[index]) / float64(time.Millisecond)
}

func TestCapacityEndToEnd(t *testing.T) {
	if os.Getenv("BETTERFLY_CAPACITY") != "1" {
		t.Skip("set BETTERFLY_CAPACITY=1 for bounded live load; creates dedicated test accounts")
	}
	stages := []int{}
	for _, raw := range strings.Split(envOr("BETTERFLY_CAPACITY_PAIRS", "1,4,8,16"), ",") {
		value, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil || value < 1 || value > 64 {
			t.Fatal("capacity pairs must be between 1 and 64")
		}
		stages = append(stages, value)
	}
	duration, err := time.ParseDuration(envOr("BETTERFLY_CAPACITY_DURATION", "20s"))
	if err != nil || duration < 5*time.Second || duration > time.Minute {
		t.Fatal("capacity duration must be between 5s and 1m")
	}
	maxPairs := 0
	for _, n := range stages {
		if n > maxPairs {
			maxPairs = n
		}
	}
	runID := randomAccount(t, "cap")
	var pairs []capacityPair
	database := acceptanceDatabase(t)
	for i := 0; i < maxPairs; i++ {
		pair := capacityPair{}
		for endpoint, target := range []**networkProbe{&pair.sender, &pair.receiver} {
			port := envOr("BETTERFLY_DF_PORT_1", defaultDFPort1)
			if endpoint == 1 {
				port = envOr("BETTERFLY_DF_PORT_2", defaultDFPort2)
			}
			probe, err := openNetworkProbe(port)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(probe.close)
			if err := probe.signup(fmt.Sprintf("%s_%d_%d", runID, i, endpoint)); err != nil {
				t.Fatal(err)
			}
			*target = probe
		}
		pairs = append(pairs, pair)
	}
	prepareAcceptanceOffsets(t, database, pairs[0].sender)
	kafka, _ := acceptanceKafka(t)
	dlqBefore := acceptanceDLQOffsets(t, kafka)
	// Synchronization request is a barrier after offset-isolation warm-up.
	if _, err := pairs[0].sender.request(&pb.RequestMessage{Payload: &pb.RequestMessage_QuerySyncMessages{QuerySyncMessages: &pb.QuerySyncMessages{Timestamp: time.Now().UTC().Format(time.RFC3339), PageSize: 1}}}, func(r *pb.ResponseMessage) bool { return r.GetSyncMsgsRsp() != nil }); err != nil {
		t.Fatal(err)
	}
	for _, n := range stages {
		t.Logf("CAPACITY_STAGE_BEGIN pairs=%d all_live_connections=%d duration=%s", n, 2*maxPairs, duration)
		started := time.Now()
		deadline := started.Add(duration)
		var wg sync.WaitGroup
		var mu sync.Mutex
		var samples []capacitySample
		var failures []string
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(index int, pair capacityPair) {
				defer wg.Done()
				for sequence := 0; time.Now().Before(deadline); sequence++ {
					id := fmt.Sprintf("%s-%d-%d-%d", runID, n, index, sequence)
					post := &pb.Post{ToId: pair.receiver.userID, Msg: strings.Repeat("x", 128), MsgType: "text", Timestamp: time.Now().UTC().Format(time.RFC3339), ClientMessageId: id}
					sent := time.Now()
					ack, err := pair.sender.request(&pb.RequestMessage{Payload: &pb.RequestMessage_Post{Post: post}}, func(r *pb.ResponseMessage) bool {
						return r.GetPostAckRsp() != nil && r.GetPostAckRsp().GetClientMessageId() == id
					})
					if err == nil {
						var delivered receivedResponse
						delivered, err = pair.receiver.wait(15*time.Second, func(r *pb.ResponseMessage) bool { return r.GetPost() != nil && r.GetPost().GetClientMessageId() == id })
						if err == nil {
							messageID := ack.message.GetPostAckRsp().GetMessageId()
							if messageID <= 0 || delivered.message.GetPost().GetMessageId() != messageID || pair.receiver.postCount(messageID) != 1 {
								err = fmt.Errorf("message identity mismatch or duplicate for pair %d", index)
							}
							if err == nil {
								mu.Lock()
								samples = append(samples, capacitySample{ack.at.Sub(sent), delivered.at.Sub(sent)})
								mu.Unlock()
							}
						}
					}
					if err != nil {
						mu.Lock()
						failures = append(failures, fmt.Sprintf("pair %d: %v", index, err))
						mu.Unlock()
						return
					}
				}
			}(i, pairs[i])
		}
		time.Sleep(duration / 2)
		statsCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		stats, statsErr := exec.CommandContext(statsCtx, "docker", "stats", "--no-stream", "--format", "{{.Name}} CPU={{.CPUPerc}} Memory={{.MemUsage}}", "df", "df2", "storageService", "pushService", "redis", "kafka1", "kafka2").CombinedOutput()
		cancel()
		if statsErr == nil {
			t.Logf("CAPACITY_RESOURCES pairs=%d\n%s", n, stats)
		} else {
			t.Logf("Docker resource sampling unavailable: %v", statsErr)
		}
		wg.Wait()
		acks, deliveries := []time.Duration{}, []time.Duration{}
		for _, sample := range samples {
			acks = append(acks, sample.ack)
			deliveries = append(deliveries, sample.delivery)
		}
		elapsed := time.Since(started).Seconds()
		report := capacityStage{Pairs: n, Connections: 2 * maxPairs, Completed: len(samples), Errors: failures, ElapsedSeconds: elapsed, MessagesPerSecond: float64(len(samples)) / elapsed, ACKP50MS: percentile(acks, .5), ACKP95MS: percentile(acks, .95), ACKP99MS: percentile(acks, .99), DeliveryP95MS: percentile(deliveries, .95)}
		encoded, _ := json.Marshal(report)
		t.Logf("CAPACITY_RESULT %s", encoded)
		if len(failures) > 0 {
			t.Errorf("capacity stage failed; stopping escalation")
			break
		}
		if report.ACKP95MS > 5000 {
			t.Log("CAPACITY_STOP ACK p95 exceeded 5s; higher concurrency not attempted")
			break
		}
	}
	dlqAfter := acceptanceDLQOffsets(t, kafka)
	for topic, before := range dlqBefore {
		if after := dlqAfter[topic]; after != before {
			t.Errorf("DLQ grew during healthy load: topic=%s before=%d after=%d", topic, before, after)
		}
	}
	t.Logf("CAPACITY_SCOPE closed-loop cross-Pod 128-byte direct messages; no real APNs devices; %d dedicated accounts retained; this is measured throughput, not a maximum connection count", 2*maxPairs)
}
