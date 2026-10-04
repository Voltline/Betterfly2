package httpserver

import (
	"Betterfly2/shared/db"
	"bytes"
	"context"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"momentsService/internal/media"
	"momentsService/internal/moments"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type fixtureAuth struct{}

func (fixtureAuth) Check(_ context.Context, user int64, jwt string) (int64, error) {
	if user >= 1 && user <= 4 && jwt == fmt.Sprintf("moments-test-%d", user) {
		return user, nil
	}
	return 0, errUnauthorized
}
func (fixtureAuth) Login(_ context.Context, account, password string) (int64, string, error) {
	user, err := strconv.ParseInt(strings.TrimPrefix(account, "u"), 10, 64)
	if err != nil || user < 1 || user > 4 || password != "test-only" {
		return 0, "", errUnauthorized
	}
	return user, fmt.Sprintf("moments-test-%d", user), nil
}

func liveHTTP(t *testing.T, addr string) (*httptest.Server, *gorm.DB) {
	t.Helper()
	dsn, endpoint := os.Getenv("BETTERFLY_TEST_POSTGRES_DSN"), os.Getenv("BETTERFLY_TEST_RUSTFS_ENDPOINT")
	if dsn == "" || endpoint == "" {
		t.Skip("requires isolated PostgreSQL and RustFS test endpoints")
	}
	base, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("moments_http_%d", time.Now().UnixNano())
	if err := base.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { base.Exec("DROP SCHEMA " + schema + " CASCADE"); sql, _ := base.DB(); sql.Close() })
	database, err := gorm.Open(postgres.Open(dsn+" search_path="+schema), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sql, _ := database.DB()
	sql.SetMaxOpenConns(6)
	t.Cleanup(func() { sql.Close() })
	if err := db.RunMigrations(database); err != nil {
		t.Fatal(err)
	}
	for n, name := range []string{"Alice", "Bob", "Carol", "Mallory"} {
		if err := database.Create(&db.User{ID: int64(n + 1), Account: fmt.Sprintf("u%d", n+1), Name: name}).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, pair := range [][2]int64{{1, 2}, {2, 1}, {1, 3}, {3, 1}} {
		if err := database.Create(&db.Friend{UserID: pair[0], FriendID: pair[1]}).Error; err != nil {
			t.Fatal(err)
		}
	}
	server := httptest.NewUnstartedServer(nil)
	if addr != "" {
		server.Listener.Close()
		server.Listener, err = net.Listen("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
	}
	origin := "http://" + server.Listener.Addr().String()
	t.Setenv("RUSTFS_ENDPOINT_URL", endpoint)
	t.Setenv("RUSTFS_EXTERNAL_ENDPOINT_URL", endpoint)
	t.Setenv("RUSTFS_ACCESS_KEY_ID", "moments-test-access")
	t.Setenv("RUSTFS_SECRET_ACCESS_KEY", "moments-test-secret")
	t.Setenv("MOMENTS_BUCKET", fmt.Sprintf("moments-test-%d", time.Now().UnixNano()))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	objects, err := media.New(ctx, origin)
	cancel()
	if err != nil {
		t.Fatal("private bucket initialization", err)
	}
	server.Config.Handler = New(moments.New(database, objects), fixtureAuth{}, origin).Routes()
	server.Start()
	t.Cleanup(server.Close)
	return server, database
}
func liveRequest(t *testing.T, server *httptest.Server, user int64, method, path string, body any, want int) []byte {
	t.Helper()
	var reader io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		reader = bytes.NewReader(data)
	}
	r, err := http.NewRequest(method, server.URL+base+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Authorization", fmt.Sprintf("Bearer moments-test-%d", user))
	r.Header.Set("X-User-ID", strconv.FormatInt(user, 10))
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	response, err := server.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != want {
		t.Fatalf("%s %s: %d want %d: %s", method, path, response.StatusCode, want, data)
	}
	return data
}
func TestLiveHTTPPrivateImagePublishAndInteractionLifecycle(t *testing.T) {
	server, database := liveHTTP(t, "")
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 4, 3))); err != nil {
		t.Fatal(err)
	}
	data := encoded.Bytes()
	digest := sha512.Sum512(data)
	input := moments.CreateAsset{ClientAssetID: "image-1", Hash: hex.EncodeToString(digest[:]), Size: int64(len(data)), MIME: "image/png"}
	var upload moments.Upload
	if err := json.Unmarshal(liveRequest(t, server, 1, "POST", "/assets", input, 200), &upload); err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest("PUT", upload.UploadURL, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range upload.Headers {
		request.Header.Set(key, value)
	}
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal("S3 upload", response.StatusCode)
	}
	liveRequest(t, server, 2, "POST", "/assets/"+upload.Asset.ID+"/verify", nil, 404)
	liveRequest(t, server, 1, "POST", "/assets/"+upload.Asset.ID+"/verify", nil, 200)
	liveRequest(t, server, 1, "POST", "/assets/"+upload.Asset.ID+"/verify", nil, 200)
	// The still-valid PUT capability cannot alter the sealed image.
	request, _ = http.NewRequest("PUT", upload.UploadURL, bytes.NewReader(bytes.Repeat([]byte("x"), len(data))))
	request.Header.Set("Content-Type", "image/png")
	response, err = server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	postInput := moments.CreatePost{ClientPostID: "image-post", Content: "今天\n**原文**", Visibility: "friends", AssetIDs: []string{upload.Asset.ID}}
	var post moments.Post
	if err := json.Unmarshal(liveRequest(t, server, 1, "POST", "/posts", postInput, 200), &post); err != nil {
		t.Fatal(err)
	}
	var duplicate moments.Post
	json.Unmarshal(liveRequest(t, server, 1, "POST", "/posts", postInput, 200), &duplicate)
	if post.ID != duplicate.ID || !post.CreatedAt.Equal(duplicate.CreatedAt) || duplicate.Content != postInput.Content {
		t.Fatal("noncanonical duplicate", duplicate)
	}
	postInput.Content = "changed"
	liveRequest(t, server, 1, "POST", "/posts", postInput, 409)
	liveRequest(t, server, 4, "GET", "/posts/"+post.ID, nil, 404)
	var download moments.Download
	json.Unmarshal(liveRequest(t, server, 2, "GET", "/assets/"+upload.Asset.ID+"/download", nil, 200), &download)
	response, err = server.Client().Get(download.URL)
	if err != nil {
		t.Fatal(err)
	}
	sealed, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 || !bytes.Equal(data, sealed) {
		t.Fatal("mutable staging replaced sealed bytes")
	}
	unsigned := strings.Split(download.URL, "?")[0]
	response, err = server.Client().Get(unsigned)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode == 200 {
		t.Fatal("anonymous private object readable")
	}
	liveRequest(t, server, 4, "GET", "/assets/"+upload.Asset.ID+"/download", nil, 404)
	for range 2 {
		liveRequest(t, server, 3, "PUT", "/posts/"+post.ID+"/like", nil, 200)
	}
	commentInput := moments.CreateComment{ClientCommentID: "comment", Content: "所有可见者都能看到"}
	var comment moments.Comment
	json.Unmarshal(liveRequest(t, server, 3, "POST", "/posts/"+post.ID+"/comments", commentInput, 200), &comment)
	liveRequest(t, server, 3, "POST", "/posts/"+post.ID+"/comments", commentInput, 200)
	comments := liveRequest(t, server, 2, "GET", "/posts/"+post.ID+"/comments", nil, 200)
	if !bytes.Contains(comments, []byte(commentInput.Content)) {
		t.Fatal("common-friend filtered")
	}
	liveRequest(t, server, 2, "GET", "/posts/"+post.ID+"/likes", nil, 200)
	if err := database.Model(&db.Friend{}).Where("user_id = 1 AND friend_id = 2").Update("is_delete", true).Error; err != nil {
		t.Fatal(err)
	}
	liveRequest(t, server, 2, "GET", "/assets/"+upload.Asset.ID+"/download", nil, 404)
	liveRequest(t, server, 1, "DELETE", "/posts/"+post.ID, nil, 200)
	liveRequest(t, server, 1, "DELETE", "/posts/"+post.ID, nil, 200)
	liveRequest(t, server, 1, "GET", "/posts/"+post.ID, nil, 404)
	liveRequest(t, server, 1, "GET", "/assets/"+upload.Asset.ID+"/download", nil, 404)
	postInput.Content = "今天\n**原文**"
	liveRequest(t, server, 1, "POST", "/posts", postInput, 410)
	var row db.MomentComment
	if err := database.First(&row).Error; err != nil || row.Content != "" || row.DeletedAt == nil {
		t.Fatal(row, err)
	}
}

// Opt-in preview uses the same real PostgreSQL/S3 API with test-only Auth.
// This entry point is test code, never part of the production executable.
func TestMomentsWebPreview(t *testing.T) {
	addr := os.Getenv("BETTERFLY_MOMENTS_WEB_TEST_ADDR")
	if addr == "" {
		t.Skip("opt-in browser preview")
	}
	server, _ := liveHTTP(t, addr)
	t.Logf("test page: %s/moments/; account u1, password test-only", server.URL)
	time.Sleep(30 * time.Minute)
}
