package consumer

import (
	pb "Betterfly2/proto/data_forwarding"
	friend "Betterfly2/proto/friend"
	storage "Betterfly2/proto/storage"
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/IBM/sarama"
)

type consumerTestSession struct {
	ctx    context.Context
	mu     sync.Mutex
	marked []*sarama.ConsumerMessage
}

func (s *consumerTestSession) Claims() map[string][]int32               { return nil }
func (s *consumerTestSession) MemberID() string                         { return "test" }
func (s *consumerTestSession) GenerationID() int32                      { return 1 }
func (s *consumerTestSession) MarkOffset(string, int32, int64, string)  {}
func (s *consumerTestSession) Commit()                                  {}
func (s *consumerTestSession) ResetOffset(string, int32, int64, string) {}
func (s *consumerTestSession) Context() context.Context                 { return s.ctx }
func (s *consumerTestSession) MarkMessage(msg *sarama.ConsumerMessage, _ string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.marked = append(s.marked, msg)
}

type consumerTestClaim struct {
	messages <-chan *sarama.ConsumerMessage
}

func (c *consumerTestClaim) Topic() string                            { return "test-topic" }
func (c *consumerTestClaim) Partition() int32                         { return 0 }
func (c *consumerTestClaim) InitialOffset() int64                     { return 0 }
func (c *consumerTestClaim) HighWaterMarkOffset() int64               { return 0 }
func (c *consumerTestClaim) Messages() <-chan *sarama.ConsumerMessage { return c.messages }

func newConsumerTestClaim(messages ...*sarama.ConsumerMessage) *consumerTestClaim {
	ch := make(chan *sarama.ConsumerMessage, len(messages))
	for _, msg := range messages {
		ch <- msg
	}
	close(ch)
	return &consumerTestClaim{messages: ch}
}

func newProcessingTestHandler(process func(*sarama.ConsumerMessage) error, dlq func(string, []byte, []sarama.RecordHeader) error) *NewKafkaConsumerGroupHandler {
	return &NewKafkaConsumerGroupHandler{
		retryConfig: consumerRetryConfig{
			maxRetries:     2,
			initialBackoff: time.Millisecond,
			maxBackoff:     2 * time.Millisecond,
			dlqTopic:       "test-dlq",
		},
		processMessageFn: process,
		publishDLQFn:     dlq,
	}
}

func TestConsumeClaimMarksOnlyAfterSuccessfulProcessing(t *testing.T) {
	msg := &sarama.ConsumerMessage{Topic: "source", Partition: 2, Offset: 10, Value: []byte("payload")}
	handler := newProcessingTestHandler(func(*sarama.ConsumerMessage) error { return nil }, func(string, []byte, []sarama.RecordHeader) error {
		t.Fatal("successful message must not enter DLQ")
		return nil
	})
	session := &consumerTestSession{ctx: context.Background()}

	if err := handler.ConsumeClaim(session, newConsumerTestClaim(msg)); err != nil {
		t.Fatal(err)
	}
	if len(session.marked) != 1 || session.marked[0] != msg {
		t.Fatalf("expected exactly one marked message, got %+v", session.marked)
	}
}

func TestConsumeClaimRetriesTransientFailureBeforeMarking(t *testing.T) {
	attempts := 0
	handler := newProcessingTestHandler(func(*sarama.ConsumerMessage) error {
		attempts++
		if attempts == 1 {
			return errors.New("temporary websocket route failure")
		}
		return nil
	}, func(string, []byte, []sarama.RecordHeader) error {
		t.Fatal("recovered message must not enter DLQ")
		return nil
	})
	session := &consumerTestSession{ctx: context.Background()}

	if err := handler.ConsumeClaim(session, newConsumerTestClaim(&sarama.ConsumerMessage{Topic: "source", Offset: 1})); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 || len(session.marked) != 1 {
		t.Fatalf("unexpected retry/mark result: attempts=%d marked=%d", attempts, len(session.marked))
	}
}

func TestConsumeClaimMarksAfterRetryExhaustionAndSuccessfulDLQ(t *testing.T) {
	attempts := 0
	var dlqPayload []byte
	var dlqHeaders []sarama.RecordHeader
	handler := newProcessingTestHandler(func(*sarama.ConsumerMessage) error {
		attempts++
		return errors.New("user route unavailable")
	}, func(topic string, payload []byte, headers []sarama.RecordHeader) error {
		if topic != "test-dlq" {
			t.Fatalf("unexpected DLQ topic: %s", topic)
		}
		dlqPayload = append([]byte(nil), payload...)
		dlqHeaders = headers
		return nil
	})
	msg := &sarama.ConsumerMessage{Topic: "source", Partition: 3, Offset: 42, Value: []byte("original")}
	session := &consumerTestSession{ctx: context.Background()}

	if err := handler.ConsumeClaim(session, newConsumerTestClaim(msg)); err != nil {
		t.Fatal(err)
	}
	if attempts != 3 || len(session.marked) != 1 {
		t.Fatalf("unexpected retry/mark result: attempts=%d marked=%d", attempts, len(session.marked))
	}
	headers := headerMap(dlqHeaders)
	if len(headers) != 10 || string(dlqPayload) != "original" || headers["schema_version"] != "1" || headers["original_topic"] != "source" || headers["original_partition"] != "3" || headers["original_offset"] != "42" || headers["retry_count"] != "2" || headers["error_class"] != string(failureTransient) || headers["first_failure_time"] == "" || headers["final_failure_time"] == "" || headers["sanitized_error_summary"] == "" {
		t.Fatalf("incomplete DLQ record: payload=%q headers=%+v", dlqPayload, headers)
	}
}

func TestDLQLargePayloadIsNotBase64Expanded(t *testing.T) {
	original := bytes.Repeat([]byte{0xab}, 1024*1024)
	var delivered []byte
	handler := newProcessingTestHandler(func(*sarama.ConsumerMessage) error {
		return permanentError("invalid large message")
	}, func(_ string, payload []byte, _ []sarama.RecordHeader) error {
		delivered = append([]byte(nil), payload...)
		return nil
	})
	session := &consumerTestSession{ctx: context.Background()}
	if err := handler.ConsumeClaim(session, newConsumerTestClaim(&sarama.ConsumerMessage{Topic: "source", Value: original})); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(delivered, original) || len(delivered) != len(original) {
		t.Fatalf("DLQ payload was transformed: original=%d delivered=%d", len(original), len(delivered))
	}
}

func headerMap(headers []sarama.RecordHeader) map[string]string {
	result := make(map[string]string, len(headers))
	for _, header := range headers {
		result[string(header.Key)] = string(header.Value)
	}
	return result
}

func TestConsumeClaimDoesNotMarkWhenDLQPublishFailsOrSkipPartitionMessage(t *testing.T) {
	processedOffsets := make([]int64, 0, 2)
	handler := newProcessingTestHandler(func(msg *sarama.ConsumerMessage) error {
		processedOffsets = append(processedOffsets, msg.Offset)
		return permanentError("invalid payload")
	}, func(string, []byte, []sarama.RecordHeader) error { return errors.New("kafka unavailable") })
	first := &sarama.ConsumerMessage{Topic: "source", Partition: 0, Offset: 7}
	second := &sarama.ConsumerMessage{Topic: "source", Partition: 0, Offset: 8}
	session := &consumerTestSession{ctx: context.Background()}

	if err := handler.ConsumeClaim(session, newConsumerTestClaim(first, second)); err == nil {
		t.Fatal("expected DLQ failure")
	}
	if len(session.marked) != 0 {
		t.Fatalf("DLQ failure unexpectedly marked offsets: %+v", session.marked)
	}
	if len(processedOffsets) != 1 || processedOffsets[0] != 7 {
		t.Fatalf("consumer crossed failed partition message: %v", processedOffsets)
	}
}

func TestConsumeClaimWritesMalformedProtobufToDLQ(t *testing.T) {
	dlqCalls := 0
	handler := newProcessingTestHandler(nil, func(_ string, payload []byte, headers []sarama.RecordHeader) error {
		dlqCalls++
		metadata := headerMap(headers)
		if metadata["error_class"] != string(failurePermanent) || metadata["retry_count"] != "0" {
			t.Fatalf("malformed protobuf was not classified as permanent: %+v", metadata)
		}
		if string(payload) != string([]byte{0xff, 0xff}) {
			t.Fatalf("DLQ payload changed: %v", payload)
		}
		return nil
	})
	handler.processMessageFn = handler.processMessage
	session := &consumerTestSession{ctx: context.Background()}

	if err := handler.ConsumeClaim(session, newConsumerTestClaim(&sarama.ConsumerMessage{Topic: "source", Value: []byte{0xff, 0xff}})); err != nil {
		t.Fatal(err)
	}
	if dlqCalls != 1 || len(session.marked) != 1 {
		t.Fatalf("unexpected malformed protobuf result: dlq=%d marked=%d", dlqCalls, len(session.marked))
	}
}

func TestProcessWithRetryStopsWhenContextIsCanceled(t *testing.T) {
	handler := newProcessingTestHandler(func(*sarama.ConsumerMessage) error { return errors.New("temporary") }, func(string, []byte, []sarama.RecordHeader) error {
		t.Fatal("canceled retry must not enter DLQ")
		return nil
	})
	handler.retryConfig.initialBackoff = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- handler.processWithRetry(ctx, &sarama.ConsumerMessage{Topic: "source"})
	}()
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context cancellation, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("retry backoff ignored context cancellation")
	}
}

func TestBuildPostAckResponse(t *testing.T) {
	resp := buildPostAckResponse(&storage.StoreMsgRsp{MessageId: 12345, ClientMessageId: "client-42"})

	if resp.GetPostAckRsp() == nil {
		t.Fatalf("expected PostAckRsp payload, got %T", resp.Payload)
	}
	if resp.GetPostAckRsp().GetMessageId() != 12345 {
		t.Fatalf("expected message_id 12345, got %d", resp.GetPostAckRsp().GetMessageId())
	}
	if resp.GetPostAckRsp().GetClientMessageId() != "client-42" {
		t.Fatalf("expected client_message_id client-42, got %q", resp.GetPostAckRsp().GetClientMessageId())
	}
}

func TestBuildFriendResponsesPreserveClientFields(t *testing.T) {
	t.Run("group info", func(t *testing.T) {
		resp := buildGroupInfoResponse(&friend.GroupInfoRsp{GroupId: 10, GroupName: "Team", Avatar: "group-avatar", ClientNeedSave: true})
		group := resp.GetGroupInfo()
		if group.GetQueryGroupId() != 10 || group.GetQueryGroupName() != "Team" || group.GetAvatar() != "group-avatar" || !group.GetClientNeedSave() {
			t.Fatalf("group info mapping mismatch: %+v", group)
		}
	})

	t.Run("group members", func(t *testing.T) {
		resp := buildGroupMembersResponse(&friend.GroupMemberListRsp{GroupId: 10, Members: []*friend.GroupMemberContact{{
			UserId: 2, Account: "alice", Name: "Alice", Avatar: "avatar", Role: "owner", UpdateTime: "2026-07-11T12:00:00Z",
		}}})
		members := resp.GetGroupMembersRsp()
		if members.GetGroupId() != 10 || len(members.GetMembers()) != 1 {
			t.Fatalf("group member list mapping mismatch: %+v", members)
		}
		member := members.GetMembers()[0]
		if member.GetUserId() != 2 || member.GetAccount() != "alice" || member.GetAvatar() != "avatar" || member.GetRole() != "owner" || member.GetUpdateTime() == "" {
			t.Fatalf("group member mapping mismatch: %+v", member)
		}
	})

	t.Run("joined groups", func(t *testing.T) {
		resp := buildJoinedGroupsResponse(&friend.JoinedGroupListRsp{Groups: []*friend.JoinedGroupContact{{
			GroupId: 10, GroupName: "Team", Avatar: "group-avatar", OwnerUserId: 2, UpdateTime: "2026-07-11T12:00:00Z",
		}}})
		groups := resp.GetJoinedGroupsRsp().GetGroups()
		if len(groups) != 1 || groups[0].GetGroupId() != 10 || groups[0].GetOwnerUserId() != 2 || groups[0].GetAvatar() != "group-avatar" {
			t.Fatalf("joined group mapping mismatch: %+v", groups)
		}
	})

	t.Run("contacts", func(t *testing.T) {
		resp := buildContactListResponse(&friend.FriendListRsp{Contacts: []*friend.FriendContact{{
			UserId: 2, Account: "alice", Name: "Alice", Avatar: "avatar", Alias: "同学", IsNotify: false, UpdateTime: "2026-07-11T12:00:00Z",
		}}}, 1001)
		contacts := resp.GetContactListRsp().GetContacts()
		if len(contacts) != 1 || contacts[0].GetAlias() != "同学" || contacts[0].GetIsNotify() || contacts[0].GetUpdateTime() == "" {
			t.Fatalf("contact mapping mismatch: %+v", contacts)
		}
	})
}

func TestOperationResponseUsesServerForSuccessAndWarnForFailure(t *testing.T) {
	tests := []struct {
		name    string
		resp    *pb.ResponseMessage
		want    string
		success bool
	}{
		{name: "friend success", resp: buildFriendOperationResponse(&friend.FriendOperationRsp{Operation: "update_friend_alias"}, "操作成功"), want: "好友备注更新成功", success: true},
		{name: "friend missing", resp: buildFriendOperationResponse(&friend.FriendOperationRsp{Operation: "update_friend_notify"}, "记录不存在"), want: "好友关系不存在，无法更新通知设置"},
		{name: "group success", resp: buildGroupOperationResponse(&friend.GroupOperationRsp{Operation: "remove_group_member"}, "操作成功"), want: "退出群组成功", success: true},
		{name: "group duplicate", resp: buildGroupOperationResponse(&friend.GroupOperationRsp{Operation: "add_group_member"}, "已存在"), want: "你已经在该群中了"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.success {
				if got := tt.resp.GetServer().GetServerMsg(); got != tt.want {
					t.Fatalf("unexpected success message: %q", got)
				}
				return
			}
			if got := tt.resp.GetWarn().GetWarningMessage(); got != tt.want {
				t.Fatalf("unexpected warning message: %q", got)
			}
		})
	}
}

func TestBuildRelationshipResponsesPreserveStructuredResults(t *testing.T) {
	request := &friend.RelationshipRequestInfo{
		RequestId: 7, RequestType: "group_join", RequesterUserId: 2, RequesterName: "Alice",
		GroupId: 10, GroupName: "Team", Status: "pending", CreatedAt: "2026-07-13T00:00:00Z", ExpiresAt: "2026-07-20T00:00:00Z",
	}
	operation := buildRelationshipOperationResponse(&friend.RelationshipOperationRsp{Operation: "resolve_group_join_request", Request: request}, friend.FriendResult_FORBIDDEN)
	if got := operation.GetRelationshipOperationRsp(); got.GetResult() != "FORBIDDEN" || got.GetRequest().GetGroupName() != "Team" || got.GetRequest().GetExpiresAt() == "" {
		t.Fatalf("relationship operation mapping mismatch: %+v", got)
	}

	list := buildRelationshipRequestListResponse(&friend.RelationshipRequestListRsp{Requests: []*friend.RelationshipRequestInfo{request}})
	if got := list.GetRelationshipRequestListRsp().GetRequests(); len(got) != 1 || got[0].GetRequestId() != 7 {
		t.Fatalf("relationship list mapping mismatch: %+v", got)
	}

	member := buildGroupMemberOperationResponse(&friend.GroupOperationRsp{
		Operation: "update_group_member_role", GroupId: 10, UserId: 2, Role: "admin", UpdateTime: "2026-07-13T01:00:00Z",
	}, friend.FriendResult_FRIEND_OK).GetGroupMemberOperationRsp()
	if member.GetResult() != "FRIEND_OK" || member.GetRole() != "admin" || member.GetGroupId() != 10 {
		t.Fatalf("group member operation mapping mismatch: %+v", member)
	}
}
