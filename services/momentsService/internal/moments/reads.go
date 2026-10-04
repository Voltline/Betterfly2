package moments

import (
	"Betterfly2/shared/db"
	"context"
	"fmt"

	"gorm.io/gorm"
)

func authors(q *gorm.DB, ids []int64) (map[int64]Author, error) {
	result := map[int64]Author{}
	if len(ids) == 0 {
		return result, nil
	}
	var users []db.User
	if err := q.Select("id", "name", "avatar").Where("id IN ?", ids).Find(&users).Error; err != nil {
		return nil, err
	}
	for _, u := range users {
		result[u.ID] = Author{id(u.ID), u.Name, u.Avatar}
	}
	return result, nil
}
func authorView(people map[int64]Author, user int64) Author {
	if value, ok := people[user]; ok {
		return value
	}
	return Author{ID: id(user), Name: "已注销用户"}
}
func commentView(row db.MomentComment, people map[int64]Author, viewer, owner int64) Comment {
	item := Comment{ID: id(row.ID), Author: authorView(people, row.AuthorUserID), Content: row.Content, CreatedAt: row.CreatedAt.UTC(), Deleted: row.DeletedAt != nil, CanDelete: row.DeletedAt == nil && (viewer == row.AuthorUserID || viewer == owner)}
	if row.ReplyToCommentID != nil {
		item.ReplyToCommentID = id(*row.ReplyToCommentID)
	}
	if item.Deleted {
		item.Content = ""
	}
	return item
}

// A page uses fixed-count bulk queries, including bounded interaction previews.
func hydrate(q *gorm.DB, viewer int64, rows []db.MomentPost) ([]Post, error) {
	result := make([]Post, 0, len(rows))
	if len(rows) == 0 {
		return result, nil
	}
	ids, users := make([]int64, 0, len(rows)), make([]int64, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
		users = append(users, row.AuthorUserID)
	}
	var assets []db.MomentAsset
	if err := q.Where("post_id IN ?", ids).Order("post_id, position").Find(&assets).Error; err != nil {
		return nil, err
	}
	type stats struct {
		PostID int64
		Count  int64
		Liked  int64
	}
	var likes, comments []stats
	if err := q.Model(&db.MomentLike{}).Select("post_id, COUNT(*) AS count, MAX(CASE WHEN user_id = ? THEN 1 ELSE 0 END) AS liked", viewer).Where("post_id IN ?", ids).Group("post_id").Scan(&likes).Error; err != nil {
		return nil, err
	}
	if err := q.Model(&db.MomentComment{}).Select("post_id, COUNT(*) AS count").Where("post_id IN ? AND deleted_at IS NULL", ids).Group("post_id").Scan(&comments).Error; err != nil {
		return nil, err
	}
	var previewLikes []db.MomentLike
	if err := q.Raw(`SELECT post_id, user_id, created_at FROM (SELECT *, row_number() OVER (PARTITION BY post_id ORDER BY created_at, user_id) AS n FROM moment_likes WHERE post_id IN ?) ranked WHERE n <= 6 ORDER BY post_id, created_at, user_id`, ids).Scan(&previewLikes).Error; err != nil {
		return nil, err
	}
	var previewComments []db.MomentComment
	if err := q.Raw(`SELECT id, post_id, author_user_id, content, reply_to_comment_id, created_at FROM (SELECT *, row_number() OVER (PARTITION BY post_id ORDER BY created_at, id) AS n FROM moment_comments WHERE post_id IN ? AND deleted_at IS NULL) ranked WHERE n <= 2 ORDER BY post_id, created_at, id`, ids).Scan(&previewComments).Error; err != nil {
		return nil, err
	}
	for _, row := range previewLikes {
		users = append(users, row.UserID)
	}
	for _, row := range previewComments {
		users = append(users, row.AuthorUserID)
	}
	people, err := authors(q, users)
	if err != nil {
		return nil, err
	}
	positions := map[int64]int{}
	for _, row := range rows {
		positions[row.ID] = len(result)
		result = append(result, Post{ID: id(row.ID), Author: authorView(people, row.AuthorUserID), Content: row.Content, Visibility: row.Visibility, CreatedAt: row.CreatedAt.UTC(), Assets: []Asset{}, CanDelete: row.AuthorUserID == viewer, LikesPreview: []Author{}, CommentsPreview: []Comment{}})
	}
	for _, row := range assets {
		i := positions[*row.PostID]
		result[i].Assets = append(result[i].Assets, Asset{id(row.ID), row.Width, row.Height})
	}
	for _, row := range likes {
		i := positions[row.PostID]
		result[i].LikeCount = row.Count
		result[i].LikedByMe = row.Liked == 1
	}
	for _, row := range comments {
		result[positions[row.PostID]].CommentCount = row.Count
	}
	for _, row := range previewLikes {
		i := positions[row.PostID]
		result[i].LikesPreview = append(result[i].LikesPreview, authorView(people, row.UserID))
	}
	for _, row := range previewComments {
		i := positions[row.PostID]
		result[i].CommentsPreview = append(result[i].CommentsPreview, commentView(row, people, viewer, rows[i].AuthorUserID))
	}
	return result, nil
}
func (s *Service) Comments(ctx context.Context, viewer, postID int64, raw string, limit int) (Page[Comment], error) {
	scope := fmt.Sprintf("comments:%d:%d", viewer, postID)
	c, limit, err := pageArgs(raw, scope, limit)
	if err != nil {
		return Page[Comment]{}, err
	}
	q := s.db.WithContext(ctx)
	post, err := postFor(q, viewer, postID, false)
	if err != nil {
		return Page[Comment]{}, err
	}
	query := q.Where("post_id = ?", postID)
	if c.ID != 0 {
		query = query.Where("(created_at, id) > (?, ?)", c.Time, c.ID)
	}
	var rows []db.MomentComment
	if err := query.Order("created_at, id").Limit(limit + 1).Find(&rows).Error; err != nil {
		return Page[Comment]{}, err
	}
	page := Page[Comment]{Items: []Comment{}, HasMore: len(rows) > limit}
	if page.HasMore {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		page.NextCursor = nextCursor(scope, last.CreatedAt, last.ID)
	}
	users := make([]int64, 0, len(rows))
	for _, row := range rows {
		users = append(users, row.AuthorUserID)
	}
	people, err := authors(q, users)
	if err != nil {
		return page, err
	}
	for _, row := range rows {
		page.Items = append(page.Items, commentView(row, people, viewer, post.AuthorUserID))
	}
	return page, nil
}
func (s *Service) Likes(ctx context.Context, viewer, postID int64, raw string, limit int) (Page[Like], error) {
	scope := fmt.Sprintf("likes:%d:%d", viewer, postID)
	c, limit, err := pageArgs(raw, scope, limit)
	if err != nil {
		return Page[Like]{}, err
	}
	q := s.db.WithContext(ctx)
	if _, err := postFor(q, viewer, postID, false); err != nil {
		return Page[Like]{}, err
	}
	query := q.Where("post_id = ?", postID)
	if c.ID != 0 {
		query = query.Where("(created_at, user_id) > (?, ?)", c.Time, c.ID)
	}
	var rows []db.MomentLike
	if err := query.Order("created_at, user_id").Limit(limit + 1).Find(&rows).Error; err != nil {
		return Page[Like]{}, err
	}
	page := Page[Like]{Items: []Like{}, HasMore: len(rows) > limit}
	if page.HasMore {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		page.NextCursor = nextCursor(scope, last.CreatedAt, last.UserID)
	}
	users := make([]int64, 0, len(rows))
	for _, row := range rows {
		users = append(users, row.UserID)
	}
	people, err := authors(q, users)
	if err != nil {
		return page, err
	}
	for _, row := range rows {
		page.Items = append(page.Items, Like{authorView(people, row.UserID), row.CreatedAt.UTC()})
	}
	return page, nil
}
