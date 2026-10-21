package http_server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"Betterfly2/shared/db"
	"storageService/internal/rustfs"
)

// rustfsIntegrationClient 连接一个隔离的真实 RustFS，例如：
//
//	docker run -d --name betterfly-rustfs-e2e -p 127.0.0.1:19000:9000 \
//	  --tmpfs /data:rw,mode=1777 rustfs/rustfs:latest \
//	  --address :9000 --access-key rustfsadmin --secret-key rustfsadmin /data
//	BETTERFLY_TEST_RUSTFS_ENDPOINT=http://127.0.0.1:19000 go test ./internal/http_server -run RustFS
func rustfsIntegrationClient(t *testing.T) *rustfs.RustFSClient {
	t.Helper()
	endpoint := os.Getenv("BETTERFLY_TEST_RUSTFS_ENDPOINT")
	if endpoint == "" {
		t.Skip("requires an isolated BETTERFLY_TEST_RUSTFS_ENDPOINT")
	}
	accessKey := os.Getenv("BETTERFLY_TEST_RUSTFS_ACCESS_KEY")
	secretKey := os.Getenv("BETTERFLY_TEST_RUSTFS_SECRET_KEY")
	if accessKey == "" || secretKey == "" {
		accessKey, secretKey = "rustfsadmin", "rustfsadmin"
	}
	t.Setenv("RUSTFS_REGION", "us-east-1")
	t.Setenv("RUSTFS_ACCESS_KEY_ID", accessKey)
	t.Setenv("RUSTFS_SECRET_ACCESS_KEY", secretKey)
	t.Setenv("RUSTFS_ENDPOINT_URL", endpoint)
	t.Setenv("RUSTFS_EXTERNAL_ENDPOINT_URL", endpoint)
	t.Setenv("RUSTFS_BUCKET", fmt.Sprintf("upload-e2e-%d", time.Now().UnixNano()))
	client, err := rustfs.NewRustFSClient()
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func newRustFSUploadHandler(client *rustfs.RustFSClient, metadata *fakeFileMetadata) *UploadHandler {
	handler := NewUploadHandler(client)
	metadata.install(handler)
	return handler
}

func requestUploadURL(t *testing.T, handler *UploadHandler, userID int64, fileHash string, size int) string {
	t.Helper()
	body := fmt.Sprintf(`{"file_hash":"%s","file_size":%d}`, fileHash, size)
	rec := httptest.NewRecorder()
	handler.HandleUploadRequest(rec, asUser(httptest.NewRequest(http.MethodPost, "/storage_service/upload", strings.NewReader(body)), userID))
	var resp struct {
		Exists    bool   `json:"exists"`
		UploadURL string `json:"upload_url"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil || rec.Code != http.StatusOK || resp.UploadURL == "" {
		t.Fatalf("upload URL request failed: status=%d resp=%+v err=%v", rec.Code, resp, err)
	}
	return resp.UploadURL
}

func putObject(t *testing.T, url, content string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, url, strings.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("presigned PUT failed: status=%d body=%s", resp.StatusCode, body)
	}
}

func readObject(t *testing.T, client *rustfs.RustFSClient, key string) (string, bool) {
	t.Helper()
	exists, err := client.ObjectExists(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		return "", false
	}
	reader, _, err := client.OpenObject(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	content, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	return string(content), true
}

func TestRustFSStagedUploadFlow(t *testing.T) {
	client := rustfsIntegrationClient(t)
	metadata := &fakeFileMetadata{}
	handler := newRustFSUploadHandler(client, metadata)
	staging := rustfs.GetStagingPath(uploaderID, payloadHash)
	canonical := rustfs.GetStoragePath(payloadHash)

	uploadURL := requestUploadURL(t, handler, uploaderID, payloadHash, len(payload))
	if !strings.Contains(uploadURL, "/staging/1001/"+payloadHash) {
		t.Fatalf("upload URL does not target the staging path: %s", uploadURL)
	}
	putObject(t, uploadURL, payload)
	if _, ok := readObject(t, client, canonical); ok {
		t.Fatal("client upload reached the canonical path before verification")
	}

	if status, resp := verifyAs(t, handler, otherID, payloadHash); status != http.StatusOK || resp.Success {
		t.Fatalf("another user verified the uploader's staged object: status=%d %+v", status, resp)
	}
	if content, ok := readObject(t, client, staging); !ok || content != payload {
		t.Fatal("another user's verify touched the uploader's staging object")
	}

	if status, resp := verifyAs(t, handler, uploaderID, payloadHash); status != http.StatusOK || !resp.Success {
		t.Fatalf("uploader verify failed: status=%d %+v", status, resp)
	}
	if content, ok := readObject(t, client, canonical); !ok || content != payload {
		t.Fatalf("canonical object missing or wrong after verify: ok=%v content=%q", ok, content)
	}
	if _, ok := readObject(t, client, staging); ok {
		t.Fatal("staging object was not removed after promotion")
	}
	if metadata.verified[payloadHash] != int64(len(payload)) {
		t.Fatalf("unexpected metadata: %v", metadata.verified)
	}

	if !metadata.grants[grantKey(payloadHash, uploaderID)] {
		t.Fatal("uploader was not granted after verify")
	}

	// 另一位用户只知道哈希：拿到的是自己的暂存URL，伪造内容既不能改写正式对象也拿不到授权。
	forgedURL := requestUploadURL(t, handler, otherID, payloadHash, len(payload))
	putObject(t, forgedURL, "forged")
	if status, resp := verifyAs(t, handler, otherID, payloadHash); status != http.StatusOK || resp.Success || resp.ErrorMessage != "File hash mismatch" {
		t.Fatalf("forged possession proof was accepted: status=%d %+v", status, resp)
	}
	if content, _ := readObject(t, client, canonical); content != payload {
		t.Fatalf("verified canonical object was rewritten: %q", content)
	}
	otherStaging := rustfs.GetStagingPath(otherID, payloadHash)
	if _, ok := readObject(t, client, otherStaging); ok {
		t.Fatal("forged staging object was not cleaned up")
	}
	if metadata.grants[grantKey(payloadHash, otherID)] {
		t.Fatal("forged possession proof granted access")
	}

	// 真正持有文件的用户重新上传一次即可获得授权，正式对象保持不变。
	putObject(t, requestUploadURL(t, handler, otherID, payloadHash, len(payload)), payload)
	if status, resp := verifyAs(t, handler, otherID, payloadHash); status != http.StatusOK || !resp.Success {
		t.Fatalf("matching possession proof was rejected: status=%d %+v", status, resp)
	}
	if !metadata.grants[grantKey(payloadHash, otherID)] {
		t.Fatal("matching possession proof did not grant access")
	}
	if _, ok := readObject(t, client, otherStaging); ok {
		t.Fatal("possession proof staging object was not cleaned up")
	}

	// 下载走真实预签名URL：持有者拿到原始内容，无权用户与文件不存在的响应一致。
	download := NewDownloadHandler(client)
	download.getFileMetadata = func(fileHash string) (*db.FileMetadata, error) {
		return &db.FileMetadata{FileHash: fileHash, IsVerified: true, FileSize: metadata.verified[fileHash]}, nil
	}
	download.canDownloadFile = func(fileHash string, userID int64) (bool, error) {
		return metadata.grants[grantKey(fileHash, userID)], nil
	}
	for userID, allowed := range map[int64]bool{uploaderID: true, otherID: true, 3003: false} {
		rec := httptest.NewRecorder()
		download.HandleDownloadRequest(rec, asUser(httptest.NewRequest(http.MethodGet, "/storage_service/download?file_hash="+payloadHash, nil), userID))
		var resp struct {
			Exists       bool   `json:"exists"`
			DownloadURL  string `json:"download_url"`
			ErrorMessage string `json:"error_message"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil || rec.Code != http.StatusOK {
			t.Fatalf("user %d download response: status=%d err=%v", userID, rec.Code, err)
		}
		if !allowed {
			if resp.Exists || resp.DownloadURL != "" || resp.ErrorMessage != "File not found" {
				t.Fatalf("unauthorized user %d got a download URL: %+v", userID, resp)
			}
			continue
		}
		got, err := http.Get(resp.DownloadURL)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(got.Body)
		got.Body.Close()
		if got.StatusCode != http.StatusOK || string(body) != payload {
			t.Fatalf("user %d presigned download failed: status=%d body=%q", userID, got.StatusCode, body)
		}
	}
}

func TestRustFSHashMismatchKeepsCanonicalObject(t *testing.T) {
	client := rustfsIntegrationClient(t)
	metadata := &fakeFileMetadata{}
	handler := newRustFSUploadHandler(client, metadata)
	canonical := rustfs.GetStoragePath(payloadHash)

	putObject(t, requestUploadURL(t, handler, uploaderID, payloadHash, len(payload)), payload)
	if status, resp := verifyAs(t, handler, uploaderID, payloadHash); status != http.StatusOK || !resp.Success {
		t.Fatalf("setup verify failed: status=%d %+v", status, resp)
	}
	// 模拟元数据丢失后，攻击者上传哈希不匹配的内容。
	delete(metadata.verified, payloadHash)
	putObject(t, requestUploadURL(t, handler, otherID, payloadHash, len(payload)), "forged")
	status, resp := verifyAs(t, handler, otherID, payloadHash)
	if status != http.StatusOK || resp.Success || resp.ErrorMessage != "File hash mismatch" {
		t.Fatalf("forged upload was not rejected: status=%d %+v", status, resp)
	}
	if content, ok := readObject(t, client, canonical); !ok || content != payload {
		t.Fatalf("hash mismatch deleted or rewrote the canonical object: ok=%v content=%q", ok, content)
	}
	if _, ok := readObject(t, client, rustfs.GetStagingPath(otherID, payloadHash)); ok {
		t.Fatal("forged staging object was not deleted")
	}
}

func TestRustFSConditionalCopyRejectsChangedSource(t *testing.T) {
	client := rustfsIntegrationClient(t)
	ctx := context.Background()
	handler := NewUploadHandler(client)
	source := rustfs.GetStagingPath(uploaderID, payloadHash)
	destination := rustfs.GetStoragePath(payloadHash)

	url, err := handler.getPresignedUploadURL(ctx, source, uploadURLTTL, "")
	if err != nil {
		t.Fatal(err)
	}
	putObject(t, url, payload)
	reader, verifiedETag, err := client.OpenObject(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	reader.Close()
	if verifiedETag == "" {
		t.Fatal("RustFS returned no ETag for the staged object")
	}

	// 校验结束后、复制之前，客户端用仍然有效的URL改写暂存对象。
	putObject(t, url, "forged")
	copiedETag, err := client.CopyObject(ctx, source, destination, verifiedETag)
	if err == nil {
		content, _ := readObject(t, client, destination)
		t.Fatalf("copy ignored the source precondition: copied_etag=%s verified_etag=%s content=%q", copiedETag, verifiedETag, content)
	}
	if _, ok := readObject(t, client, destination); ok {
		t.Fatal("failed conditional copy still wrote the destination")
	}

	putObject(t, url, payload)
	reader, currentETag, err := client.OpenObject(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	reader.Close()
	copiedETag, err = client.CopyObject(ctx, source, destination, currentETag)
	if err != nil {
		t.Fatalf("copy with matching precondition failed: %v", err)
	}
	if normalizeETag(copiedETag) != normalizeETag(currentETag) {
		t.Fatalf("copy result ETag differs from source: source=%s copied=%s", currentETag, copiedETag)
	}
}
