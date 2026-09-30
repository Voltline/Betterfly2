package db

// ChannelSettings overlays an existing group. Ordinary groups have no row here.
// Membership, ownership and posts retain their existing tables and ID namespace.
type ChannelSettings struct {
	GroupID     int64   `gorm:"primaryKey;autoIncrement:false;index:idx_channels_public_id,priority:2"`
	Description string  `gorm:"type:varchar(1000)"`
	Username    *string `gorm:"type:varchar(32);uniqueIndex:uidx_channel_username"`
	IsPublic    bool    `gorm:"not null;index:idx_channels_public_id,priority:1"`
}
