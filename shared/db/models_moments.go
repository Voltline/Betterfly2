package db

import "time"

type MomentPost struct {
	ID                 int64 `gorm:"primaryKey;autoIncrement"`
	AuthorUserID       int64
	ClientPostID       string
	RequestFingerprint string
	Content            string
	Visibility         string
	CreatedAt          time.Time
	DeletedAt          *time.Time
}

type MomentAsset struct {
	ID            int64 `gorm:"primaryKey;autoIncrement"`
	OwnerUserID   int64
	ClientAssetID string
	PostID        *int64
	Position      int
	Hash          string
	Size          int64
	MIME          string
	Width         int
	Height        int
	StagingKey    string
	StorageKey    string
	Status        string
	CreatedAt     time.Time
}

type MomentComment struct {
	ID                 int64 `gorm:"primaryKey;autoIncrement"`
	PostID             int64
	AuthorUserID       int64
	ClientCommentID    string
	RequestFingerprint string
	ReplyToCommentID   *int64
	Content            string
	CreatedAt          time.Time
	DeletedAt          *time.Time
}

type MomentLike struct {
	PostID    int64 `gorm:"primaryKey;autoIncrement:false"`
	UserID    int64 `gorm:"primaryKey;autoIncrement:false"`
	CreatedAt time.Time
}
