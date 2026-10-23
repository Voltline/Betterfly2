package handler

import (
	envelope "Betterfly2/proto/envelope"
	friend "Betterfly2/proto/friend"
	"Betterfly2/shared/db"
	"Betterfly2/shared/mq"
	"fmt"
	"sort"

	"gorm.io/gorm"
)

const (
	relationshipEventCreated   = "created"
	relationshipEventResolved  = "resolved"
	relationshipEventCancelled = "cancelled"
)

// relationshipEventOutbox 在申请或邀请状态变化成功后，为每个相关用户（不含操作者）生成一条
// RelationshipChangedEvent，与业务写入同一事务提交。投递尽力而为，接收者离线时由DF丢弃；
// 重复创建（REQUEST_PENDING）和过期不产生事件。
func relationshipEventOutbox(database *gorm.DB, operationKey string, req *friend.RequestMessage, resp *friend.ResponseMessage) ([]db.PendingOutboxEvent, error) {
	request := resp.GetRelationshipOperationRsp().GetRequest()
	if resp.GetResult() != friend.FriendResult_FRIEND_OK || request == nil || req.GetFromKafkaTopic() == "" {
		return nil, nil
	}
	var operation, changedAt string
	var actorID int64
	switch resp.GetRelationshipOperationRsp().GetOperation() {
	case "create_friend_request", "create_group_join_request", "invite_group_member":
		operation, actorID, changedAt = relationshipEventCreated, request.GetRequesterUserId(), request.GetCreatedAt()
	case "resolve_friend_request", "resolve_group_join_request", "resolve_group_invitation":
		operation, actorID, changedAt = relationshipEventResolved, request.GetResolvedBy(), request.GetResolvedAt()
		if request.GetStatus() == db.RequestStatusCancelled {
			operation = relationshipEventCancelled
		}
	default:
		return nil, nil
	}

	recipients := map[int64]struct{}{}
	for _, userID := range []int64{request.GetRequesterUserId(), request.GetTargetUserId()} {
		recipients[userID] = struct{}{}
	}
	if request.GetRequestType() == db.RequestTypeGroupJoin {
		managerIDs, err := db.ListGroupManagerIDsWithDB(database, request.GetGroupId())
		if err != nil {
			return nil, err
		}
		for _, userID := range managerIDs {
			recipients[userID] = struct{}{}
		}
	}
	delete(recipients, actorID)
	userIDs := make([]int64, 0, len(recipients))
	for userID := range recipients {
		if userID > 0 {
			userIDs = append(userIDs, userID)
		}
	}
	sort.Slice(userIDs, func(i, j int) bool { return userIDs[i] < userIDs[j] })

	events := make([]db.PendingOutboxEvent, 0, len(userIDs))
	for _, userID := range userIDs {
		payload, err := mq.MarshalEnvelope(envelope.MessageType_FRIEND_RESPONSE, &friend.ResponseMessage{
			Result: friend.FriendResult_FRIEND_OK, TargetUserId: userID,
			Payload: &friend.ResponseMessage_RelationshipChangedEvent{RelationshipChangedEvent: &friend.RelationshipChangedEvent{
				Request: request, Operation: operation, ChangedAt: changedAt,
			}},
		})
		if err != nil {
			return nil, err
		}
		events = append(events, db.PendingOutboxEvent{
			EventID: db.StableEventID("friend", operationKey, fmt.Sprintf("relationship_changed:%d", userID)),
			Topic:   req.GetFromKafkaTopic(), Payload: payload,
		})
	}
	return events, nil
}
