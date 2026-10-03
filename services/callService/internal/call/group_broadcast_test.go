package call

import (
	"context"
	"errors"
	"testing"
	"time"

	callpb "Betterfly2/proto/call"
	envelope "Betterfly2/proto/envelope"
	pushpb "Betterfly2/proto/push"
	"github.com/IBM/sarama"
	"github.com/redis/go-redis/v9"
	"google.golang.org/protobuf/proto"
)

func assertGroupStatusBroadcast(t *testing.T, f *groupFixture, deliveries []*callpb.Delivery, room GroupRoom, users ...int64) {
	t.Helper()
	kind := callpb.CallEventType_GROUP_CALL_UPDATED
	if room.State == StateEnded {
		kind = callpb.CallEventType_GROUP_CALL_ENDED
	}
	want := f.service.groupEvent(kind, room, "").GetGroupCall()
	counts := make(map[int64]int)
	for _, d := range deliveries {
		event := d.GetEvent()
		if event.GetEventType() != callpb.CallEventType_GROUP_CALL_JOINED && (event.GetJoinToken() != "" || event.GetSfuUrl() != "" || event.GetTokenExpiresAt() != "") {
			t.Fatal("room credential leaked into a non-JOINED event")
		}
		if event.GetRequestId() != "" || event.GetEventType() != callpb.CallEventType_GROUP_CALL_UPDATED && event.GetEventType() != callpb.CallEventType_GROUP_CALL_ENDED {
			continue
		}
		if event.GetEventType() != kind || !proto.Equal(event.GetGroupCall(), want) {
			t.Fatalf("broadcast is not the latest canonical room: user=%d event=%v revision=%d", d.GetTargetUserId(), event.GetEventType(), event.GetGroupCall().GetRevision())
		}
		counts[d.GetTargetUserId()]++
	}
	if len(counts) != len(users) {
		t.Fatalf("broadcast recipients: got=%v want=%v", counts, users)
	}
	for _, user := range users {
		if counts[user] != 1 {
			t.Fatalf("user %d did not receive exactly one state broadcast: %v", user, counts)
		}
	}
}

func assertGroupReply(t *testing.T, deliveries []*callpb.Delivery, user int64, kind callpb.CallEventType, requestID string) *callpb.CallEvent {
	t.Helper()
	var reply *callpb.CallEvent
	for _, d := range deliveries {
		if d.GetEvent().GetRequestId() == requestID {
			if reply != nil || d.GetTargetUserId() != user || d.GetEvent().GetEventType() != kind {
				t.Fatalf("operation reply duplicated or sent to wrong user/type: %v", d.GetEvent().GetEventType())
			}
			reply = d.GetEvent()
		}
	}
	if reply == nil {
		t.Fatal("correlated operation reply missing")
	}
	return reply
}

func TestGroupCallBroadcastIncludesNonparticipantsWithoutInvitingThem(t *testing.T) {
	f := newGroupFixture(t, 4)
	ctx := context.Background()
	f.client.HSet(ctx, "ws_connection_mapping", "9", "df-9")
	f.client.Set(ctx, "ws_route_lease:9", "df-9|owner", time.Hour)
	room := f.create(t)
	deliveries := f.deliveries(t)
	assertGroupReply(t, deliveries, 1, callpb.CallEventType_GROUP_CALL_CREATED, "create")
	assertGroupStatusBroadcast(t, f, deliveries, room, 2, 3, 4)
	invitations := 0
	for _, d := range deliveries {
		if d.GetTargetUserId() == 9 {
			t.Fatal("nonmember received a room event")
		}
		if d.GetEvent().GetEventType() == callpb.CallEventType_GROUP_CALL_INVITATION {
			if d.GetTargetUserId() != 2 || d.GetEvent().GetRequestId() != "" {
				t.Fatal("non-invitee rang or invitation carried a request ID")
			}
			invitations++
		}
	}
	if invitations != 1 {
		t.Fatal("explicit invitation missing or duplicated")
	}
	before := len(deliveries)
	room = f.join(t, 1, room.ID, "join1")
	deliveries = f.deliveries(t)[before:]
	joined := assertGroupReply(t, deliveries, 1, callpb.CallEventType_GROUP_CALL_JOINED, "join1")
	if joined.GetJoinToken() == "" || joined.GetSfuUrl() == "" || joined.GetTokenExpiresAt() == "" {
		t.Fatal("requester did not receive SFU credentials")
	}
	assertGroupStatusBroadcast(t, f, deliveries, room, 2, 3, 4)
	for _, d := range deliveries {
		if d.GetEvent().GetEventType() == callpb.CallEventType_GROUP_CALL_JOINED && d.GetTargetUserId() != 1 {
			t.Fatal("JOINED delivered to a nonrequester")
		}
	}
	before = len(f.deliveries(t))
	room = f.join(t, 2, room.ID, "join2")
	deliveries = f.deliveries(t)[before:]
	assertGroupReply(t, deliveries, 2, callpb.CallEventType_GROUP_CALL_JOINED, "join2")
	assertGroupStatusBroadcast(t, f, deliveries, room, 1, 3, 4)
	streamLength := f.client.XLen(ctx, callOutboxStream).Val()
	f.join(t, 2, room.ID, "join2")
	if f.client.XLen(ctx, callOutboxStream).Val() != streamLength {
		t.Fatal("Join retry duplicated logical broadcasts")
	}
	pushes := 0
	for _, message := range f.client.XRange(ctx, callOutboxStream, "-", "+").Val() {
		if message.Values["topic"] != voipPushTopic() {
			continue
		}
		var env envelope.Envelope
		var push pushpb.RequestMessage
		if err := proto.Unmarshal(streamBytes(message.Values["payload"]), &env); err != nil {
			t.Fatal(err)
		}
		if env.GetType() != envelope.MessageType_PUSH_REQUEST {
			t.Fatal("invitation is not a push request")
		}
		if err := proto.Unmarshal(env.GetPayload(), &push); err != nil {
			t.Fatal(err)
		}
		if push.GetVoipCall() == nil || push.GetVoipCall().GetCalleeUserId() != 2 || push.GetVoipCall().GetRequired() {
			t.Fatal("state change generated a push for a non-invitee")
		}
		pushes++
	}
	if pushes != 1 {
		t.Fatal("status changes generated additional pushes")
	}
}

func TestGroupRemovalAndLastLeaveBroadcastWithoutLeavingTheOperator(t *testing.T) {
	f := newGroupFixture(t, 4)
	ctx := context.Background()
	room := f.create(t)
	room = f.join(t, 1, room.ID, "join1")
	room = f.join(t, 3, room.ID, "join3")
	before := len(f.deliveries(t))
	remove := groupRequest(2, "remove3", &callpb.ClientRequest{Payload: &callpb.ClientRequest_RemoveGroupCallParticipant{RemoveGroupCallParticipant: &callpb.RemoveGroupCallParticipant{CallId: room.ID, TargetUserId: 3}}})
	if err := f.service.Handle(ctx, remove); err != nil {
		t.Fatal(err)
	}
	room, _ = f.store.GetRoom(ctx, room.ID)
	deliveries := f.deliveries(t)[before:]
	assertGroupReply(t, deliveries, 2, callpb.CallEventType_GROUP_CALL_UPDATED, "remove3")
	assertGroupStatusBroadcast(t, f, deliveries, room, 1, 3, 4)
	left := 0
	for _, d := range deliveries {
		if d.GetEvent().GetEventType() == callpb.CallEventType_GROUP_CALL_LEFT {
			if d.GetTargetUserId() != 3 || d.GetEvent().GetRequestId() != "" {
				t.Fatal("LEFT was broadcast or incorrectly sent to the moderator")
			}
			left++
		}
	}
	if left != 1 {
		t.Fatal("removed participant did not receive exactly one LEFT")
	}
	before = len(f.deliveries(t))
	leave := groupRequest(1, "last-leave", &callpb.ClientRequest{Payload: &callpb.ClientRequest_LeaveGroupCall{LeaveGroupCall: &callpb.LeaveGroupCall{CallId: room.ID}}})
	if err := f.service.Handle(ctx, leave); err != nil {
		t.Fatal(err)
	}
	room, _ = f.store.GetRoom(ctx, room.ID)
	if room.State != StateEnded {
		t.Fatal("last participant did not end the room")
	}
	deliveries = f.deliveries(t)[before:]
	assertGroupReply(t, deliveries, 1, callpb.CallEventType_GROUP_CALL_LEFT, "last-leave")
	assertGroupStatusBroadcast(t, f, deliveries, room, 1, 2, 3, 4)
	for _, d := range deliveries {
		if d.GetEvent().GetEventType() == callpb.CallEventType_GROUP_CALL_LEFT && d.GetTargetUserId() != 1 {
			t.Fatal("last-leaver LEFT was broadcast to the group")
		}
	}
}

func TestGroupRemovingLastParticipantEndsForObserversAndModerator(t *testing.T) {
	f := newGroupFixture(t, 4)
	ctx := context.Background()
	room := f.create(t)
	room = f.join(t, 3, room.ID, "join3")
	before := len(f.deliveries(t))
	request := groupRequest(1, "remove-last", &callpb.ClientRequest{Payload: &callpb.ClientRequest_RemoveGroupCallParticipant{RemoveGroupCallParticipant: &callpb.RemoveGroupCallParticipant{CallId: room.ID, TargetUserId: 3}}})
	if err := f.service.Handle(ctx, request); err != nil {
		t.Fatal(err)
	}
	room, _ = f.store.GetRoom(ctx, room.ID)
	if room.State != StateEnded {
		t.Fatal("removing the last participant did not end the room")
	}
	deliveries := f.deliveries(t)[before:]
	assertGroupReply(t, deliveries, 1, callpb.CallEventType_GROUP_CALL_ENDED, "remove-last")
	assertGroupStatusBroadcast(t, f, deliveries, room, 2, 3, 4)
	left := 0
	for _, d := range deliveries {
		if d.GetEvent().GetEventType() == callpb.CallEventType_GROUP_CALL_LEFT {
			if d.GetTargetUserId() != 3 || d.GetEvent().GetRequestId() != "" {
				t.Fatal("LEFT sent to someone other than the removed participant")
			}
			left++
		}
	}
	if left != 1 {
		t.Fatal("removed participant's LEFT missing or duplicated")
	}
}

func TestGroupWebhookBroadcastsConnectionChangesAndEndToAllMembers(t *testing.T) {
	f := newGroupFixture(t, 4)
	ctx := context.Background()
	room := f.create(t)
	room = f.join(t, 1, room.ID, "join1")
	room = f.join(t, 2, room.ID, "join2")
	p1, _ := room.Participant(1)
	p2, _ := room.Participant(2)
	for _, step := range []struct{ id, event, identity, sid string }{
		{"connected1", "participant_joined", p1.Identity, "PA_1"},
		{"connected2", "participant_joined", p2.Identity, "PA_2"},
		{"reconnected1", "participant_joined", p1.Identity, "PA_1_new"},
		{"disconnected1", "participant_left", p1.Identity, "PA_1_new"},
		{"disconnected2", "participant_left", p2.Identity, "PA_2"},
	} {
		t.Run(step.id, func(t *testing.T) {
			before := len(f.deliveries(t))
			revision := room.Revision
			f.webhook(t, step.id, step.event, room, step.identity, step.sid)
			room, _ = f.store.GetRoom(ctx, room.ID)
			if room.Revision != revision+1 {
				t.Fatal("webhook did not advance the room revision")
			}
			assertGroupStatusBroadcast(t, f, f.deliveries(t)[before:], room, 1, 2, 3, 4)
			length := f.client.XLen(ctx, callOutboxStream).Val()
			f.webhook(t, step.id, step.event, room, step.identity, step.sid)
			f.webhook(t, step.id+"-duplicate", step.event, room, step.identity, step.sid)
			if f.client.XLen(ctx, callOutboxStream).Val() != length {
				t.Fatal("duplicate/no-op webhook generated another broadcast")
			}
		})
	}
	if room.State != StateEnded {
		t.Fatal("last SFU participant leaving did not end the room")
	}
}

func TestGroupEndPathsBroadcastToNonparticipants(t *testing.T) {
	for _, path := range []string{"explicit", "room_finished", "empty_timeout", "pending_timeout", "room_timeout"} {
		t.Run(path, func(t *testing.T) {
			f := newGroupFixture(t, 4)
			ctx := context.Background()
			room := f.create(t)
			if path != "empty_timeout" {
				room = f.join(t, 1, room.ID, "join1")
			}
			if path == "pending_timeout" {
				p, _ := room.Participant(1)
				f.webhook(t, "connected", "participant_joined", room, p.Identity, "PA_1")
				room = f.join(t, 2, room.ID, "join2")
			}
			before := len(f.deliveries(t))
			switch path {
			case "explicit":
				if err := f.service.Handle(ctx, groupRequest(2, "end", &callpb.ClientRequest{Payload: &callpb.ClientRequest_EndGroupCall{EndGroupCall: &callpb.EndGroupCall{CallId: room.ID}}})); err != nil {
					t.Fatal(err)
				}
			case "room_finished":
				f.webhook(t, "finished", "room_finished", room, "", "")
			case "room_timeout":
				f.service.now = func() time.Time { return room.ExpiresAt.Add(time.Second) }
			default:
				f.service.now = func() time.Time { return room.CreatedAt.Add(76 * time.Second) }
			}
			if err := f.service.SweepExpired(ctx); err != nil {
				t.Fatal(err)
			}
			room, _ = f.store.GetRoom(ctx, room.ID)
			deliveries := f.deliveries(t)[before:]
			if path == "explicit" {
				assertGroupReply(t, deliveries, 2, callpb.CallEventType_GROUP_CALL_ENDED, "end")
				assertGroupStatusBroadcast(t, f, deliveries, room, 1, 3, 4)
			} else {
				assertGroupStatusBroadcast(t, f, deliveries, room, 1, 2, 3, 4)
			}
			if path == "pending_timeout" {
				if room.State != StateActive || len(room.Participants) != 1 || room.Participants[0].UserID != 1 {
					t.Fatal("admission timeout ended the connected participant's room")
				}
			} else if room.State != StateEnded {
				t.Fatal("terminal path did not end the room")
			}
			length := f.client.XLen(ctx, callOutboxStream).Val()
			if err := f.service.SweepExpired(ctx); err != nil {
				t.Fatal(err)
			}
			if f.client.XLen(ctx, callOutboxStream).Val() != length {
				t.Fatal("repeat sweep duplicated logical broadcasts")
			}
		})
	}
}

type groupRouteFailureStore struct {
	Store
	user int64
}

func (s groupRouteFailureStore) UserTopic(ctx context.Context, user int64) (string, error) {
	if user == s.user {
		return "", errors.New("route lookup temporarily unavailable")
	}
	return s.Store.UserTopic(ctx, user)
}

func TestGroupBroadcastDeliveryFailureDoesNotRollbackAndGetRecovers(t *testing.T) {
	f := newGroupFixture(t, 4)
	ctx := context.Background()
	room := f.create(t)
	f.service.store = groupRouteFailureStore{Store: f.store, user: 2}
	f.client.Del(ctx, "ws_route_lease:3") // A stale mapping does not prove online status.
	before := len(f.deliveries(t))
	room = f.join(t, 1, room.ID, "join1")
	assertGroupStatusBroadcast(t, f, f.deliveries(t)[before:], room, 4)
	if len(f.publisher.deliveries) != 0 || room.Revision != 2 || len(room.Participants) != 1 {
		t.Fatal("offline/failed routes rolled back Join or generated CALL_ERROR")
	}
	if err := f.client.XGroupCreateMkStream(ctx, callOutboxStream, callOutboxGroup, "0").Err(); err != nil {
		t.Fatal(err)
	}
	streams, err := f.client.XReadGroup(ctx, &redis.XReadGroupArgs{Group: callOutboxGroup, Consumer: "worker", Streams: []string{callOutboxStream, ">"}, Count: 100}).Result()
	if err != nil {
		t.Fatal(err)
	}
	fail := errors.New("Kafka publish temporarily unavailable")
	relay := NewEventRelay(f.client, func(context.Context, string, []byte, []sarama.RecordHeader) error { return fail })
	if err := relay.process(ctx, streams[0].Messages); !errors.Is(err, fail) {
		t.Fatal("failed delivery was silently acknowledged", err)
	}
	current, err := f.store.GetRoom(ctx, room.ID)
	if err != nil || current.Revision != room.Revision || current.State != StateActive || f.client.Get(ctx, userCallKey(1)).Val() != room.ID {
		t.Fatal("relay failure rolled back room state or ownership")
	}
	length := f.client.XLen(ctx, callOutboxStream).Val()
	f.join(t, 1, room.ID, "join1")
	if f.client.XLen(ctx, callOutboxStream).Val() != length {
		t.Fatal("retry after delivery failure duplicated room events")
	}
	f.service.store = f.store
	f.client.Set(ctx, "ws_route_lease:3", "df-3|owner", time.Hour)
	if err := f.service.Handle(ctx, groupRequest(3, "reconnect-query", &callpb.ClientRequest{Payload: &callpb.ClientRequest_GetGroupCall{GetGroupCall: &callpb.GetGroupCall{GroupId: 10}}})); err != nil {
		t.Fatal(err)
	}
	recovered := f.publisher.deliveries[len(f.publisher.deliveries)-1].delivery
	if recovered.GetTargetUserId() != 3 || recovered.GetEvent().GetRequestId() != "reconnect-query" || !proto.Equal(recovered.GetEvent().GetGroupCall(), f.service.groupEvent(callpb.CallEventType_GROUP_CALL_UPDATED, room, "").GetGroupCall()) {
		t.Fatal("Get after reconnect did not recover the latest snapshot")
	}
}

func TestGroupBroadcastExcludesFormerMembers(t *testing.T) {
	f := newGroupFixture(t, 4)
	room := f.create(t)
	delete(f.access, 4) // Their WebSocket route remains valid, but membership no longer does.
	before := len(f.deliveries(t))
	room = f.join(t, 1, room.ID, "join1")
	assertGroupStatusBroadcast(t, f, f.deliveries(t)[before:], room, 2, 3)
}

type groupMembersFailure struct {
	groupTestAccess
	err error
}

func (a groupMembersFailure) Members(context.Context, int64) ([]int64, error) { return nil, a.err }

func TestGroupMembershipQueryFailureRetriesWithoutCommitting(t *testing.T) {
	f := newGroupFixture(t, 4)
	ctx := context.Background()
	room := f.create(t)
	fail := errors.New("membership database temporarily unavailable")
	f.service.groups.access = groupMembersFailure{groupTestAccess: f.access, err: fail}
	request := groupRequest(1, "join1", &callpb.ClientRequest{Payload: &callpb.ClientRequest_JoinGroupCall{JoinGroupCall: &callpb.JoinGroupCall{CallId: room.ID}}})
	length := f.client.XLen(ctx, callOutboxStream).Val()
	if err := f.service.Handle(ctx, request); !errors.Is(err, fail) {
		t.Fatal("membership lookup failure was acknowledged", err)
	}
	current, _ := f.store.GetRoom(ctx, room.ID)
	if current.Revision != room.Revision || f.client.XLen(ctx, callOutboxStream).Val() != length || f.client.Exists(ctx, callOperationKey(groupOperation(request))).Val() != 0 {
		t.Fatal("failed membership query partially committed")
	}
	f.service.groups.access = f.access
	before := len(f.deliveries(t))
	room = f.join(t, 1, room.ID, "join1")
	assertGroupReply(t, f.deliveries(t)[before:], 1, callpb.CallEventType_GROUP_CALL_JOINED, "join1")
	assertGroupStatusBroadcast(t, f, f.deliveries(t)[before:], room, 2, 3, 4)
}
