package db

import (
	"Betterfly2/shared/utils"
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strings"
)

func migrateDiscussionSchema(tx *gorm.DB) error {
	for _, sql := range []string{
		`ALTER TABLE channel_settings ADD COLUMN IF NOT EXISTS discussion_group_id bigint`,
		`ALTER TABLE messages ADD COLUMN IF NOT EXISTS discussion_root_message_id bigint NOT NULL DEFAULT 0`,
		`ALTER TABLE messages ADD COLUMN IF NOT EXISTS source_channel_message_id bigint NOT NULL DEFAULT 0`,
		`ALTER TABLE messages ADD COLUMN IF NOT EXISTS discussion_closed boolean NOT NULL DEFAULT false`,
		`CREATE UNIQUE INDEX IF NOT EXISTS uidx_channel_discussion_group ON channel_settings(discussion_group_id) WHERE discussion_group_id IS NOT NULL`,
		`CREATE UNIQUE INDEX IF NOT EXISTS uidx_discussion_source ON messages(source_channel_message_id) WHERE source_channel_message_id > 0`,
		`CREATE INDEX IF NOT EXISTS idx_discussion_replies ON messages(to_user_id, discussion_root_message_id, message_id) WHERE discussion_root_message_id > 0`,
	} {
		if err := tx.Exec(sql).Error; err != nil {
			return err
		}
	}
	return nil
}

// Lock both groups in ID order. Binding and posting use the channel group lock
// so unlink cannot race with the creation of a new announcement thread.
func SetDiscussionGroupWithDB(database *gorm.DB, actorID, channelID, groupID int64) (*ChannelView, error) {
	if actorID <= 0 || channelID <= 0 || groupID < 0 || channelID == groupID {
		return nil, ErrChannelInvalidArgument
	}
	var view *ChannelView
	err := database.Transaction(func(tx *gorm.DB) error {
		var groups []Group
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("group_id IN ? AND is_delete = FALSE", []int64{channelID, groupID}).Order("group_id ASC").Find(&groups).Error; err != nil {
			return err
		}
		byID := make(map[int64]Group, len(groups))
		for _, group := range groups {
			byID[group.GroupID] = group
		}
		channelGroup, ok := byID[channelID]
		if !ok {
			return ErrChannelNotFound
		}
		settings, err := GetChannelSettingsWithDB(tx, channelID)
		if err != nil {
			return err
		}
		if settings == nil {
			return ErrChannelNotFound
		}
		if err := requireChannelManager(tx, &channelGroup, actorID, true, settings.IsPublic); err != nil {
			return err
		}
		var target any
		if groupID > 0 {
			group, ok := byID[groupID]
			if !ok {
				return ErrChannelNotFound
			}
			other, err := GetChannelSettingsWithDB(tx, groupID)
			if err != nil {
				return err
			}
			if other != nil {
				return ErrChannelInvalidArgument
			}
			role, err := groupRoleTx(tx, groupID, actorID)
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrChannelForbidden
			}
			if err != nil {
				return err
			}
			if group.OwnerUserID != actorID || role != GroupRoleOwner {
				return ErrChannelForbidden
			}
			var count int64
			if err := tx.Model(&ChannelSettings{}).Where("discussion_group_id = ? AND group_id <> ?", groupID, channelID).Count(&count).Error; err != nil {
				return err
			}
			if count > 0 {
				return ErrChannelAlreadyExists
			}
			target = groupID
		}
		if settings.DiscussionGroupID != nil && *settings.DiscussionGroupID != groupID {
			if err := tx.Model(&Message{}).Where(`source_channel_message_id IN (SELECT message_id FROM messages WHERE to_user_id = ? AND is_group = TRUE) AND source_channel_message_id > 0 AND discussion_closed = FALSE`, channelID).Update("discussion_closed", true).Error; err != nil {
				return err
			}
		}
		if err := tx.Model(&ChannelSettings{}).Where("group_id = ?", channelID).Update("discussion_group_id", target).Error; err != nil {
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

// Cards contain only a reference, never a second copy of channel content.
// Caller owns the Inbox/Outbox transaction, including both inserts and events.
func CreateDiscussionRootWithDB(tx *gorm.DB, post *Message) (*Message, error) {
	if !post.IsGroup {
		return nil, nil
	}
	settings, err := GetChannelSettingsWithDB(tx, post.ToUserID)
	if err != nil {
		return nil, err
	}
	if settings == nil || settings.DiscussionGroupID == nil {
		return nil, nil
	}
	var group Group
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&group, "group_id = ?", post.ToUserID).Error; err != nil {
		return nil, err
	}
	if group.IsDelete {
		return nil, ErrChannelNotFound
	}
	settings, err = GetChannelSettingsWithDB(tx, post.ToUserID)
	if err != nil {
		return nil, err
	}
	if settings == nil || settings.DiscussionGroupID == nil {
		return nil, nil
	}
	var active int64
	if err := tx.Model(&Group{}).Where("group_id = ? AND is_delete = FALSE", *settings.DiscussionGroupID).Count(&active).Error; err != nil {
		return nil, err
	}
	if active == 0 {
		return nil, nil
	}
	root := &Message{FromUserID: post.FromUserID, ToUserID: *settings.DiscussionGroupID, IsGroup: true, MessageType: "text", Timestamp: post.Timestamp, SourceChannelMessageID: post.MessageID}
	if err := tx.Create(root).Error; err != nil {
		return nil, err
	}
	root.DiscussionRootMessageID = root.MessageID
	if err := tx.Model(&Message{}).Where("message_id IN ?", []int64{post.MessageID, root.MessageID}).Update("discussion_root_message_id", root.MessageID).Error; err != nil {
		return nil, err
	}
	post.DiscussionRootMessageID = root.MessageID
	return root, nil
}

type DiscussionView struct {
	Root       Message
	Post       Message
	Joined     bool
	Closed     bool
	ReplyCount int64
}

func GetDiscussionWithDB(database *gorm.DB, actorID, rootID int64) (*DiscussionView, error) {
	root, err := GetMessageByIDWithDB(database, rootID)
	if err != nil {
		return nil, err
	}
	if root == nil || !root.IsGroup || root.SourceChannelMessageID <= 0 || root.DiscussionRootMessageID != root.MessageID {
		return nil, ErrChannelNotFound
	}
	post, err := GetMessageByIDWithDB(database, root.SourceChannelMessageID)
	if err != nil {
		return nil, err
	}
	if post == nil || !post.IsGroup || post.DiscussionRootMessageID != rootID {
		return nil, ErrChannelNotFound
	}
	channel, err := GetChannelWithDB(database, actorID, post.ToUserID, "")
	if err != nil {
		return nil, err
	}
	var active int64
	if err := database.Model(&Group{}).Where("group_id = ? AND is_delete = FALSE", root.ToUserID).Count(&active).Error; err != nil {
		return nil, err
	}
	if active == 0 {
		return nil, ErrChannelNotFound
	}
	joined, err := IsActiveGroupMemberWithDB(database, root.ToUserID, actorID)
	if err != nil {
		return nil, err
	}
	view := &DiscussionView{Root: *root, Post: *post, Joined: joined, Closed: root.DiscussionClosed || root.IsRecalled || post.IsRecalled || channel.DiscussionGroupID != root.ToUserID}
	if joined {
		if err := database.Model(&Message{}).Where("to_user_id = ? AND is_group = TRUE AND discussion_root_message_id = ? AND source_channel_message_id = 0 AND is_recalled = FALSE", root.ToUserID, rootID).Count(&view.ReplyCount).Error; err != nil {
			return nil, err
		}
	}
	return view, nil
}

// Only this authorization branch ignores JoinedAt. It never authorizes the
// rest of the group's history, and private channel membership is rechecked.
func CanReadDiscussionMessageWithDB(database *gorm.DB, actorID int64, message *Message) (bool, error) {
	var count int64
	err := database.Table("messages AS m").Joins("JOIN group_members gm ON gm.group_id = m.to_user_id AND gm.user_id = ?", actorID).
		Where("m.message_id = ? AND m.is_group = TRUE AND "+DiscussionReadPredicate, message.MessageID, actorID).Count(&count).Error
	return count > 0, err
}

func MessageRecipientIDsWithDB(database *gorm.DB, messageID int64) ([]int64, error) {
	m, err := GetMessageByIDWithDB(database, messageID)
	if err != nil {
		return nil, err
	}
	if m == nil {
		return nil, nil
	}
	channel, err := GetChannelSettingsWithDB(database, m.ToUserID)
	if err != nil {
		return nil, err
	}
	if channel != nil || m.DiscussionRootMessageID == 0 {
		return GetActiveGroupMemberIDsWithDB(database, m.ToUserID)
	}
	var ids []int64
	err = database.Table("messages AS m").Select("gm.user_id").Joins("JOIN group_members gm ON gm.group_id = m.to_user_id").
		Where("m.message_id = ? AND "+strings.ReplaceAll(DiscussionReadPredicate, "?", "gm.user_id"), messageID).Order("gm.user_id ASC").Scan(&ids).Error
	return ids, err
}

func ValidateDiscussionCommentWithDB(tx *gorm.DB, actorID, groupID, rootID, replyID int64) error {
	// Serialize comment insertion with root/source recall and unlink. Lock group
	// before source before root, the same order as publication and recall.
	var pair struct{ ChannelID int64 }
	if err := tx.Raw(`SELECT source.to_user_id AS channel_id FROM messages root JOIN messages source ON source.message_id = root.source_channel_message_id WHERE root.message_id = ?`, rootID).Scan(&pair).Error; err != nil {
		return err
	}
	if pair.ChannelID <= 0 {
		return ErrInvalidReply
	}
	var group Group
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&group, "group_id = ?", pair.ChannelID).Error; err != nil {
		return err
	}
	var locked []Message
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("message_id = ? OR message_id = (SELECT source_channel_message_id FROM messages WHERE message_id = ?)", rootID, rootID).Order("message_id ASC").Find(&locked).Error; err != nil {
		return err
	}
	view, err := GetDiscussionWithDB(tx, actorID, rootID)
	if errors.Is(err, ErrChannelNotFound) {
		return ErrInvalidReply
	}
	if err != nil {
		return err
	}
	if !view.Joined || view.Closed || view.Root.ToUserID != groupID {
		return ErrInvalidReply
	}
	if replyID > 0 {
		reply, err := GetMessageByIDWithDB(tx, replyID)
		if err != nil {
			return err
		}
		if reply == nil || !reply.IsGroup || reply.ToUserID != groupID || reply.DiscussionRootMessageID != rootID {
			return ErrInvalidReply
		}
	}
	return nil
}

// Suitable for query, realtime and push eligibility, without an N+1 loop.
// Alias m must refer to a messages row; ? is the authenticated recipient ID.
const DiscussionReadPredicate = `EXISTS (
 SELECT 1 FROM messages root JOIN messages source ON source.message_id = root.source_channel_message_id
 JOIN channel_settings origin ON origin.group_id = source.to_user_id
 JOIN groups origin_group ON origin_group.group_id = origin.group_id AND origin_group.is_delete = FALSE
 JOIN groups discussion_group ON discussion_group.group_id = root.to_user_id AND discussion_group.is_delete = FALSE
 WHERE root.message_id = m.discussion_root_message_id AND root.discussion_root_message_id = root.message_id
   AND root.to_user_id = m.to_user_id AND root.is_group = TRUE AND source.is_group = TRUE
   AND source.discussion_root_message_id = root.message_id
   AND (origin.is_public = TRUE OR EXISTS (SELECT 1 FROM group_members reader WHERE reader.group_id = origin.group_id AND reader.user_id = ?))
)`
