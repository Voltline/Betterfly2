package moments

import (
	"Betterfly2/shared/db"
	"bytes"
	"context"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type fakeMedia struct {
	mu           sync.Mutex
	data         []byte
	sealed       []byte
	reads, signs int
	err          error
	deleted      []string
	deleteErr    error
	stagingSwept int
}

func (m *fakeMedia) Upload(context.Context, string, string, int64) (string, map[string]string, error) {
	return "https://storage/upload", map[string]string{"Content-Type": "image/png"}, m.err
}
func (m *fakeMedia) Read(context.Context, string) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reads++
	return io.NopCloser(bytes.NewReader(m.data)), m.err
}
func (m *fakeMedia) Seal(_ context.Context, _ string, data []byte, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sealed = append([]byte(nil), data...)
	return m.err
}
func (m *fakeMedia) Download(context.Context, string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.signs++
	return "https://storage/download", m.err
}
func (m *fakeMedia) Avatar(ctx context.Context, key string) (string, error) {
	return m.Download(ctx, key)
}
func (m *fakeMedia) Health(context.Context) error { return m.err }
func (m *fakeMedia) Delete(_ context.Context, keys []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.deleteErr != nil {
		return m.deleteErr
	}
	m.deleted = append(m.deleted, keys...)
	return nil
}
func (m *fakeMedia) DeleteStagingBefore(context.Context, time.Time) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stagingSwept, m.deleteErr
}
func testService(t *testing.T) (*Service, sqlmock.Sqlmock, *fakeMedia) {
	t.Helper()
	sql, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sql.Close() })
	database, err := gorm.Open(postgres.New(postgres.Config{Conn: sql}), &gorm.Config{SkipDefaultTransaction: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	media := &fakeMedia{}
	return New(database, media), mock, media
}
func postRows(n int64, input CreatePost, deleted any) *sqlmock.Rows {
	return sqlmock.NewRows([]string{"id", "author_user_id", "client_post_id", "request_fingerprint", "content", "visibility", "created_at", "deleted_at"}).AddRow(n, 1, input.ClientPostID, fingerprint(input), input.Content, input.Visibility, now(), deleted)
}
func emptyHydration(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(`SELECT .*moment_assets`).WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectQuery(`SELECT .*COUNT\(\*\).*moment_likes`).WillReturnRows(sqlmock.NewRows([]string{"post_id", "count", "liked"}))
	mock.ExpectQuery(`SELECT .*COUNT\(\*\).*moment_comments`).WillReturnRows(sqlmock.NewRows([]string{"post_id", "count"}))
	mock.ExpectQuery(`SELECT post_id, user_id, created_at FROM`).WillReturnRows(sqlmock.NewRows([]string{"post_id", "user_id", "created_at"}))
	mock.ExpectQuery(`SELECT id, post_id, author_user_id, content`).WillReturnRows(sqlmock.NewRows([]string{"id", "post_id"}))
	mock.ExpectQuery(`SELECT .*users`).WillReturnRows(sqlmock.NewRows([]string{"id", "name", "avatar"}).AddRow(1, "作者", ""))
}
func TestValidationAndScopedStableCursors(t *testing.T) {
	good := CreatePost{ClientPostID: "post-1", Content: strings.Repeat("界", 4096), Visibility: "friends"}
	if _, err := validatePost(good); err != nil {
		t.Fatal(err)
	}
	for _, input := range []CreatePost{{ClientPostID: "a", Visibility: "friends"}, {ClientPostID: "a", Content: "\xff", Visibility: "friends"}, {ClientPostID: "a", Content: "a\x00b", Visibility: "friends"}, {ClientPostID: "a", Content: strings.Repeat("界", 4097), Visibility: "friends"}, {ClientPostID: "a", Content: "ok", Visibility: "public"}, {ClientPostID: "a", Content: "ok", Visibility: "friends", AssetIDs: []string{"1", "1"}}} {
		if _, err := validatePost(input); err == nil {
			t.Fatal(input)
		}
	}
	for count := 9; count <= 10; count++ {
		input := good
		for n := 1; n <= count; n++ {
			input.AssetIDs = append(input.AssetIDs, id(int64(n)))
		}
		_, err := validatePost(input)
		if (err == nil) != (count == 9) {
			t.Fatal(count, err)
		}
	}
	stamp := now()
	raw := nextCursor("posts:1:0", stamp, 42)
	c, l, err := pageArgs(raw, "posts:1:0", 20)
	if err != nil || c.ID != 42 || !c.Time.Equal(stamp) || l != 20 {
		t.Fatal(c, l, err)
	}
	for _, scope := range []string{"posts:2:0", "posts:1:1", "comments:1:42"} {
		if _, _, err := pageArgs(raw, scope, 20); err == nil {
			t.Fatal("unscoped cursor")
		}
	}
	for _, value := range []string{"bad", strings.Repeat("a", 1025)} {
		if _, _, err := pageArgs(value, "posts:1:0", 20); err == nil {
			t.Fatal(value)
		}
	}
	for _, value := range []string{"0", "-1", "01", "9223372036854775808"} {
		if _, err := ID(value); err == nil {
			t.Fatal(value)
		}
	}
}
func TestCreateIdempotentCanonicalAndConflictingRetry(t *testing.T) {
	for _, scenario := range []string{"new", "duplicate", "conflict", "deleted"} {
		t.Run(scenario, func(t *testing.T) {
			s, mock, _ := testService(t)
			input := CreatePost{ClientPostID: "same", Content: "换行\nMarkdown **原文**", Visibility: "friends", AssetIDs: []string{}}
			mock.ExpectBegin()
			insert := mock.ExpectQuery(`INSERT INTO "moment_posts".*ON CONFLICT .*DO NOTHING RETURNING "id"`)
			if scenario == "new" {
				insert.WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(7))
			} else {
				insert.WillReturnRows(sqlmock.NewRows([]string{"id"}))
				stored := input
				var deleted any
				if scenario == "conflict" {
					stored.Content = "original"
				}
				if scenario == "deleted" {
					deleted = now()
				}
				mock.ExpectQuery(`SELECT .*moment_posts`).WillReturnRows(postRows(7, stored, deleted))
			}
			if scenario == "deleted" || scenario == "conflict" {
				mock.ExpectRollback()
			} else {
				mock.ExpectCommit()
				mock.ExpectQuery(`SELECT .*moment_posts`).WillReturnRows(postRows(7, input, nil))
				emptyHydration(mock)
			}
			result, err := s.Create(context.Background(), 1, input)
			if scenario == "deleted" {
				if !errors.Is(err, ErrGone) {
					t.Fatal(err)
				}
			} else if scenario == "conflict" {
				if !errors.Is(err, ErrConflict) {
					t.Fatal(err)
				}
			} else if err != nil || result.ID != "7" || result.Content != input.Content {
				t.Fatal(result, err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestCreateRollsBackUnverifiedForeignOrAlreadyBoundAttachments(t *testing.T) {
	for _, status := range []string{"missing", "pending", "bound", "database"} {
		t.Run(status, func(t *testing.T) {
			s, mock, _ := testService(t)
			mock.ExpectBegin()
			mock.ExpectQuery(`INSERT INTO "moment_posts"`).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(9))
			query := mock.ExpectQuery(`SELECT .*moment_assets.*ORDER BY id FOR UPDATE`)
			rows := sqlmock.NewRows([]string{"id", "owner_user_id", "status", "post_id"})
			switch status {
			case "pending":
				rows.AddRow(2, 1, "pending", nil)
			case "bound":
				rows.AddRow(2, 1, "verified", 3)
			case "database":
				query.WillReturnError(errors.New("connection lost"))
			}
			if status != "database" {
				query.WillReturnRows(rows)
			}
			mock.ExpectRollback()
			if _, err := s.Create(context.Background(), 1, CreatePost{ClientPostID: "a", Visibility: "friends", AssetIDs: []string{"2"}}); err == nil {
				t.Fatal("failed attachment committed")
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestObjectReadsRecheckMutualFriendshipAndHideExistence(t *testing.T) {
	for _, route := range []string{"post", "comments", "likes", "asset"} {
		t.Run(route, func(t *testing.T) {
			s, mock, media := testService(t)
			if route == "asset" {
				mock.ExpectQuery(`SELECT .*moment_assets`).WillReturnRows(sqlmock.NewRows([]string{"id", "owner_user_id", "post_id", "status"}).AddRow(1, 1, 7, "verified"))
			}
			mock.ExpectQuery(`SELECT .*moment_posts.*deleted_at IS NULL.*visibility = 'friends'.*JOIN friends r.*f.is_delete = false AND r.is_delete = false`).WillReturnRows(sqlmock.NewRows([]string{"id"}))
			var err error
			switch route {
			case "post":
				_, err = s.Get(context.Background(), 3, 7)
			case "comments":
				_, err = s.Comments(context.Background(), 3, 7, "", 20)
			case "likes":
				_, err = s.Likes(context.Background(), 3, 7, "", 20)
			case "asset":
				_, err = s.AssetDownload(context.Background(), 3, 1)
			}
			if !errors.Is(err, ErrNotFound) || media.signs != 0 {
				t.Fatal(err, media.signs)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestDatabaseReadFailureIsNotNotFound(t *testing.T) {
	s, mock, _ := testService(t)
	failure := errors.New("database down")
	mock.ExpectQuery(`SELECT .*moment_posts`).WillReturnError(failure)
	_, err := s.Get(context.Background(), 1, 1)
	if !errors.Is(err, failure) || errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}
func TestDeletionClearsContentAndCommentsOnlyAfterOwnerLock(t *testing.T) {
	s, mock, _ := testService(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT .*moment_posts.*author_user_id.*FOR UPDATE`).WillReturnRows(sqlmock.NewRows([]string{"id", "author_user_id"}).AddRow(7, 1))
	mock.ExpectExec(`UPDATE "moment_posts" SET "content"=\$1,"deleted_at"=\$2`).WithArgs("", sqlmock.AnyArg(), int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE "moment_comments" SET "content"=\$1,"deleted_at"=\$2`).WithArgs("", sqlmock.AnyArg(), int64(7)).WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectCommit()
	if err := s.Delete(context.Background(), 1, 7); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
func imageFixture(t *testing.T) ([]byte, db.MomentAsset) {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 2, 3))); err != nil {
		t.Fatal(err)
	}
	data := b.Bytes()
	hash := sha512.Sum512(data)
	return data, db.MomentAsset{ID: 2, OwnerUserID: 1, Hash: hex.EncodeToString(hash[:]), Size: int64(len(data)), MIME: "image/png", Status: "pending", StagingKey: "staging/key", StorageKey: "sealed/key"}
}
func assetRows(row db.MomentAsset) *sqlmock.Rows {
	return sqlmock.NewRows([]string{"id", "owner_user_id", "hash", "size", "mime", "status", "staging_key", "storage_key", "width", "height"}).AddRow(row.ID, row.OwnerUserID, row.Hash, row.Size, row.MIME, row.Status, row.StagingKey, row.StorageKey, row.Width, row.Height)
}
func TestVerifyImageIntegrityBoundaries(t *testing.T) {
	data, row := imageFixture(t)
	if _, w, h, err := verifyImage(bytes.NewReader(data), row); err != nil || w != 2 || h != 3 {
		t.Fatal(w, h, err)
	}
	for _, what := range []string{"hash", "size", "mime", "fake", "truncated", "huge"} {
		t.Run(what, func(t *testing.T) {
			r := row
			payload := data
			switch what {
			case "hash":
				r.Hash = strings.Repeat("0", 128)
			case "size":
				r.Size++
			case "mime":
				r.MIME = "image/jpeg"
			case "fake":
				payload = []byte("not image")
				sum := sha512.Sum512(payload)
				r.Hash = hex.EncodeToString(sum[:])
				r.Size = int64(len(payload))
			case "truncated":
				payload = data[:len(data)-10]
				sum := sha512.Sum512(payload)
				r.Hash = hex.EncodeToString(sum[:])
				r.Size = int64(len(payload))
			case "huge":
				payload = make([]byte, MaxImageBytes+1)
			}
			if _, _, _, err := verifyImage(bytes.NewReader(payload), r); !errors.Is(err, ErrInvalid) {
				t.Fatal(what, err)
			}
		})
	}
}
func TestVerifyMetadataFailureSafeRetryAndVerifiedNoOp(t *testing.T) {
	s, mock, media := testService(t)
	data, row := imageFixture(t)
	media.data = data
	for attempt := 0; attempt < 3; attempt++ {
		mock.ExpectBegin()
		if attempt == 2 {
			row.Status = "verified"
			row.Width = 2
			row.Height = 3
		}
		mock.ExpectQuery(`SELECT .*moment_assets.*FOR UPDATE`).WillReturnRows(assetRows(row))
		if attempt < 2 {
			update := mock.ExpectExec(`UPDATE "moment_assets"`)
			if attempt == 0 {
				update.WillReturnError(errors.New("write failed"))
				mock.ExpectRollback()
			} else {
				update.WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectCommit()
			}
		} else {
			mock.ExpectCommit()
		}
		result, err := s.VerifyAsset(context.Background(), 1, 2)
		if attempt == 0 {
			if err == nil {
				t.Fatal("metadata failure reported success")
			}
		} else if err != nil || result.Status != "verified" {
			t.Fatal(result, err)
		}
	}
	if media.reads != 2 || !bytes.Equal(media.sealed, data) {
		t.Fatal("sealed wrong bytes or verified read repeated", media.reads)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
func TestCommentViewTombstoneNeverExposesDeletedContent(t *testing.T) {
	stamp := now()
	result := commentView(db.MomentComment{ID: 1, AuthorUserID: 2, Content: "secret", DeletedAt: &stamp}, nil, 2, 1)
	if result.Content != "" || !result.Deleted || result.CanDelete {
		t.Fatal(result)
	}
}
func TestPrepareAssetCanonicalConflict(t *testing.T) {
	s, mock, _ := testService(t)
	input := CreateAsset{ClientAssetID: "asset", Hash: strings.Repeat("a", 128), Size: 12, MIME: "image/png"}
	mock.ExpectQuery(`SELECT count\(\*\) FROM "moment_assets"`).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(`INSERT INTO "moment_assets"`).WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectQuery(`SELECT .*moment_assets`).WillReturnRows(sqlmock.NewRows([]string{"id", "hash", "size", "mime"}).AddRow(3, input.Hash, 13, input.MIME))
	if _, err := s.PrepareAsset(context.Background(), 1, input); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
}
func TestFeedBulkQueryCountIndependentOfPageSize(t *testing.T) {
	for _, size := range []int{1, 20, 50} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			s, mock, _ := testService(t)
			rows := sqlmock.NewRows([]string{"id", "author_user_id", "content", "visibility", "created_at"})
			for i := 0; i < size; i++ {
				rows.AddRow(i+1, 1, "text", "friends", time.Now())
			}
			mock.ExpectQuery(`SELECT .*moment_posts`).WillReturnRows(rows)
			emptyHydration(mock)
			page, err := s.Feed(context.Background(), 1, 0, "", size)
			if err != nil || len(page.Items) != size {
				t.Fatal(len(page.Items), err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal("page must use seven queries", err)
			}
		})
	}
}
