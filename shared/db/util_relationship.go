package db

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	RequestTypeFriend      = "friend"
	RequestTypeGroupJoin   = "group_join"
	RequestTypeGroupInvite = "group_invite"
	RequestStatusPending   = "pending"
	RequestStatusAccepted  = "accepted"
	RequestStatusRejected  = "rejected"
	RequestStatusCancelled = "cancelled"
	RequestStatusExpired   = "expired"
	GroupRoleOwner         = "owner"
	GroupRoleAdmin         = "admin"
	GroupRoleMember        = "member"
)

const relationshipRequestTTL = 7 * 24 * time.Hour

var (
	ErrRelationshipForbidden    = errors.New("relationship operation forbidden")
	ErrRelationshipNotFound     = errors.New("relationship request not found")
	ErrRelationshipExpired      = errors.New("relationship request expired")
	ErrRelationshipInvalidState = errors.New("relationship request state invalid")
	ErrAlreadyRelated           = errors.New("relationship already exists")
)

type RelationshipRequestView struct {
	RelationshipRequest
	RequesterName   string `gorm:"column:requester_name"`
	RequesterAvatar string `gorm:"column:requester_avatar"`
	TargetName      string `gorm:"column:target_name"`
	TargetAvatar    string `gorm:"column:target_avatar"`
	GroupName       string `gorm:"column:group_name"`
	GroupAvatar     string `gorm:"column:group_avatar"`
}

func relationshipNow() time.Time { return time.Now().UTC() }

func relationshipTime(value time.Time) string {
	return value.UTC().Format("2006-01-02T15:04:05.000000000Z")
}

// Existing synchronization columns are varchar(25), so keep entity update
// timestamps at second precision while request audit timestamps retain nanos.
func relationshipUpdateTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339)
}

func relationshipWindow(now time.Time) (string, string) {
	return relationshipTime(now), relationshipTime(now.Add(relationshipRequestTTL))
}

func CreateFriendRequest(requesterID, targetID int64, message string) (*RelationshipRequestView, bool, error) {
	if requesterID <= 0 || targetID <= 0 || requesterID == targetID {
		return nil, false, ErrRelationshipInvalidState
	}
	if active, err := friendshipActive(requesterID, targetID); err != nil || active {
		if active {
			return nil, false, ErrAlreadyRelated
		}
		return nil, false, err
	}
	first, second := requesterID, targetID
	if first > second {
		first, second = second, first
	}
	key := fmt.Sprintf("friend:%d:%d", first, second)
	request, created, err := createPendingRequest(RelationshipRequest{
		RequestType: RequestTypeFriend, RequesterUserID: requesterID, TargetUserID: targetID,
		Message: strings.TrimSpace(message), ActiveKey: &key,
	})
	if err != nil {
		return nil, false, err
	}
	view, err := GetRelationshipRequest(request.ID)
	return view, created, err
}

func CreateGroupJoinRequest(userID, groupID int64, message string) (*RelationshipRequestView, bool, error) {
	if exists, err := IsActiveGroupMember(groupID, userID); err != nil || exists {
		if exists {
			return nil, false, ErrAlreadyRelated
		}
		return nil, false, err
	}
	if group, err := GetGroupByID(groupID); err != nil || group == nil {
		if err == nil {
			err = ErrRelationshipNotFound
		}
		return nil, false, err
	}
	key := fmt.Sprintf("group_join:%d:%d", groupID, userID)
	request, created, err := createPendingRequest(RelationshipRequest{
		RequestType: RequestTypeGroupJoin, RequesterUserID: userID, GroupID: groupID,
		Message: strings.TrimSpace(message), ActiveKey: &key,
	})
	if err != nil {
		return nil, false, err
	}
	view, err := GetRelationshipRequest(request.ID)
	return view, created, err
}

func CreateGroupInvitation(actorID, groupID, targetID int64, message string) (*RelationshipRequestView, bool, error) {
	if _, allowed, err := RequireGroupManager(groupID, actorID); err != nil || !allowed {
		if err == nil {
			err = ErrRelationshipForbidden
		}
		return nil, false, err
	}
	if exists, err := IsActiveGroupMember(groupID, targetID); err != nil || exists {
		if exists {
			return nil, false, ErrAlreadyRelated
		}
		return nil, false, err
	}
	key := fmt.Sprintf("group_invite:%d:%d", groupID, targetID)
	request, created, err := createPendingRequest(RelationshipRequest{
		RequestType: RequestTypeGroupInvite, RequesterUserID: actorID, TargetUserID: targetID,
		GroupID: groupID, Message: strings.TrimSpace(message), ActiveKey: &key,
	})
	if err != nil {
		return nil, false, err
	}
	view, err := GetRelationshipRequest(request.ID)
	return view, created, err
}

func createPendingRequest(request RelationshipRequest) (*RelationshipRequest, bool, error) {
	now := relationshipNow()
	request.Status = RequestStatusPending
	request.CreatedAt, request.ExpiresAt = relationshipWindow(now)
	created := false
	err := DB().Transaction(func(tx *gorm.DB) error {
		if err := expireActiveKey(tx, *request.ActiveKey, now); err != nil {
			return err
		}
		var existing RelationshipRequest
		err := tx.Where("active_key = ?", *request.ActiveKey).First(&existing).Error
		if err == nil {
			request = existing
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		result := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "active_key"}},
			DoNothing: true,
		}).Create(&request)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			if err := tx.Where("active_key = ?", *request.ActiveKey).First(&existing).Error; err != nil {
				return err
			}
			request = existing
			return nil
		}
		created = true
		return nil
	})
	return &request, created, err
}

func expireActiveKey(tx *gorm.DB, key string, now time.Time) error {
	return tx.Model(&RelationshipRequest{}).
		Where("active_key = ? AND status = ? AND expires_at <= ?", key, RequestStatusPending, relationshipTime(now)).
		Updates(map[string]interface{}{"status": RequestStatusExpired, "active_key": nil, "resolved_at": relationshipTime(now)}).Error
}

func friendshipActive(userID, friendID int64) (bool, error) {
	var count int64
	err := DB().Model(&Friend{}).
		Where("user_id = ? AND friend_id = ? AND is_delete = ?", userID, friendID, false).
		Count(&count).Error
	return count > 0, err
}

func ListFriendRequests(userID int64, includeOutgoing bool) ([]RelationshipRequestView, error) {
	if err := expireRequestsForUser(userID); err != nil {
		return nil, err
	}
	query := relationshipViewQuery().Where("relationship_requests.request_type = ? AND relationship_requests.target_user_id = ?", RequestTypeFriend, userID)
	if includeOutgoing {
		query = relationshipViewQuery().Where("relationship_requests.request_type = ? AND (relationship_requests.target_user_id = ? OR relationship_requests.requester_user_id = ?)", RequestTypeFriend, userID, userID)
	}
	var requests []RelationshipRequestView
	err := query.Order("relationship_requests.created_at DESC").Limit(100).Scan(&requests).Error
	return requests, err
}

func ListGroupJoinRequests(actorID, groupID int64) ([]RelationshipRequestView, error) {
	if _, allowed, err := RequireGroupManager(groupID, actorID); err != nil || !allowed {
		if err == nil {
			err = ErrRelationshipForbidden
		}
		return nil, err
	}
	if err := expireRequestsForGroup(groupID); err != nil {
		return nil, err
	}
	var requests []RelationshipRequestView
	err := relationshipViewQuery().
		Where("relationship_requests.request_type = ? AND relationship_requests.group_id = ? AND relationship_requests.status = ?", RequestTypeGroupJoin, groupID, RequestStatusPending).
		Order("relationship_requests.created_at DESC").Limit(100).Scan(&requests).Error
	return requests, err
}

func ListGroupInvitations(userID int64, includeOutgoing bool) ([]RelationshipRequestView, error) {
	if err := expireRequestsForUser(userID); err != nil {
		return nil, err
	}
	query := relationshipViewQuery().Where("relationship_requests.request_type = ? AND relationship_requests.target_user_id = ? AND relationship_requests.status = ?", RequestTypeGroupInvite, userID, RequestStatusPending)
	if includeOutgoing {
		query = relationshipViewQuery().Where(`relationship_requests.status = ? AND (
(relationship_requests.request_type = ? AND (relationship_requests.target_user_id = ? OR relationship_requests.requester_user_id = ?)) OR
(relationship_requests.request_type = ? AND relationship_requests.requester_user_id = ?))`,
			RequestStatusPending, RequestTypeGroupInvite, userID, userID, RequestTypeGroupJoin, userID)
	}
	var requests []RelationshipRequestView
	err := query.
		Order("relationship_requests.created_at DESC").Limit(100).Scan(&requests).Error
	return requests, err
}

func ResolveFriendRequest(actorID, requestID int64, decision string) (*RelationshipRequestView, error) {
	return resolveRequest(actorID, requestID, RequestTypeFriend, decision)
}

func ResolveGroupJoinRequest(actorID, requestID int64, decision string) (*RelationshipRequestView, error) {
	return resolveRequest(actorID, requestID, RequestTypeGroupJoin, decision)
}

func ResolveGroupInvitation(actorID, requestID int64, decision string) (*RelationshipRequestView, error) {
	return resolveRequest(actorID, requestID, RequestTypeGroupInvite, decision)
}

func resolveRequest(actorID, requestID int64, requestType, decision string) (*RelationshipRequestView, error) {
	now := relationshipNow()
	var outcomeErr error
	err := DB().Transaction(func(tx *gorm.DB) error {
		var request RelationshipRequest
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&request, requestID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrRelationshipNotFound
			}
			return err
		}
		if request.RequestType != requestType {
			return ErrRelationshipNotFound
		}
		if request.Status != RequestStatusPending {
			if request.Status == RequestStatusExpired {
				return ErrRelationshipExpired
			}
			return ErrRelationshipInvalidState
		}
		expiresAt, err := time.Parse(time.RFC3339Nano, request.ExpiresAt)
		if err != nil || !expiresAt.After(now) {
			if updateErr := closeRequest(tx, &request, RequestStatusExpired, actorID, now); updateErr != nil {
				return updateErr
			}
			outcomeErr = ErrRelationshipExpired
			return nil
		}
		if !canResolveRequest(tx, actorID, &request, decision) {
			return ErrRelationshipForbidden
		}
		status := RequestStatusRejected
		switch decision {
		case RequestStatusAccepted:
			status = RequestStatusAccepted
		case RequestStatusRejected:
			status = RequestStatusRejected
		case RequestStatusCancelled:
			status = RequestStatusCancelled
		default:
			return ErrRelationshipInvalidState
		}
		if status == RequestStatusAccepted {
			updateTime := relationshipUpdateTime(now)
			switch request.RequestType {
			case RequestTypeFriend:
				if err := addDirectFriendPairTx(tx, request.RequesterUserID, request.TargetUserID, updateTime); err != nil {
					return err
				}
			case RequestTypeGroupJoin, RequestTypeGroupInvite:
				if err := addGroupMemberTx(tx, request.GroupID, groupRequestUserID(&request), updateTime); err != nil {
					return err
				}
			}
		}
		return closeRequest(tx, &request, status, actorID, now)
	})
	if err != nil {
		return nil, err
	}
	if outcomeErr != nil {
		return nil, outcomeErr
	}
	return GetRelationshipRequest(requestID)
}

func canResolveRequest(tx *gorm.DB, actorID int64, request *RelationshipRequest, decision string) bool {
	if decision == RequestStatusCancelled {
		return actorID == request.RequesterUserID
	}
	switch request.RequestType {
	case RequestTypeFriend, RequestTypeGroupInvite:
		return actorID == request.TargetUserID
	case RequestTypeGroupJoin:
		role, err := groupRoleTx(tx, request.GroupID, actorID)
		return err == nil && (role == GroupRoleOwner || role == GroupRoleAdmin)
	default:
		return false
	}
}

func closeRequest(tx *gorm.DB, request *RelationshipRequest, status string, actorID int64, now time.Time) error {
	return tx.Model(request).Updates(map[string]interface{}{
		"status": status, "active_key": nil, "resolved_at": relationshipTime(now), "resolved_by": actorID,
	}).Error
}

func addDirectFriendPairTx(tx *gorm.DB, userID, friendID int64, now string) error {
	for _, pair := range [][2]int64{{userID, friendID}, {friendID, userID}} {
		var relation Friend
		err := tx.Where("user_id = ? AND friend_id = ?", pair[0], pair[1]).First(&relation).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if err := tx.Create(&Friend{UserID: pair[0], FriendID: pair[1], IsNotify: true, UpdateTime: now}).Error; err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if err := tx.Model(&relation).Updates(map[string]interface{}{"is_delete": false, "update_time": now}).Error; err != nil {
			return err
		}
	}
	return nil
}

func addGroupMemberTx(tx *gorm.DB, groupID, userID int64, now string) error {
	var group Group
	if err := tx.Where("group_id = ? AND is_delete = ?", groupID, false).First(&group).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrRelationshipNotFound
	} else if err != nil {
		return err
	}
	var member GroupMember
	err := tx.Where("group_id = ? AND user_id = ?", groupID, userID).First(&member).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if err := tx.Create(&GroupMember{GroupID: groupID, UserID: userID, Role: GroupRoleMember, UpdateTime: now}).Error; err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	return tx.Model(&group).Update("update_time", now).Error
}

func groupRequestUserID(request *RelationshipRequest) int64 {
	if request.RequestType == RequestTypeGroupInvite {
		return request.TargetUserID
	}
	return request.RequesterUserID
}

func GetRelationshipRequest(requestID int64) (*RelationshipRequestView, error) {
	var request RelationshipRequestView
	err := relationshipViewQuery().Where("relationship_requests.id = ?", requestID).Scan(&request).Error
	if err != nil {
		return nil, err
	}
	if request.ID == 0 {
		return nil, ErrRelationshipNotFound
	}
	return &request, nil
}

func relationshipViewQuery() *gorm.DB {
	return DB().Table("relationship_requests").
		Select(`relationship_requests.*, requester.name AS requester_name, requester.avatar AS requester_avatar,
target.name AS target_name, target.avatar AS target_avatar, groups.name AS group_name, groups.avatar AS group_avatar`).
		Joins("LEFT JOIN users AS requester ON requester.id = relationship_requests.requester_user_id").
		Joins("LEFT JOIN users AS target ON target.id = relationship_requests.target_user_id").
		Joins("LEFT JOIN groups ON groups.group_id = relationship_requests.group_id")
}

func expireRequestsForUser(userID int64) error {
	now := relationshipTime(relationshipNow())
	return DB().Model(&RelationshipRequest{}).
		Where("status = ? AND expires_at <= ? AND (requester_user_id = ? OR target_user_id = ?)", RequestStatusPending, now, userID, userID).
		Updates(map[string]interface{}{"status": RequestStatusExpired, "active_key": nil, "resolved_at": now}).Error
}

func expireRequestsForGroup(groupID int64) error {
	now := relationshipTime(relationshipNow())
	return DB().Model(&RelationshipRequest{}).
		Where("status = ? AND expires_at <= ? AND group_id = ?", RequestStatusPending, now, groupID).
		Updates(map[string]interface{}{"status": RequestStatusExpired, "active_key": nil, "resolved_at": now}).Error
}

func groupRoleTx(tx *gorm.DB, groupID, userID int64) (string, error) {
	var member GroupMember
	err := tx.Where("group_id = ? AND user_id = ?", groupID, userID).First(&member).Error
	return member.Role, err
}

func RequireGroupManager(groupID, userID int64) (string, bool, error) {
	role, err := groupRoleTx(DB(), groupID, userID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", false, nil
	}
	return role, canManageGroup(role), err
}

func canManageGroup(role string) bool {
	return role == GroupRoleOwner || role == GroupRoleAdmin
}

func canKickGroupMember(actorRole, targetRole string) bool {
	if !canManageGroup(actorRole) || targetRole == GroupRoleOwner {
		return false
	}
	return actorRole == GroupRoleOwner || targetRole == GroupRoleMember
}

func canChangeGroupMemberRole(actorRole, targetRole, newRole string) bool {
	return actorRole == GroupRoleOwner && targetRole != GroupRoleOwner && (newRole == GroupRoleAdmin || newRole == GroupRoleMember)
}

func KickGroupMemberBy(actorID, groupID, targetID int64) (string, error) {
	now := relationshipUpdateTime(relationshipNow())
	err := DB().Transaction(func(tx *gorm.DB) error {
		actorRole, err := groupRoleTx(tx, groupID, actorID)
		if err != nil || !canManageGroup(actorRole) {
			return ErrRelationshipForbidden
		}
		targetRole, err := groupRoleTx(tx, groupID, targetID)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrRelationshipNotFound
		}
		if err != nil {
			return err
		}
		if !canKickGroupMember(actorRole, targetRole) {
			return ErrRelationshipForbidden
		}
		result := tx.Where("group_id = ? AND user_id = ?", groupID, targetID).Delete(&GroupMember{})
		if result.Error != nil {
			return result.Error
		}
		return tx.Model(&Group{}).Where("group_id = ?", groupID).Update("update_time", now).Error
	})
	return now, err
}

func UpdateGroupMemberRoleBy(actorID, groupID, targetID int64, role string) (string, error) {
	if role != GroupRoleAdmin && role != GroupRoleMember {
		return "", ErrRelationshipInvalidState
	}
	now := relationshipUpdateTime(relationshipNow())
	err := DB().Transaction(func(tx *gorm.DB) error {
		actorRole, err := groupRoleTx(tx, groupID, actorID)
		if err != nil || !canManageGroup(actorRole) {
			return ErrRelationshipForbidden
		}
		targetRole, err := groupRoleTx(tx, groupID, targetID)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrRelationshipNotFound
		}
		if err != nil {
			return err
		}
		if !canChangeGroupMemberRole(actorRole, targetRole, role) {
			return ErrRelationshipForbidden
		}
		if err := tx.Model(&GroupMember{}).Where("group_id = ? AND user_id = ?", groupID, targetID).
			Updates(map[string]interface{}{"role": role, "update_time": now}).Error; err != nil {
			return err
		}
		return tx.Model(&Group{}).Where("group_id = ?", groupID).Update("update_time", now).Error
	})
	return now, err
}
