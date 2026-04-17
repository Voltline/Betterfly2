package db

import (
	"Betterfly2/shared/utils"
	"errors"

	"gorm.io/gorm"
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

func CreateGroupWithOwner(ownerUserID, groupID int64, groupName string) (bool, string, error) {
	now := utils.NowTime()
	alreadyExists := false

	err := DB().Transaction(func(tx *gorm.DB) error {
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
			return tx.Create(&GroupMember{
				GroupID:    groupID,
				UserID:     ownerUserID,
				Role:       "owner",
				UpdateTime: now,
			}).Error
		}
		if err != nil {
			return err
		}

		return tx.Model(&GroupMember{}).
			Where("group_id = ? AND user_id = ?", groupID, ownerUserID).
			Updates(map[string]interface{}{
				"role":        "owner",
				"update_time": now,
			}).Error
	})

	return alreadyExists, now, err
}

func GetGroupByID(groupID int64) (*Group, error) {
	var group Group
	err := DB().Where("group_id = ? AND is_delete = ?", groupID, false).First(&group).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &group, nil
}

func IsActiveGroupMember(groupID, userID int64) (bool, error) {
	var count int64
	err := DB().Model(&GroupMember{}).
		Where("group_id = ? AND user_id = ?", groupID, userID).
		Count(&count).Error
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func AddUserToGroup(groupID, userID int64) (bool, bool, string, error) {
	now := utils.NowTime()
	group, err := GetGroupByID(groupID)
	if err != nil {
		return false, false, "", err
	}
	if group == nil {
		return false, false, "", nil
	}

	added := false
	err = DB().Transaction(func(tx *gorm.DB) error {
		var member GroupMember
		err := tx.Where("group_id = ? AND user_id = ?", groupID, userID).First(&member).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			added = true
			return tx.Create(&GroupMember{
				GroupID:    groupID,
				UserID:     userID,
				Role:       "member",
				UpdateTime: now,
			}).Error
		}
		if err != nil {
			return err
		}
		return nil
	})

	return true, added, now, err
}

func GetActiveGroupMemberIDs(groupID int64) ([]int64, error) {
	var userIDs []int64
	err := DB().
		Model(&GroupMember{}).
		Where("group_id = ?", groupID).
		Order("user_id ASC").
		Pluck("user_id", &userIDs).Error
	return userIDs, err
}

func GetGroupMembers(groupID int64) ([]GroupMemberContact, error) {
	var members []GroupMemberContact
	err := DB().
		Table("group_members").
		Select("group_members.user_id, users.account, users.name, users.avatar, group_members.role, group_members.update_time").
		Joins("JOIN users ON users.id = group_members.user_id").
		Where("group_members.group_id = ?", groupID).
		Order("group_members.user_id ASC").
		Scan(&members).Error
	return members, err
}

func GetJoinedGroups(userID int64) ([]JoinedGroupContact, error) {
	var groups []JoinedGroupContact
	err := DB().
		Table("group_members").
		Select("groups.group_id, groups.name AS group_name, groups.avatar, groups.owner_user_id, groups.update_time").
		Joins("JOIN groups ON groups.group_id = group_members.group_id").
		Where("group_members.user_id = ? AND groups.is_delete = ?", userID, false).
		Order("groups.group_id ASC").
		Scan(&groups).Error
	return groups, err
}

func RemoveUserFromGroup(groupID, userID int64) (bool, bool, string, error) {
	now := utils.NowTime()
	group, err := GetGroupByID(groupID)
	if err != nil {
		return false, false, "", err
	}
	if group == nil {
		return false, false, "", nil
	}

	result := DB().Where("group_id = ? AND user_id = ?", groupID, userID).Delete(&GroupMember{})
	if result.Error != nil {
		return true, false, "", result.Error
	}
	return true, result.RowsAffected > 0, now, nil
}

func UpdateGroupAvatar(groupID int64, avatar string) (bool, string, error) {
	now := utils.NowTime()
	result := DB().Model(&Group{}).
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
