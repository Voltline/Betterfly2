package moments

import (
	"Betterfly2/shared/utils"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
)

var (
	ErrInvalid  = errors.New("invalid request")
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("idempotency conflict")
	ErrGone     = errors.New("deleted")
	ErrQuota    = errors.New("too many unattached assets")
)

const MaxImageBytes int64 = 10 << 20

type Author struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Avatar string `json:"avatar"`
}
type Asset struct {
	ID     string `json:"id"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}
type Post struct {
	ID              string    `json:"id"`
	Author          Author    `json:"author"`
	Content         string    `json:"content"`
	Visibility      string    `json:"visibility"`
	CreatedAt       time.Time `json:"created_at"`
	Assets          []Asset   `json:"assets"`
	LikeCount       int64     `json:"like_count"`
	CommentCount    int64     `json:"comment_count"`
	LikedByMe       bool      `json:"liked_by_me"`
	CanDelete       bool      `json:"can_delete"`
	LikesPreview    []Author  `json:"likes_preview"`
	CommentsPreview []Comment `json:"comments_preview"`
}
type Comment struct {
	ID               string    `json:"id"`
	Author           Author    `json:"author"`
	Content          string    `json:"content"`
	CreatedAt        time.Time `json:"created_at"`
	ReplyToCommentID string    `json:"reply_to_comment_id,omitempty"`
	Deleted          bool      `json:"deleted"`
	CanDelete        bool      `json:"can_delete"`
}
type Like struct {
	Author    Author    `json:"author"`
	CreatedAt time.Time `json:"created_at"`
}
type Page[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor"`
	HasMore    bool   `json:"has_more"`
}
type CreatePost struct {
	ClientPostID string   `json:"client_post_id"`
	Content      string   `json:"content"`
	Visibility   string   `json:"visibility"`
	AssetIDs     []string `json:"asset_ids"`
}
type CreateComment struct {
	ClientCommentID  string `json:"client_comment_id"`
	Content          string `json:"content"`
	ReplyToCommentID string `json:"reply_to_comment_id,omitempty"`
}
type CreateAsset struct {
	ClientAssetID string `json:"client_asset_id"`
	Hash          string `json:"hash"`
	Size          int64  `json:"size"`
	MIME          string `json:"mime"`
}
type Upload struct {
	Asset     Asset             `json:"asset"`
	Status    string            `json:"status"`
	UploadURL string            `json:"upload_url,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	ExpiresAt *time.Time        `json:"expires_at,omitempty"`
}
type Download struct {
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
}

func ID(value string) (int64, error) {
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n <= 0 || strconv.FormatInt(n, 10) != value {
		return 0, ErrInvalid
	}
	return n, nil
}
func id(value int64) string { return strconv.FormatInt(value, 10) }
func validKey(value string) bool {
	if len(value) < 1 || len(value) > 128 {
		return false
	}
	for _, c := range value {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("._-", c)) {
			return false
		}
	}
	return true
}
func validText(value string, points, bytes int) bool {
	return len(value) <= bytes && utils.ValidText(value, points)
}
func validatePost(input CreatePost) ([]int64, error) {
	if !validKey(input.ClientPostID) || !validText(input.Content, 4096, 16<<10) || input.Visibility != "friends" && input.Visibility != "private" || len(input.AssetIDs) > 9 || strings.TrimSpace(input.Content) == "" && len(input.AssetIDs) == 0 {
		return nil, ErrInvalid
	}
	ids := make([]int64, 0, len(input.AssetIDs))
	seen := map[int64]bool{}
	for _, value := range input.AssetIDs {
		n, err := ID(value)
		if err != nil || seen[n] {
			return nil, ErrInvalid
		}
		seen[n] = true
		ids = append(ids, n)
	}
	return ids, nil
}
func validateAsset(input CreateAsset) error {
	hash, err := hex.DecodeString(input.Hash)
	if !validKey(input.ClientAssetID) || err != nil || len(hash) != 64 || strings.ToLower(input.Hash) != input.Hash || input.Size <= 0 || input.Size > MaxImageBytes || input.MIME != "image/jpeg" && input.MIME != "image/png" {
		return ErrInvalid
	}
	return nil
}
func fingerprint(input any) string {
	data, _ := json.Marshal(input)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

type cursor struct {
	Context string    `json:"q"`
	Time    time.Time `json:"t"`
	ID      int64     `json:"i"`
}

func pageArgs(raw, scope string, limit int) (cursor, int, error) {
	if limit == 0 {
		limit = 20
	}
	if limit < 1 || limit > 50 {
		return cursor{}, 0, ErrInvalid
	}
	if raw == "" {
		return cursor{}, limit, nil
	}
	if len(raw) > 1024 {
		return cursor{}, 0, ErrInvalid
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	var c cursor
	if err != nil || json.Unmarshal(data, &c) != nil || c.Context != scope || c.ID <= 0 || c.Time.IsZero() || c.Time.Year() < 1 || c.Time.Year() > 9999 {
		return cursor{}, 0, ErrInvalid
	}
	return c, limit, nil
}
func nextCursor(scope string, timestamp time.Time, n int64) string {
	data, _ := json.Marshal(cursor{scope, timestamp, n})
	return base64.RawURLEncoding.EncodeToString(data)
}
func now() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }
