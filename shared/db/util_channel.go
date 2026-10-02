package db

import (
	"Betterfly2/shared/utils"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrChannelInvalidArgument = errors.New("invalid channel argument")
	ErrChannelForbidden       = errors.New("channel operation forbidden")
	ErrChannelNotFound        = errors.New("channel not found")
	ErrChannelAlreadyExists   = errors.New("channel already exists")
	ErrChannelInvalidState    = errors.New("invalid channel state")
	channelUsernamePattern    = regexp.MustCompile(`^[a-z][a-z0-9_]{4,31}$`)
)

type ChannelView struct {
	GroupID            int64
	Name               string
	Avatar             string
	OwnerUserID        int64
	UpdateTime         string
	Description        string
	Username           string
	IsPublic           bool
	SubscriberCount    int64
	Subscribed         bool
	MyRole             string
	PinnedMessageID    int64
	NotificationsMuted bool
	DiscussionGroupID  int64
}

func NormalizeChannelUsername(value string) (string, error) {
	value = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(value), "@"))
	if value != "" && !channelUsernamePattern.MatchString(value) {
		return "", ErrChannelInvalidArgument
	}
	return value, nil
}

func ValidChannelText(value string, maximum int) bool {
	return utf8.ValidString(value) && utf8.RuneCountInString(value) <= maximum
}

func ChannelPageSize(size int32) (int, error) {
	if size < 0 {
		return 0, ErrChannelInvalidArgument
	}
	if size == 0 {
		return 50, nil
	}
	if size > 100 {
		return 100, nil
	}
	return int(size), nil
}

func GetChannelSettingsWithDB(database *gorm.DB, groupID int64) (*ChannelSettings, error) {
	var settings ChannelSettings
	err := database.Where("group_id = ?", groupID).First(&settings).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &settings, err
}

func channelViewQuery(database *gorm.DB, actorID int64) *gorm.DB {
	return database.Table("channel_settings").Select(`groups.group_id, groups.name, groups.avatar,
groups.owner_user_id, groups.update_time, channel_settings.description,
COALESCE(channel_settings.username, '') AS username, channel_settings.is_public,
(SELECT COUNT(*) FROM group_members WHERE group_id = groups.group_id) AS subscriber_count,
(viewer.user_id IS NOT NULL) AS subscribed, COALESCE(viewer.role, '') AS my_role,
channel_settings.pinned_message_id, COALESCE(viewer.notifications_muted, FALSE) AS notifications_muted,
COALESCE(channel_settings.discussion_group_id, 0) AS discussion_group_id`).
		Joins("JOIN groups ON groups.group_id = channel_settings.group_id").
		Joins("LEFT JOIN group_members AS viewer ON viewer.group_id = groups.group_id AND viewer.user_id = ?", actorID).
		Where("groups.is_delete = FALSE AND (channel_settings.is_public = TRUE OR viewer.user_id IS NOT NULL)")
}

func GetChannelWithDB(database *gorm.DB, actorID, channelID int64, username string) (*ChannelView, error) {
	if actorID <= 0 || channelID < 0 || (channelID == 0) == (username == "") {
		return nil, ErrChannelInvalidArgument
	}
	query := channelViewQuery(database, actorID)
	if channelID > 0 {
		query = query.Where("groups.group_id = ?", channelID)
	} else {
		query = query.Where("channel_settings.username = ?", username)
	}
	var view ChannelView
	if err := query.Limit(1).Scan(&view).Error; err != nil {
		return nil, err
	}
	if view.GroupID == 0 {
		return nil, ErrChannelNotFound
	}
	return &view, nil
}

func ListChannelsWithDB(database *gorm.DB, actorID int64, subscribedOnly bool, search string, cursor int64, size int) ([]ChannelView, error) {
	if actorID <= 0 || cursor < 0 || size < 1 || size > 100 || !ValidChannelText(search, 100) {
		return nil, ErrChannelInvalidArgument
	}
	query := channelViewQuery(database, actorID).Where("groups.group_id > ?", cursor)
	if subscribedOnly {
		query = query.Where("viewer.user_id IS NOT NULL")
	} else {
		query = query.Where("channel_settings.is_public = TRUE")
	}
	if search = strings.TrimSpace(search); search != "" {
		pattern := "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(search) + "%"
		query = query.Where("groups.name ILIKE ? OR channel_settings.username ILIKE ?", pattern, pattern)
	}
	var views []ChannelView
	err := query.Order("groups.group_id ASC").Limit(size + 1).Scan(&views).Error
	return views, err
}

func lockChannel(database *gorm.DB, channelID int64) (*Group, *ChannelSettings, error) {
	var group Group
	err := database.Clauses(clause.Locking{Strength: "UPDATE"}).Where("group_id = ? AND is_delete = FALSE", channelID).First(&group).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil, ErrChannelNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	settings, err := GetChannelSettingsWithDB(database, channelID)
	if err != nil {
		return nil, nil, err
	}
	if settings == nil {
		return nil, nil, ErrChannelNotFound
	}
	return &group, settings, nil
}

func requireChannelManager(database *gorm.DB, group *Group, actorID int64, ownerOnly, isPublic bool) error {
	role, err := groupRoleTx(database, group.GroupID, actorID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if !isPublic {
			return ErrChannelNotFound
		}
		return ErrChannelForbidden
	}
	if err != nil {
		return err
	}
	if !canManageGroup(role) || ownerOnly && (group.OwnerUserID != actorID || role != GroupRoleOwner) {
		return ErrChannelForbidden
	}
	return nil
}

func CreateChannelWithDB(database *gorm.DB, actorID, channelID int64, name, description, avatar, username string, isPublic bool) (*ChannelView, error) {
	name, description = strings.TrimSpace(name), strings.TrimSpace(description)
	username, err := NormalizeChannelUsername(username)
	if err != nil || actorID <= 0 || channelID <= 0 || name == "" || !ValidChannelText(name, 100) || !ValidChannelText(description, 1000) || !ValidChannelText(avatar, 255) {
		return nil, ErrChannelInvalidArgument
	}
	var view *ChannelView
	err = database.Transaction(func(tx *gorm.DB) error {
		user, err := GetUserByIDWithDB(tx, actorID)
		if err != nil {
			return err
		}
		if user == nil {
			return ErrChannelForbidden
		}
		now := utils.NowTime()
		group := Group{GroupID: channelID, Name: name, Avatar: avatar, OwnerUserID: actorID, UpdateTime: now}
		insert := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&group)
		if insert.Error != nil {
			return insert.Error
		}
		if insert.RowsAffected != 1 {
			return ErrChannelAlreadyExists
		}
		settings := ChannelSettings{GroupID: channelID, Description: description, IsPublic: isPublic}
		if username != "" {
			settings.Username = &username
		}
		if err := tx.Create(&settings).Error; err != nil {
			return channelConstraintError(err)
		}
		if err := tx.Create(newGroupMember(channelID, actorID, GroupRoleOwner, now)).Error; err != nil {
			return err
		}
		view, err = GetChannelWithDB(tx, actorID, channelID, "")
		return err
	})
	return view, err
}

func channelConstraintError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "uidx_channel_username" {
		return ErrChannelAlreadyExists
	}
	return err
}

// Updates come from validated optional protobuf fields; missing fields are not reset.
func UpdateChannelWithDB(database *gorm.DB, actorID, channelID int64, groupUpdates, settingsUpdates map[string]any) (*ChannelView, error) {
	var view *ChannelView
	err := database.Transaction(func(tx *gorm.DB) error {
		group, settings, err := lockChannel(tx, channelID)
		if err != nil {
			return err
		}
		_, changesVisibility := settingsUpdates["is_public"]
		if err := requireChannelManager(tx, group, actorID, changesVisibility, settings.IsPublic); err != nil {
			return err
		}
		if len(settingsUpdates) > 0 {
			if err := tx.Model(&ChannelSettings{}).Where("group_id = ?", channelID).Updates(settingsUpdates).Error; err != nil {
				return channelConstraintError(err)
			}
		}
		groupUpdates["update_time"] = utils.NowTime()
		if err := tx.Model(&Group{}).Where("group_id = ?", channelID).Updates(groupUpdates).Error; err != nil {
			return err
		}
		view, err = GetChannelWithDB(tx, actorID, channelID, "")
		return err
	})
	return view, err
}

func SetChannelPinWithDB(database *gorm.DB, actorID, channelID, messageID int64) (*ChannelView, error) {
	if actorID <= 0 || channelID <= 0 || messageID < 0 {
		return nil, ErrChannelInvalidArgument
	}
	var view *ChannelView
	err := database.Transaction(func(tx *gorm.DB) error {
		group, settings, err := lockChannel(tx, channelID)
		if err != nil {
			return err
		}
		if err := requireChannelManager(tx, group, actorID, false, settings.IsPublic); err != nil {
			return err
		}
		if messageID > 0 {
			var message Message
			err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&message, "message_id = ?", messageID).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrChannelNotFound
			}
			if err != nil {
				return err
			}
			if !message.IsGroup || message.ToUserID != channelID {
				return ErrChannelNotFound
			}
			if message.IsRecalled {
				return ErrChannelInvalidState
			}
		}
		if err := tx.Model(&ChannelSettings{}).Where("group_id = ?", channelID).Update("pinned_message_id", messageID).Error; err != nil {
			return err
		}
		if err := tx.Model(&Group{}).Where("group_id = ?", channelID).Update("update_time", utils.NowTime()).Error; err != nil {
			return err
		}
		view, err = GetChannelWithDB(tx, actorID, channelID, "")
		return err
	})
	return view, err
}

func SubscribeChannelWithDB(database *gorm.DB, actorID, channelID int64) (*ChannelView, error) {
	if actorID <= 0 || channelID <= 0 {
		return nil, ErrChannelInvalidArgument
	}
	var view *ChannelView
	err := database.Transaction(func(tx *gorm.DB) error {
		_, settings, err := lockChannel(tx, channelID)
		if err != nil {
			return err
		}
		if !settings.IsPublic {
			member, err := IsActiveGroupMemberWithDB(tx, channelID, actorID)
			if err != nil {
				return err
			}
			if !member {
				return ErrChannelNotFound
			}
		}
		user, err := GetUserByIDWithDB(tx, actorID)
		if err != nil {
			return err
		}
		if user == nil {
			return ErrChannelForbidden
		}
		now := utils.NowTime()
		insert := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(newGroupMember(channelID, actorID, GroupRoleMember, now))
		if insert.Error != nil {
			return insert.Error
		}
		if insert.RowsAffected > 0 {
			if err := tx.Model(&Group{}).Where("group_id = ?", channelID).Update("update_time", now).Error; err != nil {
				return err
			}
		}
		view, err = GetChannelWithDB(tx, actorID, channelID, "")
		return err
	})
	return view, err
}

func UnsubscribeChannelWithDB(database *gorm.DB, actorID, channelID int64) error {
	if actorID <= 0 || channelID <= 0 {
		return ErrChannelInvalidArgument
	}
	return database.Transaction(func(tx *gorm.DB) error {
		group, settings, err := lockChannel(tx, channelID)
		if err != nil {
			return err
		}
		if !settings.IsPublic {
			member, err := IsActiveGroupMemberWithDB(tx, channelID, actorID)
			if err != nil {
				return err
			}
			if !member {
				return ErrChannelNotFound
			}
		}
		if group.OwnerUserID == actorID {
			return ErrChannelInvalidState // Transfer ownership or delete explicitly.
		}
		deleted := tx.Where("group_id = ? AND user_id = ?", channelID, actorID).Delete(&GroupMember{})
		if deleted.Error != nil || deleted.RowsAffected == 0 {
			return deleted.Error
		}
		return tx.Model(&Group{}).Where("group_id = ?", channelID).Update("update_time", utils.NowTime()).Error
	})
}

func DeleteChannelWithDB(database *gorm.DB, actorID, channelID int64) error {
	if actorID <= 0 || channelID <= 0 {
		return ErrChannelInvalidArgument
	}
	return database.Transaction(func(tx *gorm.DB) error {
		group, settings, err := lockChannel(tx, channelID)
		if err != nil {
			return err
		}
		if err := requireChannelManager(tx, group, actorID, true, settings.IsPublic); err != nil {
			return err
		}
		now := utils.NowTime()
		if err := tx.Model(&Group{}).Where("group_id = ?", channelID).Updates(map[string]any{"is_delete": true, "update_time": now}).Error; err != nil {
			return err
		}
		if err := tx.Where("group_id = ?", channelID).Delete(&GroupMember{}).Error; err != nil {
			return err
		}
		if err := tx.Model(&ChannelSettings{}).Where("group_id = ?", channelID).Updates(map[string]any{"username": nil, "discussion_group_id": nil}).Error; err != nil {
			return err
		}
		return tx.Model(&RelationshipRequest{}).Where("group_id = ? AND status = ?", channelID, RequestStatusPending).
			Updates(map[string]any{"status": RequestStatusCancelled, "active_key": nil, "resolved_by": actorID, "resolved_at": relationshipTime(relationshipNow())}).Error
	})
}

func ListChannelMembersWithDB(database *gorm.DB, actorID, channelID, cursor int64, size int) ([]GroupMemberContact, error) {
	if actorID <= 0 || channelID <= 0 || cursor < 0 || size < 1 || size > 100 {
		return nil, ErrChannelInvalidArgument
	}
	view, err := GetChannelWithDB(database, actorID, channelID, "")
	if err != nil {
		return nil, err
	}
	if !canManageGroup(view.MyRole) {
		return nil, ErrChannelForbidden
	}
	var members []GroupMemberContact
	err = database.Table("group_members").Select("group_members.user_id, users.name, users.avatar, group_members.role, group_members.joined_at").
		Joins("JOIN users ON users.id = group_members.user_id").
		Where("group_members.group_id = ? AND group_members.user_id > ?", channelID, cursor).
		Order("group_members.user_id ASC").Limit(size + 1).Scan(&members).Error
	return members, err
}

func CanPublishGroupMessageWithDB(database *gorm.DB, groupID, actorID int64) (bool, error) {
	var count int64
	err := database.Model(&GroupMember{}).
		Joins("LEFT JOIN channel_settings ON channel_settings.group_id = group_members.group_id").
		Joins("JOIN groups ON groups.group_id = group_members.group_id").
		Where("group_members.group_id = ? AND group_members.user_id = ? AND groups.is_delete = FALSE", groupID, actorID).
		Where("channel_settings.group_id IS NULL OR group_members.role IN ?", []string{GroupRoleOwner, GroupRoleAdmin}).Count(&count).Error
	return count > 0, err
}

func GetChannelHistoryWithDB(database *gorm.DB, actorID, channelID, beforeID int64, size int) (*ChannelView, []Message, error) {
	if actorID <= 0 || channelID <= 0 || beforeID < 0 || size < 1 || size > 100 {
		return nil, nil, ErrChannelInvalidArgument
	}
	view, err := GetChannelWithDB(database, actorID, channelID, "")
	if err != nil {
		return nil, nil, err
	}
	query := database.Where("is_group = TRUE AND to_user_id = ?", channelID)
	if beforeID > 0 {
		query = query.Where("message_id < ?", beforeID)
	}
	var messages []Message
	err = query.Order("message_id DESC").Limit(size + 1).Find(&messages).Error
	return view, messages, err
}
