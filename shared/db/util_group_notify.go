package db

import (
	"Betterfly2/shared/utils"
	"gorm.io/gorm"
)

// Updates only the caller's membership. Leaving and rejoining resets the preference.
func UpdateGroupNotifyWithDB(database *gorm.DB, userID, groupID int64, isNotify bool) (string, error) {
	if userID <= 0 || groupID <= 0 {
		return "", ErrRelationshipNotFound
	}
	now := utils.NowTime()
	result := database.Model(&GroupMember{}).
		Where("group_id = ? AND user_id = ? AND EXISTS (SELECT 1 FROM groups WHERE groups.group_id = group_members.group_id AND groups.is_delete = FALSE)", groupID, userID).
		Updates(map[string]any{"notifications_muted": !isNotify, "update_time": now})
	if result.Error != nil {
		return "", result.Error
	}
	if result.RowsAffected != 1 {
		return "", ErrRelationshipNotFound
	}
	return now, nil
}
