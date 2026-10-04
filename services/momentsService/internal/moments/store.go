package moments

import (
	"Betterfly2/shared/db"
	"context"
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Service struct {
	db    *gorm.DB
	media Media
}

func New(database *gorm.DB, media Media) *Service { return &Service{database, media} }
func (s *Service) Health(ctx context.Context) error {
	sql, err := s.db.DB()
	if err != nil {
		return err
	}
	if err := sql.PingContext(ctx); err != nil {
		return err
	}
	return s.media.Health(ctx)
}

// Every list and object read evaluates today's mutual friendship, never a cached grant.
const mutualFriend = `EXISTS (SELECT 1 FROM friends f JOIN friends r ON r.user_id = f.friend_id AND r.friend_id = f.user_id WHERE f.user_id = ? AND f.friend_id = moment_posts.author_user_id AND f.is_delete = false AND r.is_delete = false)`

func visible(q *gorm.DB, viewer int64) *gorm.DB {
	return q.Where("moment_posts.deleted_at IS NULL").Where("(moment_posts.author_user_id = ? OR (moment_posts.visibility = 'friends' AND "+mutualFriend+"))", viewer, viewer)
}
func postFor(q *gorm.DB, viewer, postID int64, lock bool) (*db.MomentPost, error) {
	var post db.MomentPost
	q = visible(q.Model(&db.MomentPost{}), viewer).Where("moment_posts.id = ?", postID)
	if lock {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if err := q.Take(&post).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &post, nil
}
func (s *Service) Me(ctx context.Context, viewer int64) (Author, error) {
	var user db.User
	err := s.db.WithContext(ctx).Select("id", "name", "avatar").Where("id = ?", viewer).Take(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Author{}, ErrNotFound
	}
	return Author{id(user.ID), user.Name, user.Avatar}, err
}
func (s *Service) Get(ctx context.Context, viewer, postID int64) (*Post, error) {
	q := s.db.WithContext(ctx)
	post, err := postFor(q, viewer, postID, false)
	if err != nil {
		return nil, err
	}
	items, err := hydrate(q, viewer, []db.MomentPost{*post})
	if err != nil {
		return nil, err
	}
	return &items[0], nil
}
func (s *Service) Feed(ctx context.Context, viewer, author int64, raw string, limit int) (Page[Post], error) {
	scope := fmt.Sprintf("posts:%d:%d", viewer, author)
	c, limit, err := pageArgs(raw, scope, limit)
	if err != nil {
		return Page[Post]{}, err
	}
	q := visible(s.db.WithContext(ctx).Model(&db.MomentPost{}), viewer)
	if author > 0 {
		q = q.Where("author_user_id = ?", author)
	}
	if c.ID != 0 {
		q = q.Where("(created_at, id) < (?, ?)", c.Time, c.ID)
	}
	var rows []db.MomentPost
	if err := q.Order("created_at DESC, id DESC").Limit(limit + 1).Find(&rows).Error; err != nil {
		return Page[Post]{}, err
	}
	page := Page[Post]{HasMore: len(rows) > limit}
	if page.HasMore {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		page.NextCursor = nextCursor(scope, last.CreatedAt, last.ID)
	}
	page.Items, err = hydrate(s.db.WithContext(ctx), viewer, rows)
	return page, err
}
func (s *Service) Create(ctx context.Context, viewer int64, input CreatePost) (*Post, error) {
	assetIDs, err := validatePost(input)
	if err != nil {
		return nil, err
	}
	// Normalize missing and empty attachment lists to the same request.
	input.AssetIDs = make([]string, len(assetIDs))
	for i, n := range assetIDs {
		input.AssetIDs[i] = id(n)
	}
	post := db.MomentPost{AuthorUserID: viewer, ClientPostID: input.ClientPostID, RequestFingerprint: fingerprint(input), Content: input.Content, Visibility: input.Visibility, CreatedAt: now()}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "author_user_id"}, {Name: "client_post_id"}}, DoNothing: true}).Create(&post)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			if err := tx.Where("author_user_id = ? AND client_post_id = ?", viewer, input.ClientPostID).Take(&post).Error; err != nil {
				return err
			}
			if post.DeletedAt != nil {
				return ErrGone
			}
			if post.RequestFingerprint != fingerprint(input) {
				return ErrConflict
			}
			return nil
		}
		if len(assetIDs) == 0 {
			return nil
		}
		var assets []db.MomentAsset
		// Stable lock order prevents reverse attachment orders from deadlocking.
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id IN ? AND owner_user_id = ?", assetIDs, viewer).Order("id").Find(&assets).Error; err != nil {
			return err
		}
		if len(assets) != len(assetIDs) {
			return ErrInvalid
		}
		for _, asset := range assets {
			if asset.Status != "verified" || asset.PostID != nil {
				return ErrConflict
			}
		}
		positions := strings.Builder{}
		positions.WriteString("CASE id")
		args := make([]any, 0, len(assetIDs)*2)
		for position, assetID := range assetIDs {
			positions.WriteString(" WHEN ? THEN ?::integer")
			args = append(args, assetID, position)
		}
		positions.WriteString(" END")
		return tx.Model(&db.MomentAsset{}).Where("id IN ?", assetIDs).Updates(map[string]any{"post_id": post.ID, "position": gorm.Expr(positions.String(), args...)}).Error
	})
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, viewer, post.ID)
}
func (s *Service) Delete(ctx context.Context, viewer, postID int64) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var post db.MomentPost
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND author_user_id = ?", postID, viewer).Take(&post).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		if post.DeletedAt != nil {
			return nil
		}
		deleted := now()
		if err := tx.Model(&post).Updates(map[string]any{"content": "", "deleted_at": deleted}).Error; err != nil {
			return err
		}
		return tx.Model(&db.MomentComment{}).Where("post_id = ?", postID).Updates(map[string]any{"content": "", "deleted_at": deleted}).Error
	})
}
func (s *Service) SetLike(ctx context.Context, viewer, postID int64, liked bool) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := postFor(tx, viewer, postID, true); err != nil {
			return err
		}
		if !liked {
			return tx.Where("post_id = ? AND user_id = ?", postID, viewer).Delete(&db.MomentLike{}).Error
		}
		return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&db.MomentLike{PostID: postID, UserID: viewer, CreatedAt: now()}).Error
	})
}
func (s *Service) AddComment(ctx context.Context, viewer, postID int64, input CreateComment) (*Comment, error) {
	if !validKey(input.ClientCommentID) || !validText(input.Content, 1000, 4<<10) || strings.TrimSpace(input.Content) == "" {
		return nil, ErrInvalid
	}
	var reply *int64
	if input.ReplyToCommentID != "" {
		n, err := ID(input.ReplyToCommentID)
		if err != nil {
			return nil, err
		}
		reply = &n
	}
	row := db.MomentComment{PostID: postID, AuthorUserID: viewer, ClientCommentID: input.ClientCommentID, RequestFingerprint: fingerprint(struct {
		Post  int64
		Input CreateComment
	}{postID, input}), ReplyToCommentID: reply, Content: input.Content, CreatedAt: now()}
	var owner int64
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		post, err := postFor(tx, viewer, postID, true)
		if err != nil {
			return err
		}
		owner = post.AuthorUserID
		var existing db.MomentComment
		lookup := tx.Where("author_user_id = ? AND client_comment_id = ?", viewer, input.ClientCommentID).Take(&existing).Error
		if lookup == nil {
			if existing.DeletedAt != nil {
				return ErrGone
			}
			if existing.RequestFingerprint != row.RequestFingerprint {
				return ErrConflict
			}
			row = existing
			return nil
		}
		if !errors.Is(lookup, gorm.ErrRecordNotFound) {
			return lookup
		}
		if reply != nil {
			var count int64
			if err := tx.Model(&db.MomentComment{}).Where("id = ? AND post_id = ? AND deleted_at IS NULL", *reply, postID).Count(&count).Error; err != nil {
				return err
			}
			if count != 1 {
				return ErrInvalid
			}
		}
		result := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "author_user_id"}, {Name: "client_comment_id"}}, DoNothing: true}).Create(&row)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			if err := tx.Where("author_user_id = ? AND client_comment_id = ?", viewer, input.ClientCommentID).Take(&row).Error; err != nil {
				return err
			}
			if row.DeletedAt != nil {
				return ErrGone
			}
			if row.RequestFingerprint != fingerprint(struct {
				Post  int64
				Input CreateComment
			}{postID, input}) {
				return ErrConflict
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	people, err := authors(s.db.WithContext(ctx), []int64{viewer})
	if err != nil {
		return nil, err
	}
	comment := commentView(row, people, viewer, owner)
	return &comment, nil
}
func (s *Service) DeleteComment(ctx context.Context, viewer, commentID int64) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var comment db.MomentComment
		if err := tx.Where("id = ?", commentID).Take(&comment).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		// Always lock the post first, matching comment creation and post deletion.
		var post db.MomentPost
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", comment.PostID).Take(&post).Error; err != nil {
			return err
		}
		if comment.AuthorUserID != viewer && post.AuthorUserID != viewer {
			return ErrNotFound
		}
		return tx.Model(&db.MomentComment{}).Where("id = ? AND deleted_at IS NULL", commentID).Updates(map[string]any{"content": "", "deleted_at": now()}).Error
	})
}
