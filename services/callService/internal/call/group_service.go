package call

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	callpb "Betterfly2/proto/call"
	pushpb "Betterfly2/proto/push"
	"Betterfly2/shared/logger"
)

func groupOperation(request *callpb.InternalRequest) string {
	return fmt.Sprintf("group-call:%d:%s", request.GetUserId(), request.GetRequest().GetRequestId())
}

func (s *Service) handleGroupCall(ctx context.Context, req *callpb.InternalRequest) error {
	requestID := req.GetRequest().GetRequestId()
	if strings.TrimSpace(requestID) == "" || len(requestID) > 128 {
		return ErrInvalidInput
	}
	if s.groups == nil {
		return ErrMediaUnavailable
	}
	operation := groupOperation(req)
	if _, query := req.GetRequest().Payload.(*callpb.ClientRequest_GetGroupCall); !query {
		completed, err := s.store.OperationCompleted(ctx, operation)
		if err != nil {
			return err
		}
		if completed {
			return nil
		} // Original reply is already in the durable event stream.
	}
	if payload, ok := req.GetRequest().Payload.(*callpb.ClientRequest_CreateGroupCall); ok {
		return s.createGroupCall(ctx, req, payload.CreateGroupCall, operation)
	}
	callID := requestCallID(req.GetRequest())
	if query := req.GetRequest().GetGetGroupCall(); query != nil {
		if (query.GetGroupId() > 0) == (query.GetCallId() != "") || query.GetGroupId() < 0 {
			return ErrInvalidInput
		}
		if query.GetGroupId() > 0 {
			if _, err := s.groups.access.Access(ctx, query.GetGroupId(), req.GetUserId()); err != nil {
				return err
			}
			id, err := s.groups.store.GroupRoomID(ctx, query.GetGroupId())
			if err != nil {
				return err
			}
			callID = id
		}
	}
	if !validRoomID(callID) {
		return ErrInvalidInput
	}
	room, err := s.groups.store.GetRoom(ctx, callID)
	if err != nil {
		return err
	}
	access, err := s.groups.access.Access(ctx, room.GroupID, req.GetUserId())
	if err != nil {
		return err
	}
	if req.GetRequest().GetGetGroupCall() != nil {
		return s.publishToTopic(ctx, req.GetFromKafkaTopic(), req.GetUserId(), s.groupEvent(callpb.CallEventType_GROUP_CALL_UPDATED, room, requestID))
	}
	if room.State != StateActive || !room.ExpiresAt.After(s.now()) {
		return ErrInvalidState
	}
	switch payload := req.GetRequest().Payload.(type) {
	case *callpb.ClientRequest_JoinGroupCall:
		return s.joinGroupCall(ctx, req, room, operation)
	case *callpb.ClientRequest_LeaveGroupCall:
		return s.removeGroupParticipant(ctx, req, room, req.GetUserId(), operation)
	case *callpb.ClientRequest_EndGroupCall:
		if req.GetUserId() != room.CreatorID && access.Role != "owner" && access.Role != "admin" {
			return ErrForbidden
		}
		return s.endGroupCall(ctx, req, room, operation)
	case *callpb.ClientRequest_RemoveGroupCallParticipant:
		target := payload.RemoveGroupCallParticipant.GetTargetUserId()
		if target <= 0 || target == req.GetUserId() {
			return ErrInvalidInput
		}
		// Group role rules apply even when the creator has already left the room.
		targetAccess, err := s.groups.access.Access(ctx, room.GroupID, target)
		if err != nil && !errors.Is(err, ErrForbidden) {
			return err
		}
		if access.Role != "owner" && access.Role != "admin" || targetAccess.Role == "owner" || access.Role == "admin" && targetAccess.Role == "admin" {
			return ErrForbidden
		}
		return s.removeGroupParticipant(ctx, req, room, target, operation)
	default:
		return ErrInvalidInput
	}
}

func validRoomID(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

func (s *Service) createGroupCall(ctx context.Context, req *callpb.InternalRequest, payload *callpb.CreateGroupCall, operation string) error {
	if payload == nil || payload.GetGroupId() <= 0 || payload.GetCallType() != callpb.CallType_AUDIO && payload.GetCallType() != callpb.CallType_VIDEO {
		return ErrInvalidInput
	}
	access, err := s.groups.access.Access(ctx, payload.GetGroupId(), req.GetUserId())
	if err != nil {
		return err
	}
	if _, err := s.groups.store.GroupRoomID(ctx, payload.GetGroupId()); err == nil {
		done, err := s.store.OperationCompleted(ctx, operation)
		if err != nil {
			return err
		}
		if done {
			return nil
		}
		return ErrInvalidState
	} else if !errors.Is(err, ErrCallNotFound) {
		return err
	}
	if len(payload.GetInviteUserIds()) >= s.groups.maxParticipants {
		return ErrInvalidInput
	}
	seen := map[int64]bool{req.GetUserId(): true}
	for _, id := range payload.GetInviteUserIds() {
		if id <= 0 || seen[id] {
			return ErrInvalidInput
		}
		seen[id] = true
		if _, err := s.groups.access.Access(ctx, payload.GetGroupId(), id); err != nil {
			return err
		}
	}
	now := s.now().UTC()
	digest := sha256.Sum256([]byte(operation))
	room := GroupRoom{ID: hex.EncodeToString(digest[:16]), GroupID: payload.GetGroupId(), CreatorID: req.GetUserId(), CallType: payload.GetCallType(),
		State: StateActive, MaxParticipants: s.groups.maxParticipants, CreatedAt: now, ExpiresAt: now.Add(s.groups.ttl), Revision: 1}
	// Idempotent SFU creation happens before issuing any room credential.
	room.MediaSID, err = s.groups.media.EnsureRoom(ctx, room)
	if err != nil {
		return err
	}
	events, err := s.groupChangeEvents(ctx, req, room, operation, callpb.CallEventType_GROUP_CALL_CREATED, nil)
	if err != nil {
		return err
	}
	for _, id := range payload.GetInviteUserIds() {
		if topic := s.groupUserTopic(ctx, room, id); topic != "" {
			event := s.groupEvent(callpb.CallEventType_GROUP_CALL_INVITATION, room, "")
			event.PeerUserId = req.GetUserId()
			pending, err := pendingCallDelivery(operation, fmt.Sprintf("invite-%d", id), topic, id, event)
			if err != nil {
				return err
			}
			events = append(events, pending)
		}
		push := &pushpb.RequestMessage{Payload: &pushpb.RequestMessage_VoipCall{VoipCall: &pushpb.VoIPCallRequest{
			CallId: room.ID, CallerUserId: req.GetUserId(), CalleeUserId: id, CallType: callTypeName(room.CallType), ResultKafkaTopic: "call-service",
			ExpiresAt: timestamp(now.Add(s.ringTTL)), GroupId: room.GroupID, GroupName: access.Name, Required: false,
		}}}
		pending, err := pendingPushRequest(operation, fmt.Sprintf("group-voip-%d", id), voipPushTopic(), push)
		if err != nil {
			return err
		}
		events = append(events, pending)
	}
	_, err = s.groups.store.CommitRoom(ctx, nil, room, operation, events)
	return err
}

func (s *Service) joinGroupCall(ctx context.Context, req *callpb.InternalRequest, expected GroupRoom, operation string) error {
	updated := cloneRoom(expected)
	participant, exists := updated.Participant(req.GetUserId())
	events := []PendingEvent{}
	if !exists {
		if len(updated.Participants) >= updated.MaxParticipants {
			return ErrRoomFull
		}
		participant = RoomParticipant{UserID: req.GetUserId(), Identity: fmt.Sprintf("bf2-%d-%s", req.GetUserId(), callEventID(operation, "admission")[5:21]), JoinDeadline: s.now().Add(75 * time.Second).UTC()}
		updated.Participants = append(updated.Participants, participant)
	} else {
		// A new admission fences both late joined and left callbacks from the old
		// connection, including leave-before-join while replacing an SFU identity.
		events = append(events, mediaEvent(operation, "RemoveParticipant", updated.Name(), participant.Identity))
		participant = RoomParticipant{UserID: req.GetUserId(), Identity: fmt.Sprintf("bf2-%d-%s", req.GetUserId(), callEventID(operation, "admission")[5:21]), JoinDeadline: s.now().Add(75 * time.Second).UTC()}
		for i, p := range updated.Participants {
			if p.UserID == participant.UserID {
				updated.Participants[i] = participant
			}
		}
	}
	sid, err := s.groups.media.EnsureRoom(ctx, updated)
	if err != nil {
		return err
	}
	if sid != updated.MediaSID {
		return ErrInvalidState
	}
	token, expires, err := s.groups.media.Token(updated, participant, s.now().UTC())
	if err != nil {
		return err
	}
	updated.Revision++
	event := s.groupEvent(callpb.CallEventType_GROUP_CALL_JOINED, updated, req.GetRequest().GetRequestId())
	event.JoinToken = token
	event.TokenExpiresAt = timestamp(expires)
	event.SfuUrl = s.groups.media.URL()
	changed, err := s.groupChangeEvents(ctx, req, updated, operation, callpb.CallEventType_GROUP_CALL_JOINED, event)
	if err != nil {
		return err
	}
	_, err = s.groups.store.CommitRoom(ctx, &expected, updated, operation, append(events, changed...))
	return err
}

func cloneRoom(room GroupRoom) GroupRoom {
	room.Participants = append([]RoomParticipant(nil), room.Participants...)
	return room
}

func mediaEvent(operation, method, room, identity string) PendingEvent {
	payload, _ := json.Marshal(map[string]string{"room": room, "identity": identity})
	return PendingEvent{EventID: callEventID(operation, "media-"+method+identity), OperationKey: operation, Topic: mediaTopicPrefix + method, Payload: payload}
}

func (s *Service) removeGroupParticipant(ctx context.Context, req *callpb.InternalRequest, expected GroupRoom, target int64, operation string) error {
	participant, exists := expected.Participant(target)
	if !exists {
		return ErrInvalidState
	}
	updated := cloneRoom(expected)
	updated.Participants = nil
	updated.Revision++
	for _, p := range expected.Participants {
		if p.UserID != target {
			updated.Participants = append(updated.Participants, p)
		}
	}
	if len(updated.Participants) == 0 {
		updated.State = StateEnded
	}
	events := []PendingEvent{mediaEvent(operation, "RemoveParticipant", updated.Name(), participant.Identity)}
	if updated.State == StateEnded {
		events = append(events, mediaEvent(operation, "DeleteRoom", updated.Name(), ""))
	}
	kind := callpb.CallEventType_GROUP_CALL_LEFT
	if target != req.GetUserId() {
		kind = callpb.CallEventType_GROUP_CALL_UPDATED
		if updated.State == StateEnded {
			kind = callpb.CallEventType_GROUP_CALL_ENDED
		}
	}
	changed, err := s.groupChangeEvents(ctx, req, updated, operation, kind, nil)
	if err != nil {
		return err
	}
	events = append(events, changed...)
	if target != req.GetUserId() {
		if topic := s.groupUserTopic(ctx, updated, target); topic != "" {
			event, err := pendingCallDelivery(operation, "removed", topic, target, s.groupEvent(callpb.CallEventType_GROUP_CALL_LEFT, updated, ""))
			if err != nil {
				return err
			}
			events = append(events, event)
		}
	}
	_, err = s.groups.store.CommitRoom(ctx, &expected, updated, operation, events)
	return err
}

func (s *Service) endGroupCall(ctx context.Context, req *callpb.InternalRequest, expected GroupRoom, operation string) error {
	updated := cloneRoom(expected)
	updated.State = StateEnded
	updated.Revision++
	events := []PendingEvent{mediaEvent(operation, "DeleteRoom", updated.Name(), "")}
	changed, err := s.groupChangeEvents(ctx, req, updated, operation, callpb.CallEventType_GROUP_CALL_ENDED, nil)
	if err != nil {
		return err
	}
	_, err = s.groups.store.CommitRoom(ctx, &expected, updated, operation, append(events, changed...))
	return err
}

func (s *Service) groupChangeEvents(ctx context.Context, req *callpb.InternalRequest, room GroupRoom, operation string, kind callpb.CallEventType, reply *callpb.CallEvent) ([]PendingEvent, error) {
	if reply == nil {
		reply = s.groupEvent(kind, room, req.GetRequest().GetRequestId())
	}
	first, err := pendingCallDelivery(operation, "reply", req.GetFromKafkaTopic(), req.GetUserId(), reply)
	if err != nil {
		return nil, err
	}
	events := []PendingEvent{first}
	// The last leaver keeps their correlated LEFT reply and also receives ENDED.
	if room.State == StateEnded && reply.GetEventType() == callpb.CallEventType_GROUP_CALL_LEFT {
		return s.appendRoomBroadcast(ctx, room, operation, events)
	}
	return s.appendRoomBroadcast(ctx, room, operation, events, req.GetUserId())
}

func (s *Service) groupEvent(kind callpb.CallEventType, room GroupRoom, requestID string) *callpb.CallEvent {
	info := &callpb.GroupCallInfo{CallId: room.ID, GroupId: room.GroupID, CreatorUserId: room.CreatorID, CallType: room.CallType, State: stateToProto(room.State),
		RoomName: room.Name(), MaxParticipants: int32(room.MaxParticipants), CreatedAt: timestamp(room.CreatedAt), ExpiresAt: timestamp(room.ExpiresAt), Revision: room.Revision}
	for _, p := range room.Participants {
		info.Participants = append(info.Participants, &callpb.GroupCallParticipant{UserId: p.UserID, Identity: p.Identity, Connected: p.Connected})
	}
	sort.Slice(info.Participants, func(i, j int) bool { return info.Participants[i].UserId < info.Participants[j].UserId })
	return &callpb.CallEvent{EventType: kind, CallId: room.ID, CallType: room.CallType, State: stateToProto(room.State), GroupCall: info, RequestId: requestID, Timestamp: timestamp(s.now())}
}

func (s *Service) sweepGroupCalls(ctx context.Context, now time.Time) error {
	if s.groups == nil {
		return nil
	}
	rooms, err := s.groups.store.DueRooms(ctx, now, 100)
	if err != nil {
		return err
	}
	for _, expected := range rooms {
		if expected.State != StateActive {
			continue
		}
		updated := cloneRoom(expected)
		updated.Participants = nil
		updated.Revision++
		operation := fmt.Sprintf("group-timeout:%s:%d", expected.ID, expected.Revision)
		events := []PendingEvent{}
		for _, p := range expected.Participants {
			if !expected.ExpiresAt.After(now) || !p.Connected && !p.JoinDeadline.After(now) {
				events = append(events, mediaEvent(operation, "RemoveParticipant", expected.Name(), p.Identity))
			} else {
				updated.Participants = append(updated.Participants, p)
			}
		}
		// An empty room has a short initial grace for its creator to join.
		if !expected.ExpiresAt.After(now) || len(updated.Participants) == 0 {
			updated.State = StateEnded
			events = append(events, mediaEvent(operation, "DeleteRoom", expected.Name(), ""))
		}
		events, err = s.appendRoomBroadcast(ctx, updated, operation, events)
		if err != nil {
			return err
		}
		if _, err = s.groups.store.CommitRoom(ctx, &expected, updated, operation, events); err != nil && !errors.Is(err, ErrRoomConflict) {
			return err
		}
	}
	return nil
}

func (s *Service) appendRoomBroadcast(ctx context.Context, room GroupRoom, operation string, events []PendingEvent, exclude ...int64) ([]PendingEvent, error) {
	members, err := s.groups.access.Members(ctx, room.GroupID)
	if err != nil {
		return nil, err
	}
	seen := make(map[int64]bool, len(members))
	for _, id := range exclude {
		seen[id] = true
	}
	kind := callpb.CallEventType_GROUP_CALL_UPDATED
	if room.State == StateEnded {
		kind = callpb.CallEventType_GROUP_CALL_ENDED
	}
	for _, id := range members {
		if id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		topic := s.groupUserTopic(ctx, room, id)
		if topic == "" {
			continue
		}
		event, err := pendingCallDelivery(operation, fmt.Sprintf("updated-%d", id), topic, id, s.groupEvent(kind, room, ""))
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, nil
}

func (s *Service) groupUserTopic(ctx context.Context, room GroupRoom, userID int64) string {
	topic, err := s.store.UserTopic(ctx, userID)
	if err != nil {
		if !errors.Is(err, ErrUserOffline) {
			logger.Sugar().Warnw("群通话事件路由暂时不可用，跳过本次投递", "call_id", room.ID, "group_id", room.GroupID, "target_user_id", userID, "error", err)
		}
		return ""
	}
	return topic
}
