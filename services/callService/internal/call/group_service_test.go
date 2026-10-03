package call

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	callpb "Betterfly2/proto/call"
	envelope "Betterfly2/proto/envelope"
	pushpb "Betterfly2/proto/push"
	"github.com/IBM/sarama"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"google.golang.org/protobuf/proto"
)

type groupTestAccess map[int64]string

func (a groupTestAccess) Access(_ context.Context, group, user int64) (GroupAccess, error) {
	if group != 10 || a[user] == "" {
		return GroupAccess{}, ErrForbidden
	}
	return GroupAccess{Role: a[user], Name: "测试群"}, nil
}

func (a groupTestAccess) Members(_ context.Context, group int64) ([]int64, error) {
	var members []int64
	if group == 10 {
		for user, role := range a {
			if role == "owner" || role == "admin" || role == "member" {
				members = append(members, user)
			}
		}
	}
	sort.Slice(members, func(i, j int) bool { return members[i] < members[j] })
	return members, nil
}

type groupFailAccess struct{ err error }

func (a groupFailAccess) Access(context.Context, int64, int64) (GroupAccess, error) {
	return GroupAccess{}, a.err
}

func (a groupFailAccess) Members(context.Context, int64) ([]int64, error) {
	return nil, a.err
}

type groupTestMedia struct {
	mu       sync.Mutex
	fail     bool
	commands []string
}

func (m *groupTestMedia) EnsureRoom(_ context.Context, r GroupRoom) (string, error) {
	return "RM_" + r.ID, nil
}
func (m *groupTestMedia) Token(_ GroupRoom, p RoomParticipant, now time.Time) (string, time.Time, error) {
	return "secret-" + p.Identity, now.Add(time.Minute), nil
}
func (m *groupTestMedia) URL() string { return "wss://media.example.test" }
func (m *groupTestMedia) Execute(_ context.Context, method string, _ []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail {
		return errors.New("media temporarily unavailable")
	}
	m.commands = append(m.commands, method)
	return nil
}
func (*groupTestMedia) VerifyWebhook(body []byte, _ string) (MediaWebhook, error) {
	var event MediaWebhook
	err := json.Unmarshal(body, &event)
	return event, err
}

type groupFixture struct {
	service   *Service
	store     *RedisStore
	client    *redis.Client
	access    groupTestAccess
	media     *groupTestMedia
	publisher *memoryPublisher
}

func newGroupFixture(t *testing.T, max int) *groupFixture {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	store := NewRedisStore(client, 45*time.Second, time.Hour)
	publisher := &memoryPublisher{}
	service := NewService(store, publisher, testICE{}, 45*time.Second)
	access := groupTestAccess{1: "owner", 2: "admin", 3: "member", 4: "member"}
	media := &groupTestMedia{}
	service.EnableGroupCalls(store, access, media, max, time.Hour)
	for user := range access {
		client.HSet(context.Background(), "ws_connection_mapping", fmt.Sprint(user), fmt.Sprintf("df-%d", user))
		client.Set(context.Background(), fmt.Sprintf("ws_route_lease:%d", user), fmt.Sprintf("df-%d|owner", user), time.Hour)
	}
	return &groupFixture{service, store, client, access, media, publisher}
}
func groupRequest(user int64, id string, request *callpb.ClientRequest) *callpb.InternalRequest {
	request.RequestId = id
	return &callpb.InternalRequest{UserId: user, FromKafkaTopic: fmt.Sprintf("df-%d", user), Request: request}
}
func (f *groupFixture) create(t *testing.T) GroupRoom {
	t.Helper()
	err := f.service.Handle(context.Background(), groupRequest(1, "create", &callpb.ClientRequest{Payload: &callpb.ClientRequest_CreateGroupCall{CreateGroupCall: &callpb.CreateGroupCall{GroupId: 10, CallType: callpb.CallType_VIDEO, InviteUserIds: []int64{2}}}}))
	if err != nil {
		t.Fatal(err)
	}
	id, err := f.store.GroupRoomID(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	room, err := f.store.GetRoom(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return room
}
func (f *groupFixture) join(t *testing.T, user int64, id, requestID string) GroupRoom {
	t.Helper()
	err := f.service.Handle(context.Background(), groupRequest(user, requestID, &callpb.ClientRequest{Payload: &callpb.ClientRequest_JoinGroupCall{JoinGroupCall: &callpb.JoinGroupCall{CallId: id}}}))
	if err != nil {
		t.Fatal(err)
	}
	room, err := f.store.GetRoom(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return room
}
func (f *groupFixture) webhook(t *testing.T, id, event string, room GroupRoom, identity, sid string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"id": id, "event": event, "room": map[string]string{"name": room.Name(), "sid": room.MediaSID}, "participant": map[string]string{"identity": identity, "sid": sid}})
	if err := f.service.HandleMediaWebhook(context.Background(), body, ""); err != nil {
		t.Fatal(err)
	}
}
func (f *groupFixture) deliveries(t *testing.T) []*callpb.Delivery {
	t.Helper()
	messages, err := f.client.XRange(context.Background(), callOutboxStream, "-", "+").Result()
	if err != nil {
		t.Fatal(err)
	}
	var deliveries []*callpb.Delivery
	for _, message := range messages {
		if strings.HasPrefix(streamString(message.Values["topic"]), mediaTopicPrefix) {
			continue
		}
		var env envelope.Envelope
		if err := proto.Unmarshal(streamBytes(message.Values["payload"]), &env); err != nil {
			t.Fatal(err)
		}
		if env.Type == envelope.MessageType_CALL_RESPONSE {
			var d callpb.Delivery
			if err := proto.Unmarshal(env.Payload, &d); err != nil {
				t.Fatal(err)
			}
			deliveries = append(deliveries, &d)
		}
	}
	return deliveries
}

func TestGroupCallLifecycleCredentialsAndBusy(t *testing.T) {
	f := newGroupFixture(t, 3)
	ctx := context.Background()
	room := f.create(t)
	// Creation does not auto-join. Clients explicitly join after CREATED.
	if len(room.Participants) != 0 {
		t.Fatal("creation auto-joined")
	}
	messages, _ := f.client.XRange(ctx, callOutboxStream, "-", "+").Result()
	pushes := 0
	for _, m := range messages {
		if streamString(m.Values["topic"]) == voipPushTopic() {
			var env envelope.Envelope
			_ = proto.Unmarshal(streamBytes(m.Values["payload"]), &env)
			var p pushpb.RequestMessage
			_ = proto.Unmarshal(env.Payload, &p)
			if p.GetVoipCall().GetGroupId() != 10 || p.GetVoipCall().GetRequired() {
				t.Fatal("group invitation changed required semantics")
			}
			pushes++
		}
	}
	if pushes != 1 {
		t.Fatal("missing VoIP invitation")
	}
	room = f.join(t, 1, room.ID, "join1")
	room = f.join(t, 2, room.ID, "join2")
	room = f.join(t, 3, room.ID, "join3")
	for _, d := range f.deliveries(t) {
		if d.Event.JoinToken != "" && (d.Event.EventType != callpb.CallEventType_GROUP_CALL_JOINED || d.Event.RequestId == "") {
			t.Fatal("credential leaked into broadcast")
		}
		if d.Event.JoinToken != "" {
			matched := false
			for _, p := range d.Event.GroupCall.Participants {
				if p.UserId == d.TargetUserId && d.Event.JoinToken == "secret-"+p.Identity {
					matched = true
				}
			}
			if !matched {
				t.Fatal("credential delivered to another user")
			}
		}
	}
	f.join(t, 4, room.ID, "full")
	if got := f.publisher.deliveries[len(f.publisher.deliveries)-1].delivery.Event.ErrorCode; got != callpb.CallErrorCode_ROOM_FULL {
		t.Fatalf("full: %v", got)
	}
	if got, _ := f.client.Get(ctx, userCallKey(1)).Result(); got != room.ID {
		t.Fatal("busy index missing")
	}
	if err := f.service.Handle(testCallContext("private-during-group"), initiateRequest(1, 4, "df-1")); err != nil {
		t.Fatal(err)
	}
	if got := f.publisher.deliveries[len(f.publisher.deliveries)-1].delivery.Event.ErrorCode; got != callpb.CallErrorCode_USER_BUSY {
		t.Fatalf("private call not busy: %v", got)
	}
	if err := f.service.Handle(ctx, groupRequest(2, "end", &callpb.ClientRequest{Payload: &callpb.ClientRequest_EndGroupCall{EndGroupCall: &callpb.EndGroupCall{CallId: room.ID}}})); err != nil {
		t.Fatal(err)
	}
	room, _ = f.store.GetRoom(ctx, room.ID)
	if room.State != StateEnded {
		t.Fatal("not ended")
	}
	for _, id := range []int64{1, 2, 3} {
		if f.client.Exists(ctx, userCallKey(id)).Val() != 0 {
			t.Fatal("busy index not freed")
		}
	}
	errorCount := len(f.publisher.deliveries)
	if err := f.service.Handle(testCallContext("private-after-group"), initiateRequest(1, 4, "df-1")); err != nil {
		t.Fatal(err)
	}
	if len(f.publisher.deliveries) != errorCount || f.client.Get(ctx, userCallKey(1)).Val() == room.ID {
		t.Fatal("private call blocked after end")
	}
}

func TestGroupCallPermissionAndStableRequestDedup(t *testing.T) {
	f := newGroupFixture(t, 4)
	ctx := context.Background()
	room := f.create(t)
	before := f.client.XLen(ctx, callOutboxStream).Val()
	f.create(t)
	if f.client.XLen(ctx, callOutboxStream).Val() != before {
		t.Fatal("create replay duplicated events")
	}
	f.join(t, 1, room.ID, "join")
	before = f.client.XLen(ctx, callOutboxStream).Val()
	f.join(t, 1, room.ID, "join")
	if f.client.XLen(ctx, callOutboxStream).Val() != before {
		t.Fatal("join replay duplicated events")
	}
	requests := []*callpb.InternalRequest{
		groupRequest(9, "outsider", &callpb.ClientRequest{Payload: &callpb.ClientRequest_JoinGroupCall{JoinGroupCall: &callpb.JoinGroupCall{CallId: room.ID}}}),
		groupRequest(3, "member-end", &callpb.ClientRequest{Payload: &callpb.ClientRequest_EndGroupCall{EndGroupCall: &callpb.EndGroupCall{CallId: room.ID}}}),
		groupRequest(2, "admin-remove-owner", &callpb.ClientRequest{Payload: &callpb.ClientRequest_RemoveGroupCallParticipant{RemoveGroupCallParticipant: &callpb.RemoveGroupCallParticipant{CallId: room.ID, TargetUserId: 1}}}),
	}
	for _, req := range requests {
		if err := f.service.Handle(ctx, req); err != nil {
			t.Fatal(err)
		}
		if f.publisher.deliveries[len(f.publisher.deliveries)-1].delivery.Event.ErrorCode != callpb.CallErrorCode_FORBIDDEN {
			t.Fatal("permission not enforced")
		}
	}
	delete(f.access, 1)
	if err := f.service.Handle(ctx, groupRequest(1, "removed", &callpb.ClientRequest{Payload: &callpb.ClientRequest_GetGroupCall{GetGroupCall: &callpb.GetGroupCall{CallId: room.ID}}})); err != nil {
		t.Fatal(err)
	}
	if f.publisher.deliveries[len(f.publisher.deliveries)-1].delivery.Event.ErrorCode != callpb.CallErrorCode_FORBIDDEN {
		t.Fatal("removed member queried room")
	}
}

func TestGroupWebhookSIDFencingLeaveAndTimeout(t *testing.T) {
	f := newGroupFixture(t, 4)
	ctx := context.Background()
	room := f.create(t)
	room = f.join(t, 1, room.ID, "join1")
	room = f.join(t, 2, room.ID, "join2")
	p, _ := room.Participant(1)
	f.webhook(t, "joined", "participant_joined", room, p.Identity, "PA_old")
	f.webhook(t, "reconnected", "participant_joined", room, p.Identity, "PA_new")
	f.webhook(t, "stale-left", "participant_left", room, p.Identity, "PA_old")
	room, _ = f.store.GetRoom(ctx, room.ID)
	p, _ = room.Participant(1)
	if p.SID != "PA_new" || !p.Connected {
		t.Fatal("stale leave removed reconnect")
	}
	before := f.client.XLen(ctx, callOutboxStream).Val()
	f.webhook(t, "reconnected", "participant_joined", room, p.Identity, "PA_new")
	if f.client.XLen(ctx, callOutboxStream).Val() != before {
		t.Fatal("webhook replay duplicated events")
	}
	f.service.now = func() time.Time { return room.CreatedAt.Add(76 * time.Second) }
	if err := f.service.SweepExpired(ctx); err != nil {
		t.Fatal(err)
	}
	room, _ = f.store.GetRoom(ctx, room.ID)
	if len(room.Participants) != 1 || room.Participants[0].UserID != 1 || room.State != StateActive {
		t.Fatal("pending join did not expire independently")
	}
	f.webhook(t, "current-left", "participant_left", room, p.Identity, "PA_new")
	room, _ = f.store.GetRoom(ctx, room.ID)
	if room.State != StateEnded || f.client.Exists(ctx, userCallKey(1)).Val() != 0 {
		t.Fatal("last leave did not end/release")
	}
}

func TestGroupStoreConcurrentCapacityAndOwnerFencing(t *testing.T) {
	f := newGroupFixture(t, 2)
	room := f.create(t)
	room = f.join(t, 1, room.ID, "join1")
	ctx := context.Background()
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, user := range []int64{2, 3} {
		wg.Add(1)
		go func(user int64) {
			defer wg.Done()
			updated := cloneRoom(room)
			updated.Revision++
			updated.Participants = append(updated.Participants, RoomParticipant{UserID: user, Identity: fmt.Sprint(user), JoinDeadline: time.Now().Add(time.Minute)})
			_, err := f.store.CommitRoom(ctx, &room, updated, fmt.Sprint("race-", user), nil)
			results <- err
		}(user)
	}
	wg.Wait()
	close(results)
	success, conflicts := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrRoomConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflicts != 1 {
		t.Fatalf("concurrent commits: %d/%d", success, conflicts)
	}
	current, _ := f.store.GetRoom(ctx, room.ID)
	ended := cloneRoom(current)
	ended.State = StateEnded
	if err := f.client.Set(ctx, userCallKey(1), "new-owner", time.Hour).Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.CommitRoom(ctx, &current, ended, "end-fenced", nil); err != nil {
		t.Fatal(err)
	}
	if f.client.Get(ctx, userCallKey(1)).Val() != "new-owner" {
		t.Fatal("terminal cleanup deleted new owner")
	}
}

func TestGroupMediaRelayRetriesWithoutKafkaTopic(t *testing.T) {
	f := newGroupFixture(t, 3)
	ctx := context.Background()
	f.client.XGroupCreateMkStream(ctx, callOutboxStream, callOutboxGroup, "0")
	event := mediaEvent("operation", "DeleteRoom", "bf2-group-"+strings.Repeat("a", 32), "")
	f.client.XAdd(ctx, &redis.XAddArgs{Stream: callOutboxStream, Values: map[string]any{"event_id": event.EventID, "operation_key": event.OperationKey, "topic": event.Topic, "payload": event.Payload}})
	streams, err := f.client.XReadGroup(ctx, &redis.XReadGroupArgs{Group: callOutboxGroup, Consumer: "worker", Streams: []string{callOutboxStream, ">"}, Count: 1}).Result()
	if err != nil {
		t.Fatal(err)
	}
	relay := NewEventRelay(f.client, func(context.Context, string, []byte, []sarama.RecordHeader) error {
		t.Fatal("SFU command was published to Kafka")
		return nil
	})
	relay.SetMediaServer(f.media)
	f.media.fail = true
	if err := relay.process(ctx, streams[0].Messages); err == nil {
		t.Fatal("failed command was acknowledged")
	}
	if f.client.XPending(ctx, callOutboxStream, callOutboxGroup).Val().Count != 1 {
		t.Fatal("command not pending")
	}
	f.media.fail = false
	if err := relay.process(ctx, streams[0].Messages); err != nil {
		t.Fatal(err)
	}
	if len(f.media.commands) != 1 || f.client.XPending(ctx, callOutboxStream, callOutboxGroup).Val().Count != 0 {
		t.Fatal("command did not recover")
	}
	if err := relay.process(ctx, streams[0].Messages); err != nil {
		t.Fatal(err)
	}
	if len(f.media.commands) != 1 {
		t.Fatal("sent ledger ignored")
	}
}

func TestGroupProtocolAppendOnlyAndLegacyRequest(t *testing.T) {
	// Existing empty get_config oneof (field 1) remains wire-compatible.
	var old callpb.ClientRequest
	if err := proto.Unmarshal([]byte{0x0a, 0x00}, &old); err != nil || old.GetGetConfig() == nil || old.RequestId != "" {
		t.Fatal("legacy request changed")
	}
	request := &callpb.ClientRequest{RequestId: "request", Payload: &callpb.ClientRequest_CreateGroupCall{CreateGroupCall: &callpb.CreateGroupCall{GroupId: 10, CallType: callpb.CallType_VIDEO, InviteUserIds: []int64{2, 3}}}}
	bytes, err := proto.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var got callpb.ClientRequest
	if err := proto.Unmarshal(bytes, &got); err != nil || !proto.Equal(request, &got) {
		t.Fatal("group protocol roundtrip failed")
	}
	f := newGroupFixture(t, 2)
	if err := f.service.Handle(context.Background(), &callpb.InternalRequest{UserId: 1, FromKafkaTopic: "df-1", Request: &old}); err != nil {
		t.Fatal(err)
	}
	if !f.publisher.deliveries[0].delivery.Event.GroupCallsAvailable {
		t.Fatal("feature capability absent")
	}
}

func TestGroupNewAdmissionIgnoresOldJoinAndLeaveBeforeReconnect(t *testing.T) {
	f := newGroupFixture(t, 3)
	ctx := context.Background()
	room := f.create(t)
	room = f.join(t, 1, room.ID, "join-old")
	old, _ := room.Participant(1)
	f.webhook(t, "connected-old", "participant_joined", room, old.Identity, "PA_old")
	room = f.join(t, 1, room.ID, "join-new")
	current, _ := room.Participant(1)
	if old.Identity == current.Identity || current.Connected || current.SID != "" {
		t.Fatal("new admission did not fence old connection")
	}
	f.webhook(t, "late-old-join", "participant_joined", room, old.Identity, "PA_old")
	f.webhook(t, "old-leaves-first", "participant_left", room, old.Identity, "PA_old")
	room, _ = f.store.GetRoom(ctx, room.ID)
	if room.State != StateActive || len(room.Participants) != 1 || room.Participants[0].Identity != current.Identity {
		t.Fatal("old callbacks removed new reservation")
	}
	f.webhook(t, "connected-new", "participant_joined", room, current.Identity, "PA_new")
	room, _ = f.store.GetRoom(ctx, room.ID)
	if !room.Participants[0].Connected || room.Participants[0].SID != "PA_new" {
		t.Fatal("new admission did not connect")
	}
}

func TestGroupInitialTimeoutAndTransientFailureDoNotCacheSuccess(t *testing.T) {
	f := newGroupFixture(t, 3)
	ctx := context.Background()
	injected := errors.New("database temporarily unavailable")
	f.service.groups.access = groupFailAccess{injected}
	req := groupRequest(1, "create", &callpb.ClientRequest{Payload: &callpb.ClientRequest_CreateGroupCall{CreateGroupCall: &callpb.CreateGroupCall{GroupId: 10, CallType: callpb.CallType_VIDEO}}})
	if err := f.service.Handle(ctx, req); !errors.Is(err, injected) {
		t.Fatal("transient database failure acknowledged", err)
	}
	if done, _ := f.store.OperationCompleted(ctx, groupOperation(req)); done || f.client.XLen(ctx, callOutboxStream).Val() != 0 || len(f.publisher.deliveries) != 0 {
		t.Fatal("failed operation persisted/cached a response")
	}
	f.service.groups.access = f.access
	room := f.create(t)
	f.service.now = func() time.Time { return room.CreatedAt.Add(76 * time.Second) }
	if err := f.service.SweepExpired(ctx); err != nil {
		t.Fatal(err)
	}
	room, _ = f.store.GetRoom(ctx, room.ID)
	if room.State != StateEnded || f.client.Exists(ctx, groupRoomKey(10)).Val() != 0 {
		t.Fatal("empty initial room did not expire")
	}
}

func TestGroupLeaveAndAdminRemovalFreeOnlyTheirOwnBusyKeys(t *testing.T) {
	f := newGroupFixture(t, 4)
	ctx := context.Background()
	room := f.create(t)
	room = f.join(t, 1, room.ID, "join1")
	room = f.join(t, 2, room.ID, "join2")
	room = f.join(t, 3, room.ID, "join3")
	if err := f.service.Handle(ctx, groupRequest(2, "remove-member", &callpb.ClientRequest{Payload: &callpb.ClientRequest_RemoveGroupCallParticipant{RemoveGroupCallParticipant: &callpb.RemoveGroupCallParticipant{CallId: room.ID, TargetUserId: 3}}})); err != nil {
		t.Fatal(err)
	}
	if f.client.Exists(ctx, userCallKey(3)).Val() != 0 || f.client.Get(ctx, userCallKey(1)).Val() != room.ID {
		t.Fatal("removal cleared wrong busy ownership")
	}
	for _, user := range []int64{1, 2} {
		if err := f.service.Handle(ctx, groupRequest(user, fmt.Sprint("leave", user), &callpb.ClientRequest{Payload: &callpb.ClientRequest_LeaveGroupCall{LeaveGroupCall: &callpb.LeaveGroupCall{CallId: room.ID}}})); err != nil {
			t.Fatal(err)
		}
		current, _ := f.store.GetRoom(ctx, room.ID)
		if user == 1 && current.State != StateActive {
			t.Fatal("creator leave ended remaining participants")
		}
		if user == 2 && current.State != StateEnded {
			t.Fatal("last leave did not end")
		}
	}
}

func TestGroupExpiredMetadataStillDurablyDeletesMediaRoom(t *testing.T) {
	f := newGroupFixture(t, 4)
	ctx := context.Background()
	room := f.create(t)
	// Simulate metadata expiration during downtime, keeping the deadline index.
	f.client.Del(ctx, roomKey(room.ID))
	f.service.now = func() time.Time { return room.ExpiresAt.Add(time.Minute) }
	before := f.client.XLen(ctx, callOutboxStream).Val()
	if err := f.service.SweepExpired(ctx); err != nil {
		t.Fatal(err)
	}
	if f.client.ZCard(ctx, groupDeadlinesKey).Val() != 0 || f.client.XLen(ctx, callOutboxStream).Val() != before+1 {
		t.Fatal("expired metadata dropped SFU cleanup")
	}
	if err := f.service.SweepExpired(ctx); err != nil {
		t.Fatal(err)
	}
	if f.client.XLen(ctx, callOutboxStream).Val() != before+1 {
		t.Fatal("expired cleanup duplicated logical command")
	}
}

func TestGroupDifferentMediaRoomSIDCannotAdmitOrEndCurrentRoom(t *testing.T) {
	f := newGroupFixture(t, 3)
	ctx := context.Background()
	room := f.create(t)
	room = f.join(t, 1, room.ID, "join")
	p, _ := room.Participant(1)
	other := cloneRoom(room)
	other.MediaSID = "RM_other"
	f.webhook(t, "other-room-joined", "participant_joined", other, p.Identity, "PA_other")
	f.webhook(t, "other-room-finished", "room_finished", other, "", "")
	current, _ := f.store.GetRoom(ctx, room.ID)
	if current.State != StateActive || current.Participants[0].Connected || len(f.media.commands) != 1 || f.media.commands[0] != "RemoveParticipant" {
		t.Fatal("foreign media room bypassed admission or terminated current room")
	}
}
