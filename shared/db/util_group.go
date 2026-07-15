package db

import (
	"Betterfly2/shared/utils"
	"errors"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type GroupMemberContact struct {
	UserID     int64  `gorm:"column:user_id"`
	Account    string `gorm:"column:account"`
	Name       string `gorm:"column:name"`
	Avatar     string `gorm:"column:avatar"`
	Role       string `gorm:"column:role"`
	UpdateTime string `gorm:"column:update_time"`
}

type JoinedGroupContact struct {
	GroupID     int64  `gorm:"column:group_id"`
	GroupName   string `gorm:"column:group_name"`
	Avatar      string `gorm:"column:avatar"`
	OwnerUserID int64  `gorm:"column:owner_user_id"`
	UpdateTime  string `gorm:"column:update_time"`
}

func newGroupMember(groupID, userID int64, role, joinedAt string) *GroupMember {
	return &GroupMember{
		GroupID:    groupID,
		UserID:     userID,
		Role:       role,
		JoinedAt:   joinedAt,
		UpdateTime: joinedAt,
	}
}

func BackfillGroupMemberJoinedAtWithDB(database *gorm.DB) error {
	return database.Model(&GroupMember{}).
		Where("joined_at IS NULL OR joined_at = ''").
		Update("joined_at", gorm.Expr("update_time")).Error
}

func CreateGroupWithOwnerWithDB(database *gorm.DB, ownerUserID, groupID int64, groupName string) (bool, string, error) {
	now := utils.NowTime()
	alreadyExists := false

	err := database.Transaction(func(tx *gorm.DB) error {
		var group Group
		err := tx.Where("group_id = ?", groupID).First(&group).Error
		if err == nil && !group.IsDelete {
			alreadyExists = true
			return nil
		}
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		if errors.Is(err, gorm.ErrRecordNotFound) {
			if err := tx.Create(&Group{
				GroupID:     groupID,
				Name:        groupName,
				Avatar:      "",
				OwnerUserID: ownerUserID,
				IsDelete:    false,
				UpdateTime:  now,
			}).Error; err != nil {
				return err
			}
		} else {
			if err := tx.Model(&Group{}).
				Where("group_id = ?", groupID).
				Updates(map[string]interface{}{
					"name":          groupName,
					"owner_user_id": ownerUserID,
					"is_delete":     false,
					"update_time":   now,
				}).Error; err != nil {
				return err
			}
		}

		var member GroupMember
		err = tx.Where("group_id = ? AND user_id = ?", groupID, ownerUserID).First(&member).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return tx.Create(newGroupMember(groupID, ownerUserID, GroupRoleOwner, now)).Error
		}
		if err != nil {
			return err
		}

		return tx.Model(&GroupMember{}).
			Where("group_id = ? AND user_id = ?", groupID, ownerUserID).
			Updates(groupMemberRoleUpdates("owner", now)).Error
	})

	return alreadyExists, now, err
}

func GetGroupByID(groupID int64) (*Group, error) {
	return GetGroupByIDWithDB(DB(), groupID)
}

func GetGroupByIDWithDB(database *gorm.DB, groupID int64) (*Group, error) {
	var group Group
	err := database.Where("group_id = ? AND is_delete = ?", groupID, false).First(&group).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &group, nil
}

func IsActiveGroupMember(groupID, userID int64) (bool, error) {
	return IsActiveGroupMemberWithDB(DB(), groupID, userID)
}

func IsActiveGroupMemberWithDB(database *gorm.DB, groupID, userID int64) (bool, error) {
	var count int64
	err := database.Model(&GroupMember{}).
		Where("group_id = ? AND user_id = ?", groupID, userID).
		Count(&count).Error
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func GetActiveGroupMemberIDs(groupID int64) ([]int64, error) {
	return GetActiveGroupMemberIDsWithDB(DB(), groupID)
}

func GetActiveGroupMemberIDsWithDB(database *gorm.DB, groupID int64) ([]int64, error) {
	var userIDs []int64
	err := database.
		Model(&GroupMember{}).
		Where("group_id = ?", groupID).
		Order("user_id ASC").
		Pluck("user_id", &userIDs).Error
	return userIDs, err
}

func GetGroupMembers(groupID int64) ([]GroupMemberContact, error) {
	return GetGroupMembersWithDB(DB(), groupID)
}

func GetGroupMembersWithDB(database *gorm.DB, groupID int64) ([]GroupMemberContact, error) {
	var members []GroupMemberContact
	err := database.
		Table("group_members").
		Select("group_members.user_id, users.account, users.name, users.avatar, group_members.role, group_members.update_time").
		Joins("JOIN users ON users.id = group_members.user_id").
		Where("group_members.group_id = ?", groupID).
		Order("group_members.user_id ASC").
		Scan(&members).Error
	return members, err
}

func GetJoinedGroups(userID int64) ([]JoinedGroupContact, error) {
	return GetJoinedGroupsWithDB(DB(), userID)
}

func GetJoinedGroupsWithDB(database *gorm.DB, userID int64) ([]JoinedGroupContact, error) {
	var groups []JoinedGroupContact
	err := database.
		Table("group_members").
		Select("groups.group_id, groups.name AS group_name, groups.avatar, groups.owner_user_id, groups.update_time").
		Joins("JOIN groups ON groups.group_id = group_members.group_id").
		Where("group_members.user_id = ? AND groups.is_delete = ?", userID, false).
		Order("groups.group_id ASC").
		Scan(&groups).Error
	return groups, err
}

func RemoveUserFromGroupWithDB(database *gorm.DB, groupID, userID int64) (bool, bool, string, error) {
	now := utils.NowTime()
	groupExists := false
	removed := false

	err := database.Transaction(func(tx *gorm.DB) error {
		var group Group
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("group_id = ? AND is_delete = ?", groupID, false).
			First(&group).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		groupExists = true

		var member GroupMember
		err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("group_id = ? AND user_id = ?", groupID, userID).
			First(&member).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}

		if group.OwnerUserID == userID {
			var successor GroupMember
			err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("group_id = ? AND user_id <> ?", groupID, userID).
				Order("CASE WHEN role = 'admin' THEN 0 ELSE 1 END").
				Order("update_time ASC").
				Order("user_id ASC").
				First(&successor).Error
			switch {
			case errors.Is(err, gorm.ErrRecordNotFound):
				if err := tx.Model(&Group{}).
					Where("group_id = ? AND is_delete = ?", groupID, false).
					Updates(map[string]interface{}{"is_delete": true, "update_time": now}).Error; err != nil {
					return err
				}
			case err != nil:
				return err
			default:
				if err := tx.Model(&Group{}).
					Where("group_id = ? AND is_delete = ?", groupID, false).
					Updates(map[string]interface{}{"owner_user_id": successor.UserID, "update_time": now}).Error; err != nil {
					return err
				}
				if err := tx.Model(&GroupMember{}).
					Where("group_id = ? AND user_id = ?", groupID, successor.UserID).
					Updates(groupMemberRoleUpdates("owner", now)).Error; err != nil {
					return err
				}
			}
		} else if err := tx.Model(&Group{}).
			Where("group_id = ? AND is_delete = ?", groupID, false).
			Update("update_time", now).Error; err != nil {
			return err
		}

		result := tx.Where("group_id = ? AND user_id = ?", groupID, userID).Delete(&GroupMember{})
		if result.Error != nil {
			return result.Error
		}
		removed = result.RowsAffected > 0
		return nil
	})
	if err != nil {
		return groupExists, false, "", err
	}
	return groupExists, removed, now, nil
}

func UpdateGroupAvatarWithDB(database *gorm.DB, groupID int64, avatar string) (bool, string, error) {
	now := utils.NowTime()
	result := database.Model(&Group{}).
		Where("group_id = ? AND is_delete = ?", groupID, false).
		Updates(map[string]interface{}{
			"avatar":      avatar,
			"update_time": now,
		})
	if result.Error != nil {
		return false, "", result.Error
	}
	return result.RowsAffected > 0, now, nil
}
