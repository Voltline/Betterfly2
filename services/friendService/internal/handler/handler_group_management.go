package handler

import (
	friend "Betterfly2/proto/friend"
	"Betterfly2/shared/db"
	"Betterfly2/shared/utils"
	"strings"

	"gorm.io/gorm"
)

// validGroupName 与groups.name列宽及客户端上限一致：去掉首尾空白后非空且不超过100个字符。
func validGroupName(name string) bool {
	return name != "" && utils.ValidText(name, 100)
}

func (h *FriendHandler) handleUpdateGroupNameWithDB(database *gorm.DB, req *friend.RequestMessage, payload *friend.UpdateGroupName) (*friend.ResponseMessage, error) {
	groupName := strings.TrimSpace(payload.GetGroupName())
	if payload.GetRequestUserId() <= 0 || payload.GetGroupId() <= 0 || !validGroupName(groupName) {
		return groupManagementOperation(req, "update_group_name", friend.FriendResult_INVALID_ARGUMENT, payload.GetGroupId(), payload.GetRequestUserId(), "", "", groupName, 0), nil
	}

	updatedAt, err := db.UpdateGroupNameByWithDB(h.resolveDatabase(database), payload.GetRequestUserId(), payload.GetGroupId(), groupName)
	if err != nil {
		result := relationshipResult(err)
		if result == friend.FriendResult_SERVICE_ERROR {
			return nil, err
		}
		return groupManagementOperation(req, "update_group_name", result, payload.GetGroupId(), payload.GetRequestUserId(), "", "", groupName, 0), nil
	}
	return groupManagementOperation(req, "update_group_name", friend.FriendResult_FRIEND_OK, payload.GetGroupId(), payload.GetRequestUserId(), "", updatedAt, groupName, 0), nil
}

func (h *FriendHandler) handleTransferGroupOwnerWithDB(database *gorm.DB, req *friend.RequestMessage, payload *friend.TransferGroupOwner) (*friend.ResponseMessage, error) {
	if payload.GetRequestUserId() <= 0 || payload.GetGroupId() <= 0 || payload.GetUserId() <= 0 || payload.GetRequestUserId() == payload.GetUserId() {
		return groupManagementOperation(req, "transfer_group_owner", friend.FriendResult_INVALID_ARGUMENT, payload.GetGroupId(), payload.GetUserId(), db.GroupRoleOwner, "", "", payload.GetRequestUserId()), nil
	}

	groupName, previousOwnerID, updatedAt, err := db.TransferGroupOwnerWithDB(h.resolveDatabase(database), payload.GetRequestUserId(), payload.GetGroupId(), payload.GetUserId())
	if err != nil {
		result := relationshipResult(err)
		if result == friend.FriendResult_SERVICE_ERROR {
			return nil, err
		}
		return groupManagementOperation(req, "transfer_group_owner", result, payload.GetGroupId(), payload.GetUserId(), db.GroupRoleOwner, "", "", payload.GetRequestUserId()), nil
	}
	return groupManagementOperation(req, "transfer_group_owner", friend.FriendResult_FRIEND_OK, payload.GetGroupId(), payload.GetUserId(), db.GroupRoleOwner, updatedAt, groupName, previousOwnerID), nil
}

func groupManagementOperation(req *friend.RequestMessage, operation string, result friend.FriendResult, groupID, userID int64, role, updateTime, groupName string, previousOwnerID int64) *friend.ResponseMessage {
	return &friend.ResponseMessage{
		Result:       result,
		TargetUserId: req.GetTargetUserId(),
		Payload: &friend.ResponseMessage_GroupOperationRsp{GroupOperationRsp: &friend.GroupOperationRsp{
			Operation: operation, GroupId: groupID, UserId: userID, Role: role, UpdateTime: updateTime,
			GroupName: groupName, PreviousOwnerUserId: previousOwnerID,
		}},
	}
}
