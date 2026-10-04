package db

import "gorm.io/gorm"

// Keep v10 independent of future model edits: released migrations are immutable.
func migrateMomentsSchema(tx *gorm.DB) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS moment_posts (
 id bigserial PRIMARY KEY, author_user_id bigint NOT NULL, client_post_id varchar(128) NOT NULL,
 request_fingerprint varchar(64) NOT NULL, content text NOT NULL, visibility varchar(16) NOT NULL CHECK (visibility IN ('friends', 'private')),
 created_at timestamptz NOT NULL, deleted_at timestamptz,
 UNIQUE (author_user_id, client_post_id))`,
		`CREATE INDEX IF NOT EXISTS idx_moment_posts_author_page ON moment_posts (author_user_id, created_at DESC, id DESC) WHERE deleted_at IS NULL`,
		`CREATE INDEX IF NOT EXISTS idx_moment_posts_page ON moment_posts (created_at DESC, id DESC) WHERE deleted_at IS NULL`,
		`CREATE TABLE IF NOT EXISTS moment_assets (
 id bigserial PRIMARY KEY, owner_user_id bigint NOT NULL, client_asset_id varchar(128) NOT NULL,
 post_id bigint REFERENCES moment_posts(id), position integer NOT NULL DEFAULT 0,
 hash varchar(128) NOT NULL, size bigint NOT NULL, mime varchar(32) NOT NULL,
 width integer NOT NULL DEFAULT 0, height integer NOT NULL DEFAULT 0,
 staging_key varchar(255) NOT NULL, storage_key varchar(255) NOT NULL, status varchar(16) NOT NULL CHECK (status IN ('pending', 'verified')),
 created_at timestamptz NOT NULL, UNIQUE (owner_user_id, client_asset_id), UNIQUE (post_id, position))`,
		`CREATE TABLE IF NOT EXISTS moment_comments (
 id bigserial PRIMARY KEY, post_id bigint NOT NULL REFERENCES moment_posts(id), author_user_id bigint NOT NULL,
 client_comment_id varchar(128) NOT NULL, request_fingerprint varchar(64) NOT NULL,
 reply_to_comment_id bigint REFERENCES moment_comments(id), content text NOT NULL,
 created_at timestamptz NOT NULL, deleted_at timestamptz, UNIQUE (author_user_id, client_comment_id))`,
		`CREATE INDEX IF NOT EXISTS idx_moment_comments_page ON moment_comments (post_id, created_at, id)`,
		`CREATE TABLE IF NOT EXISTS moment_likes (
 post_id bigint NOT NULL REFERENCES moment_posts(id), user_id bigint NOT NULL,
 created_at timestamptz NOT NULL, PRIMARY KEY (post_id, user_id))`,
		`CREATE INDEX IF NOT EXISTS idx_moment_likes_page ON moment_likes (post_id, created_at, user_id)`,
	}
	for _, statement := range statements {
		if err := tx.Exec(statement).Error; err != nil {
			return err
		}
	}
	return nil
}
