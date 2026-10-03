package call

import (
	"Betterfly2/shared/db"
	"context"
	"gorm.io/gorm"
)

type GormGroupAuthorizer struct{ database *gorm.DB }

func NewGormGroupAuthorizer(database *gorm.DB) *GormGroupAuthorizer {
	return &GormGroupAuthorizer{database: database}
}

func (a *GormGroupAuthorizer) Access(ctx context.Context, groupID, userID int64) (GroupAccess, error) {
	var result GroupAccess
	query := a.database.WithContext(ctx).Table("groups").
		Select("group_members.role, groups.name").
		Joins("JOIN group_members ON group_members.group_id = groups.group_id AND group_members.user_id = ?", userID).
		Joins("LEFT JOIN channel_settings ON channel_settings.group_id = groups.group_id").
		Where("groups.group_id = ? AND groups.is_delete = false AND channel_settings.group_id IS NULL", groupID).Scan(&result)
	if query.Error != nil {
		return result, query.Error
	}
	if query.RowsAffected == 0 {
		return result, ErrForbidden
	}
	if result.Role != db.GroupRoleOwner && result.Role != db.GroupRoleAdmin && result.Role != db.GroupRoleMember {
		return result, ErrForbidden
	}
	return result, nil
}

func (a *GormGroupAuthorizer) Members(ctx context.Context, groupID int64) ([]int64, error) {
	var members []int64
	err := a.database.WithContext(ctx).Table("groups").
		Joins("JOIN group_members ON group_members.group_id = groups.group_id").
		Joins("LEFT JOIN channel_settings ON channel_settings.group_id = groups.group_id").
		Where("groups.group_id = ? AND groups.is_delete = false AND channel_settings.group_id IS NULL", groupID).
		Where("group_members.role IN ?", []string{db.GroupRoleOwner, db.GroupRoleAdmin, db.GroupRoleMember}).
		Order("group_members.user_id").Pluck("group_members.user_id", &members).Error
	return members, err
}
