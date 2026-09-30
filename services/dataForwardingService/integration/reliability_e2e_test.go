package integration

import (
	pb "Betterfly2/proto/data_forwarding"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func probeRequest(t *testing.T, p *networkProbe, request *pb.RequestMessage, match func(*pb.ResponseMessage) bool) *pb.ResponseMessage {
	t.Helper()
	response, err := p.request(request, match)
	if err != nil {
		t.Fatal(err)
	}
	return response.message
}

func probeReconnect(t *testing.T, previous *networkProbe, port string) *networkProbe {
	t.Helper()
	p, err := openNetworkProbe(port)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.close)
	if err := p.login(previous.account); err != nil {
		t.Fatal(err)
	}
	return p
}

func probeSync(t *testing.T, p *networkProbe, query *pb.QuerySyncMessages) *pb.SyncMessagesRsp {
	t.Helper()
	return probeRequest(t, p, &pb.RequestMessage{Payload: &pb.RequestMessage_QuerySyncMessages{QuerySyncMessages: query}}, func(r *pb.ResponseMessage) bool { return r.GetSyncMsgsRsp() != nil }).GetSyncMsgsRsp()
}

func TestReliabilityEndToEnd(t *testing.T) {
	if os.Getenv("BETTERFLY_ACCEPTANCE") != "1" {
		t.Skip("set BETTERFLY_ACCEPTANCE=1 against a dedicated Compose test environment")
	}
	database := acceptanceDatabase(t)
	port1, port2 := envOr("BETTERFLY_DF_PORT_1", defaultDFPort1), envOr("BETTERFLY_DF_PORT_2", defaultDFPort2)
	var users []*networkProbe
	for i, port := range []string{port1, port2, port2} {
		p, err := openNetworkProbe(port)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(p.close)
		if err := p.signup(randomAccount(t, fmt.Sprintf("acc%d", i))); err != nil {
			t.Fatal(err)
		}
		users = append(users, p)
	}
	owner, online, offline := users[0], users[1], users[2]
	prepareAcceptanceOffsets(t, database, owner)
	var databaseRTT []time.Duration
	for i := 0; i < 10; i++ {
		started := time.Now()
		var value int
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := database.WithContext(ctx).Raw("SELECT 1").Scan(&value).Error
		cancel()
		if err != nil || value != 1 {
			t.Fatal("database RTT probe failed")
		}
		databaseRTT = append(databaseRTT, time.Since(started))
	}
	t.Logf("DATABASE_SELECT1_RTT p50_ms=%.2f p95_ms=%.2f (host-to-current-database; not server SQL timing)", percentile(databaseRTT, .5), percentile(databaseRTT, .95))
	groupID := time.Now().UnixNano()%1000000000 + 7000000000
	probeRequest(t, owner, &pb.RequestMessage{Payload: &pb.RequestMessage_InsertGroup{InsertGroup: &pb.InsertGroup{ToBeCreatedGroupId: groupID, ToBeCreatedGroupName: "acceptance-reliability"}}}, func(r *pb.ResponseMessage) bool { return r.GetServer() != nil })
	for _, member := range []*networkProbe{online, offline} {
		request := probeRequest(t, member, &pb.RequestMessage{Payload: &pb.RequestMessage_InsertGroupUser{InsertGroupUser: &pb.InsertGroupUser{TargetGroupId: groupID}}}, func(r *pb.ResponseMessage) bool { return r.GetRelationshipOperationRsp() != nil }).GetRelationshipOperationRsp()
		if request.GetResult() != "FRIEND_OK" {
			t.Fatal("join application failed")
		}
		accepted := probeRequest(t, owner, &pb.RequestMessage{Payload: &pb.RequestMessage_ResolveGroupJoinRequest{ResolveGroupJoinRequest: &pb.ResolveGroupJoinRequest{RequestId: request.GetRequest().GetRequestId(), Decision: pb.RequestDecision_REQUEST_ACCEPT}}}, func(r *pb.ResponseMessage) bool { return r.GetRelationshipOperationRsp() != nil }).GetRelationshipOperationRsp()
		if accepted.GetResult() != "FRIEND_OK" {
			t.Fatal("join approval failed")
		}
	}
	start := time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)
	offline.close()
	var ids []int64
	for i := 0; i < 5; i++ {
		clientID := fmt.Sprintf("acceptance-%d-%d", groupID, i)
		post := &pb.Post{IsGroup: true, ToId: groupID, Msg: "acceptance-message", MsgType: "text", Timestamp: time.Now().UTC().Format(time.RFC3339Nano), ClientMessageId: clientID}
		ackMatch := func(r *pb.ResponseMessage) bool {
			return r.GetPostAckRsp() != nil && r.GetPostAckRsp().GetClientMessageId() == clientID
		}
		ack := probeRequest(t, owner, &pb.RequestMessage{Payload: &pb.RequestMessage_Post{Post: post}}, ackMatch).GetPostAckRsp()
		if ack.GetMessageId() <= 0 {
			t.Fatal("missing server message ID")
		}
		ids = append(ids, ack.GetMessageId())
		response, err := online.wait(15*time.Second, func(r *pb.ResponseMessage) bool {
			return r.GetPost() != nil && r.GetPost().GetMessageId() == ack.GetMessageId()
		})
		if err != nil {
			t.Fatal(err)
		}
		if response.message.GetPost().GetClientMessageId() != clientID {
			t.Fatal("ACK/realtime identity mismatch")
		}
		repeated := probeRequest(t, owner, &pb.RequestMessage{Payload: &pb.RequestMessage_Post{Post: post}}, ackMatch).GetPostAckRsp()
		if repeated.GetMessageId() != ack.GetMessageId() {
			t.Fatal("idempotent resend generated another message ID")
		}
	}
	time.Sleep(time.Second)
	for _, id := range ids {
		if online.postCount(id) != 1 {
			t.Fatalf("message %d delivered %d times", id, online.postCount(id))
		}
	}
	t.Log("PASS group cross-Pod delivery + stable ACK IDs + repeated client_message_id (five messages)")
	offline = probeReconnect(t, offline, port2)
	query := &pb.QuerySyncMessages{Timestamp: start, PageSize: 2}
	seen := map[int64]bool{}
	pages := 0
	for ; pages < 10; pages++ {
		page := probeSync(t, offline, query)
		for _, message := range page.GetMsgs() {
			if seen[message.GetMessageId()] {
				t.Fatal("duplicate message across synchronization pages")
			}
			seen[message.GetMessageId()] = true
		}
		if !page.GetHasMore() {
			pages++
			break
		}
		if page.GetNextCursorMessageId() <= query.GetCursorMessageId() {
			t.Fatal("non-advancing message cursor")
		}
		query.CursorTimestamp, query.CursorMessageId = page.GetNextCursorTimestamp(), page.GetNextCursorMessageId()
	}
	if len(seen) != len(ids) || pages != 3 {
		t.Fatalf("expected five messages in three pages: messages=%d pages=%d", len(seen), pages)
	}
	for _, id := range ids {
		if !seen[id] {
			t.Fatalf("offline synchronization missing message %d", id)
		}
	}
	t.Log("PASS offline reconnect + page_size=2 synchronization: all five message IDs recovered in three pages")
	kafka, _ := acceptanceKafka(t)
	storageEnd := func() int64 {
		partitions, err := kafka.Partitions("storage-service")
		if err != nil {
			t.Fatal(err)
		}
		var total int64
		for _, partition := range partitions {
			end, err := kafka.GetOffset("storage-service", partition, -1)
			if err != nil {
				t.Fatal(err)
			}
			total += end
		}
		return total
	}
	before := storageEnd()
	if err := offline.send(&pb.RequestMessage{Payload: &pb.RequestMessage_QuerySyncMessages{QuerySyncMessages: &pb.QuerySyncMessages{ToUserId: owner.userID, Timestamp: start}}}); err != nil {
		t.Fatal(err)
	}
	_, denial := offline.wait(time.Second, func(r *pb.ResponseMessage) bool { return r.GetSyncMsgsRsp() != nil })
	if denial == nil || !strings.Contains(denial.Error(), "response timeout") || storageEnd() != before {
		t.Fatal("forged synchronization request returned data or reached Storage")
	}
	t.Log("PASS forged synchronization target blocked before Kafka; current runtime logs denial without a client Warn response")
	offline.close()
	lastID := ids[len(ids)-1]
	recall := probeRequest(t, owner, &pb.RequestMessage{Payload: &pb.RequestMessage_RecallMessage{RecallMessage: &pb.RecallMessage{MessageId: lastID}}}, func(r *pb.ResponseMessage) bool {
		return r.GetMessageRecallEvent() != nil && r.GetMessageRecallEvent().GetMessageId() == lastID
	}).GetMessageRecallEvent()
	if recall.GetResult() != pb.MessageRecallResult_MESSAGE_RECALL_OK {
		t.Fatalf("recall failed: %s", recall.GetResult())
	}
	if _, err := online.wait(15*time.Second, func(r *pb.ResponseMessage) bool {
		return r.GetMessageRecallEvent() != nil && r.GetMessageRecallEvent().GetMessageId() == lastID
	}); err != nil {
		t.Fatal(err)
	}
	offline = probeReconnect(t, offline, port2)
	changes := probeSync(t, offline, &pb.QuerySyncMessages{Timestamp: time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano), PageSize: 2, IncludeRecalledChanges: true, RecallCursorTimestamp: start})
	if len(changes.GetMsgs()) != 0 || len(changes.GetRecalledMsgs()) != 1 {
		t.Fatalf("independent recall cursor failed: messages=%d recalls=%d", len(changes.GetMsgs()), len(changes.GetRecalledMsgs()))
	}
	tombstone := changes.GetRecalledMsgs()[0]
	if tombstone.GetMessageId() != lastID || !tombstone.GetIsRecalled() || tombstone.GetContent() != "" {
		t.Fatal("invalid recall tombstone or leaked message content")
	}
	t.Log("PASS realtime recall + offline independent recall cursor + tombstone without body")
	moved := probeReconnect(t, owner, port2)
	select {
	case <-owner.done:
	case <-time.After(10 * time.Second):
		t.Fatal("old connection survived ownership transfer")
	}
	owner = moved
	if page := probeSync(t, owner, &pb.QuerySyncMessages{Timestamp: start, PageSize: 10}); len(page.GetMsgs()) != 5 {
		t.Fatal("sender's own group messages missing after ownership transfer")
	}
	t.Log("PASS cross-Pod ownership transfer closes stale socket; sender's own group history remains visible")
	if os.Getenv("BETTERFLY_E2E_RESTART") != "1" {
		t.Log("SKIP live DF restart: set BETTERFLY_E2E_RESTART=1 with explicit permission")
		return
	}
	beforeRestart := probeReconnect(t, offline, port1)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	command := exec.CommandContext(ctx, "docker", "compose", "restart", "df")
	command.Dir = ".."
	err := command.Run()
	cancel()
	if err != nil {
		t.Fatalf("approved DF restart failed: %v", err)
	}
	select {
	case <-beforeRestart.done:
	case <-time.After(5 * time.Second):
		t.Fatal("restarted DF socket did not close")
	}
	var restarted *networkProbe
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		restarted, err = openNetworkProbe(port1)
		if err == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if err != nil {
		t.Fatal("DF did not become available after restart")
	}
	t.Cleanup(restarted.close)
	if err := restarted.login(offline.account); err != nil {
		t.Fatal(err)
	}
	if page := probeSync(t, restarted, &pb.QuerySyncMessages{Timestamp: start, PageSize: 10, IncludeRecalledChanges: true, RecallCursorTimestamp: start}); len(page.GetMsgs()) != 5 || len(page.GetRecalledMsgs()) != 1 {
		t.Fatal("history/recall state not recovered after DF restart")
	}
	t.Log("PASS approved single-DF restart + same-account reconnect + persisted history/recall recovery")
}
