package media

import (
	"context"
	"fmt"
	"io"
	"os"
	"testing"
	"time"
)

func TestRustFSDeleteAndStagingSweep(t *testing.T) {
	endpoint := os.Getenv("BETTERFLY_TEST_RUSTFS_ENDPOINT")
	if endpoint == "" {
		t.Skip("requires isolated BETTERFLY_TEST_RUSTFS_ENDPOINT")
	}
	t.Setenv("RUSTFS_ENDPOINT_URL", endpoint)
	t.Setenv("RUSTFS_EXTERNAL_ENDPOINT_URL", endpoint)
	t.Setenv("RUSTFS_ACCESS_KEY_ID", "moments-test-access")
	t.Setenv("RUSTFS_SECRET_ACCESS_KEY", "moments-test-secret")
	t.Setenv("MOMENTS_BUCKET", fmt.Sprintf("moments-media-%d", time.Now().UnixNano()))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, err := New(ctx, "http://127.0.0.1:8087")
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"staging/a", "staging/b", "sealed/a"} {
		if err := store.Seal(ctx, key, []byte(key), "image/png"); err != nil {
			t.Fatal(err)
		}
	}
	// 删除不存在的对象不报错，清理可以安全重试。
	if err := store.Delete(ctx, []string{"staging/a", "sealed/missing"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read(ctx, "staging/a"); err == nil {
		t.Fatal("deleted object still readable")
	}
	if n, err := store.DeleteStagingBefore(ctx, time.Now().Add(-time.Hour)); err != nil || n != 0 {
		t.Fatal("fresh staging swept", n, err)
	}
	if n, err := store.DeleteStagingBefore(ctx, time.Now().Add(time.Minute)); err != nil || n != 1 {
		t.Fatal("old staging not swept", n, err)
	}
	reader, err := store.Read(ctx, "sealed/a")
	if err != nil {
		t.Fatal("sealed object touched by staging sweep", err)
	}
	data, _ := io.ReadAll(reader)
	reader.Close()
	if string(data) != "sealed/a" {
		t.Fatal(string(data))
	}
}
