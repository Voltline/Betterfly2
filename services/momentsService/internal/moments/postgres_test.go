package moments

import (
	"Betterfly2/shared/db"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func postgresFixture(t *testing.T) (*Service, *gorm.DB) {
	t.Helper()
	dsn := os.Getenv("BETTERFLY_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("requires isolated BETTERFLY_TEST_POSTGRES_DSN")
	}
	base, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("moments_%d", time.Now().UnixNano())
	if err := base.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { base.Exec("DROP SCHEMA " + schema + " CASCADE"); sql, _ := base.DB(); sql.Close() })
	scoped := dsn + " search_path=" + schema
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, _ := url.Parse(dsn)
		q := u.Query()
		q.Set("search_path", schema)
		u.RawQuery = q.Encode()
		scoped = u.String()
	}
	database, err := gorm.Open(postgres.Open(scoped), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sql, _ := database.DB()
	sql.SetMaxOpenConns(12)
	t.Cleanup(func() { sql.Close() })
	for range 2 {
		if err := db.RunMigrations(database); err != nil {
			t.Fatal("migration", err)
		}
	}
	for n := int64(1); n <= 4; n++ {
		if err := database.Create(&db.User{ID: n, Account: fmt.Sprintf("u%d", n), Name: fmt.Sprintf("user%d", n)}).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, n := range []int64{2, 3} {
		for _, f := range []db.Friend{{UserID: 1, FriendID: n}, {UserID: n, FriendID: 1}} {
			if err := database.Create(&f).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	return New(database, &fakeMedia{}), database
}
func TestPostgresVisibilityAllInteractionsAndRevocation(t *testing.T) {
	s, database := postgresFixture(t)
	ctx := context.Background()
	post, err := s.Create(ctx, 1, CreatePost{ClientPostID: "hello", Content: "原文\n**保留**", Visibility: "friends"})
	if err != nil {
		t.Fatal(err)
	}
	n, _ := ID(post.ID)
	private, err := s.Create(ctx, 1, CreatePost{ClientPostID: "private", Content: "secret", Visibility: "private"})
	if err != nil {
		t.Fatal(err)
	}
	privateID, _ := ID(private.ID)
	if _, err := s.Get(ctx, 2, privateID); !errors.Is(err, ErrNotFound) {
		t.Fatal("private leak", err)
	}
	if _, err := s.Get(ctx, 4, n); !errors.Is(err, ErrNotFound) {
		t.Fatal("nonfriend leak", err)
	}
	comment, err := s.AddComment(ctx, 3, n, CreateComment{ClientCommentID: "c", Content: "everyone can see"})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := s.SetLike(ctx, 3, n, true); err != nil {
			t.Fatal(err)
		}
	}
	read, err := s.Get(ctx, 2, n)
	if err != nil || read.CommentCount != 1 || read.LikeCount != 1 || read.CommentsPreview[0].Author.ID != "3" || read.LikesPreview[0].ID != "3" {
		t.Fatal(read, err)
	}
	if page, err := s.Comments(ctx, 2, n, "", 20); err != nil || len(page.Items) != 1 || page.Items[0].Content != comment.Content {
		t.Fatal(page, err)
	}
	if page, err := s.Likes(ctx, 2, n, "", 20); err != nil || len(page.Items) != 1 || page.Items[0].Author.ID != "3" {
		t.Fatal(page, err)
	}
	if page, err := s.Feed(ctx, 2, 0, "", 20); err != nil || len(page.Items) != 1 {
		t.Fatal(page, err)
	}
	if err := database.Model(&db.Friend{}).Where("user_id = ? AND friend_id = ?", 1, 3).Update("is_delete", true).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, 3, n); !errors.Is(err, ErrNotFound) {
		t.Fatal("one-way friendship allowed", err)
	}
	commentID, _ := ID(comment.ID)
	if err := s.DeleteComment(ctx, 3, commentID); err != nil {
		t.Fatal("former friend cannot remove own comment", err)
	}
	if page, err := s.Comments(ctx, 2, n, "", 20); err != nil || page.Items[0].Content != "" || !page.Items[0].Deleted {
		t.Fatal(page, err)
	}
}
func TestPostgresConcurrentIdempotencyAndCanonicalTimestamp(t *testing.T) {
	s, database := postgresFixture(t)
	ctx := context.Background()
	input := CreatePost{ClientPostID: "concurrent", Content: "hello", Visibility: "friends"}
	var wg sync.WaitGroup
	ids := make(chan *Post, 12)
	errs := make(chan error, 12)
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			post, err := s.Create(ctx, 1, input)
			if err != nil {
				errs <- err
			} else {
				ids <- post
			}
		}()
	}
	wg.Wait()
	close(errs)
	close(ids)
	for err := range errs {
		t.Fatal(err)
	}
	var first *Post
	for p := range ids {
		if first == nil {
			first = p
		} else if p.ID != first.ID || !p.CreatedAt.Equal(first.CreatedAt) {
			t.Fatal("noncanonical duplicate", p, first)
		}
	}
	var count int64
	database.Model(&db.MomentPost{}).Count(&count)
	if count != 1 {
		t.Fatal("duplicate post", count)
	}
	input.Content = "changed"
	if _, err := s.Create(ctx, 1, input); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	n, _ := ID(first.ID)
	commentInput := CreateComment{ClientCommentID: "same-comment", Content: "hi"}
	errs = make(chan error, 12)
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.AddComment(ctx, 2, n, commentInput)
			if err == nil {
				err = s.SetLike(ctx, 2, n, true)
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	database.Model(&db.MomentComment{}).Count(&count)
	if count != 1 {
		t.Fatal("duplicate comment", count)
	}
	database.Model(&db.MomentLike{}).Count(&count)
	if count != 1 {
		t.Fatal("duplicate like", count)
	}
	if err := s.Delete(ctx, 1, n); err != nil {
		t.Fatal(err)
	}
	input.Content = "hello"
	if _, err := s.Create(ctx, 1, input); !errors.Is(err, ErrGone) {
		t.Fatal("deleted key revived", err)
	}
	var row db.MomentComment
	database.First(&row)
	if row.Content != "" || row.DeletedAt == nil {
		t.Fatal(row)
	}
}
func TestPostgresAttachmentsAtomicBindingPrivacyAndPagination(t *testing.T) {
	s, database := postgresFixture(t)
	ctx := context.Background()
	ids := []string{}
	for n := 0; n < 9; n++ {
		row := db.MomentAsset{OwnerUserID: 1, ClientAssetID: fmt.Sprint(n), Hash: strings.Repeat("a", 128), Size: 1, MIME: "image/png", Status: "verified", CreatedAt: now()}
		if err := database.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id(row.ID))
	}
	post, err := s.Create(ctx, 1, CreatePost{ClientPostID: "photos", Visibility: "friends", AssetIDs: ids})
	if err != nil || len(post.Assets) != 9 {
		t.Fatal(post, err)
	}
	n, _ := ID(post.ID)
	assetID, _ := ID(ids[0])
	if _, err := s.AssetDownload(ctx, 2, assetID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AssetDownload(ctx, 4, assetID); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, 1, CreatePost{ClientPostID: "again", Visibility: "friends", AssetIDs: ids}); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	var count int64
	database.Model(&db.MomentPost{}).Count(&count)
	if count != 1 {
		t.Fatal("attachment rollback left post", count)
	}
	if err := s.Delete(ctx, 1, n); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AssetDownload(ctx, 1, assetID); !errors.Is(err, ErrNotFound) {
		t.Fatal("deleted post still signs", err)
	}
	stamp := now()
	for i := 0; i < 8; i++ {
		if err := database.Create(&db.MomentPost{AuthorUserID: 1, ClientPostID: fmt.Sprintf("page%d", i), RequestFingerprint: "f", Content: "x", Visibility: "friends", CreatedAt: stamp}).Error; err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	raw := ""
	for {
		page, err := s.Feed(ctx, 2, 0, raw, 3)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range page.Items {
			if seen[p.ID] {
				t.Fatal("repeated item")
			}
			seen[p.ID] = true
		}
		if !page.HasMore {
			break
		}
		raw = page.NextCursor
	}
	if len(seen) != 8 {
		t.Fatal("unstable equal-time pagination", seen)
	}
}
func TestPostgresDeletionRacesDoNotResurrectInteractions(t *testing.T) {
	s, database := postgresFixture(t)
	ctx := context.Background()
	p, err := s.Create(ctx, 1, CreatePost{ClientPostID: "delete-race", Content: "x", Visibility: "friends"})
	if err != nil {
		t.Fatal(err)
	}
	n, _ := ID(p.ID)
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		if err := s.Delete(ctx, 1, n); err != nil {
			t.Error(err)
		}
	}()
	go func() {
		defer wg.Done()
		err := s.SetLike(ctx, 2, n, true)
		if err != nil && !errors.Is(err, ErrNotFound) {
			t.Error(err)
		}
	}()
	go func() {
		defer wg.Done()
		_, err := s.AddComment(ctx, 2, n, CreateComment{ClientCommentID: "race", Content: "x"})
		if err != nil && !errors.Is(err, ErrNotFound) {
			t.Error(err)
		}
	}()
	wg.Wait()
	var count int64
	database.Model(&db.MomentComment{}).Where("post_id = ? AND deleted_at IS NULL", n).Count(&count)
	if count != 0 {
		t.Fatal("live comment on deleted post")
	}
	if _, err := s.Get(ctx, 1, n); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}
