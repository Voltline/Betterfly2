package http_server

import (
	"context"
	"crypto/md5"
	"crypto/sha512"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"storageService/internal/rustfs"
)

const (
	uploaderID int64 = 1001
	otherID    int64 = 2002
	payload          = "payload"
)

var payloadHash = fmt.Sprintf("%x", sha512.Sum512([]byte(payload)))

func asUser(r *http.Request, userID int64) *http.Request {
	return r.WithContext(withAuthenticatedUserID(r.Context(), userID))
}

type fakeObjectStore struct {
	objects map[string]string
	deleted []string
	copies  []string
	// copyETag 非空时模拟复制结果与校验时的源对象不一致。
	copyETag string
}

func newFakeObjectStore() *fakeObjectStore {
	return &fakeObjectStore{objects: map[string]string{}}
}

func objectETag(content string) string {
	return fmt.Sprintf("\"%x\"", md5.Sum([]byte(content)))
}

func (s *fakeObjectStore) exists(_ context.Context, key string) (bool, error) {
	_, ok := s.objects[key]
	return ok, nil
}

func (s *fakeObjectStore) open(_ context.Context, key string) (io.ReadCloser, string, error) {
	content, ok := s.objects[key]
	if !ok {
		return nil, "", errors.New("NoSuchKey")
	}
	return io.NopCloser(strings.NewReader(content)), objectETag(content), nil
}

func (s *fakeObjectStore) copy(_ context.Context, source, destination, ifMatch string) (string, error) {
	content, ok := s.objects[source]
	if !ok {
		return "", errors.New("NoSuchKey")
	}
	if ifMatch != "" && ifMatch != objectETag(content) {
		return "", errors.New("PreconditionFailed")
	}
	s.objects[destination] = content
	s.copies = append(s.copies, source+"->"+destination)
	if s.copyETag != "" {
		return s.copyETag, nil
	}
	return objectETag(content), nil
}

func (s *fakeObjectStore) delete(_ context.Context, key string) error {
	delete(s.objects, key)
	s.deleted = append(s.deleted, key)
	return nil
}

type fakeFileMetadata struct {
	verified  map[string]int64
	grants    map[string]bool
	pending   []string
	storeErrs []error
}

func grantKey(fileHash string, userID int64) string {
	return fmt.Sprintf("%s:%d", fileHash, userID)
}

func (m *fakeFileMetadata) hasAccess(fileHash string, userID int64) (bool, error) {
	return m.grants[grantKey(fileHash, userID)], nil
}

func (m *fakeFileMetadata) grant(fileHash string, userID int64) error {
	m.grants[grantKey(fileHash, userID)] = true
	return nil
}

func (m *fakeFileMetadata) storeVerified(fileHash string, fileSize int64, storagePath string, userID int64) error {
	if err := m.store(fileHash, fileSize, storagePath); err != nil {
		return err
	}
	return m.grant(fileHash, userID)
}

func (m *fakeFileMetadata) exists(fileHash string) (bool, error) {
	_, ok := m.verified[fileHash]
	return ok, nil
}

func (m *fakeFileMetadata) upsertPending(fileHash string, _ int64, storagePath string) error {
	m.pending = append(m.pending, fileHash+"@"+storagePath)
	return nil
}

func (m *fakeFileMetadata) store(fileHash string, fileSize int64, storagePath string) error {
	if len(m.storeErrs) > 0 {
		err := m.storeErrs[0]
		m.storeErrs = m.storeErrs[1:]
		if err != nil {
			return err
		}
	}
	if storagePath != rustfs.GetStoragePath(fileHash) {
		return fmt.Errorf("unexpected storage path %s", storagePath)
	}
	m.verified[fileHash] = fileSize
	return nil
}

func (m *fakeFileMetadata) install(handler *UploadHandler) {
	if m.verified == nil {
		m.verified = map[string]int64{}
	}
	if m.grants == nil {
		m.grants = map[string]bool{}
	}
	handler.fileExists = m.exists
	handler.hasFileAccess = m.hasAccess
	handler.grantFileAccess = m.grant
	handler.upsertPendingFileMetadata = m.upsertPending
	handler.storeFileMetadata = m.store
	handler.storeVerifiedFile = m.storeVerified
}

func newTestUploadHandler(store *fakeObjectStore, metadata *fakeFileMetadata) *UploadHandler {
	handler := &UploadHandler{
		objectExists:   store.exists,
		openObject:     store.open,
		copyObject:     store.copy,
		deleteObject:   store.delete,
		verifyFileHash: rustfs.VerifyFileHash,
	}
	metadata.install(handler)
	return handler
}

func requestUpload(t *testing.T, handler *UploadHandler, userID int64) (int, bool, string) {
	t.Helper()
	req := asUser(httptest.NewRequest(http.MethodPost, "/storage_service/upload", strings.NewReader(`{"file_hash":"`+payloadHash+`","file_size":7}`)), userID)
	rec := httptest.NewRecorder()
	handler.HandleUploadRequest(rec, req)
	var resp struct {
		Exists    bool   `json:"exists"`
		UploadURL string `json:"upload_url"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	return rec.Code, resp.Exists, resp.UploadURL
}

type storageVerifyResponse struct {
	Success      bool   `json:"success"`
	ErrorMessage string `json:"error_message"`
}

func verifyAs(t *testing.T, handler *UploadHandler, userID int64, fileHash string) (int, storageVerifyResponse) {
	t.Helper()
	req := asUser(httptest.NewRequest(http.MethodPost, "/storage_service/upload/verify", strings.NewReader(`{"file_hash":"`+fileHash+`"}`)), userID)
	rec := httptest.NewRecorder()
	handler.HandleVerifyUpload(rec, req)
	var response storageVerifyResponse
	if rec.Code == http.StatusOK || rec.Header().Get("Content-Type") == "application/json" {
		if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
			t.Fatalf("failed to decode verify response: %v body=%q", err, rec.Body.String())
		}
	}
	return rec.Code, response
}

func TestHandleUploadRequestDeduplicatesOnlyForGrantedUser(t *testing.T) {
	metadata := &fakeFileMetadata{
		verified: map[string]int64{payloadHash: 7},
		grants:   map[string]bool{grantKey(payloadHash, uploaderID): true},
	}
	handler := newTestUploadHandler(newFakeObjectStore(), metadata)
	var signedKeys []string
	handler.getPresignedUploadURL = func(_ context.Context, key string, _ time.Duration, _ string) (string, error) {
		signedKeys = append(signedKeys, key)
		return "https://example.com/upload", nil
	}

	if status, exists, url := requestUpload(t, handler, uploaderID); status != http.StatusOK || !exists || url != "" {
		t.Fatalf("granted user should skip the upload: status=%d exists=%v url=%q", status, exists, url)
	}
	// 只知道哈希的用户必须真正上传一次，不能凭哈希直接拿到已存在的文件。
	if status, exists, url := requestUpload(t, handler, otherID); status != http.StatusOK || exists || url != "https://example.com/upload" {
		t.Fatalf("ungranted user must prove possession: status=%d exists=%v url=%q", status, exists, url)
	}
	if len(signedKeys) != 1 || signedKeys[0] != rustfs.GetStagingPath(otherID, payloadHash) {
		t.Fatalf("possession proof must target the requester's staging path, signed %v", signedKeys)
	}
	if len(metadata.pending) != 0 {
		t.Fatalf("verified metadata must not be touched by a possession proof request: %v", metadata.pending)
	}
}

func TestHandleUploadRequestSignsRequesterStagingPathWithShortTTL(t *testing.T) {
	metadata := &fakeFileMetadata{}
	handler := newTestUploadHandler(newFakeObjectStore(), metadata)
	var signedKey, signedEndpoint string
	var signedTTL time.Duration
	handler.getPresignedUploadURL = func(_ context.Context, key string, ttl time.Duration, endpoint string) (string, error) {
		signedKey, signedTTL, signedEndpoint = key, ttl, endpoint
		return "https://example.com/upload", nil
	}

	req := asUser(httptest.NewRequest(http.MethodPost, "/storage_service/upload", strings.NewReader(`{"file_hash":"`+payloadHash+`","file_size":7}`)), uploaderID)
	req.Host = "storage.example.com:8081"
	req.Header.Set("X-Forwarded-Proto", "https")
	rec := httptest.NewRecorder()
	handler.HandleUploadRequest(rec, req)

	var resp struct {
		Exists    bool   `json:"exists"`
		UploadURL string `json:"upload_url"`
		ExpiresIn int64  `json:"expires_in"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || resp.Exists || resp.UploadURL != "https://example.com/upload" || resp.ExpiresIn != 900 {
		t.Fatalf("unexpected response: status=%d %+v", rec.Code, resp)
	}
	if signedKey != "staging/1001/"+payloadHash {
		t.Fatalf("upload URL must target the requester's staging path, got %s", signedKey)
	}
	if signedTTL != 15*time.Minute || signedEndpoint != "https://storage.example.com:9000" {
		t.Fatalf("unexpected presign options: ttl=%s endpoint=%s", signedTTL, signedEndpoint)
	}
	if len(metadata.pending) != 1 || metadata.pending[0] != payloadHash+"@"+rustfs.GetStoragePath(payloadHash) {
		t.Fatalf("unexpected pending metadata: %v", metadata.pending)
	}
}

func TestUploadEndpointsRequireAuthenticatedUser(t *testing.T) {
	handler := newTestUploadHandler(newFakeObjectStore(), &fakeFileMetadata{})
	for _, path := range []string{"/storage_service/upload", "/storage_service/upload/verify"} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"file_hash":"`+payloadHash+`","file_size":7}`))
		rec := httptest.NewRecorder()
		if strings.HasSuffix(path, "verify") {
			handler.HandleVerifyUpload(rec, req)
		} else {
			handler.HandleUploadRequest(rec, req)
		}
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s without authenticated user returned %d", path, rec.Code)
		}
	}
}

func TestVerifyPromotesStagedObjectToCanonicalPath(t *testing.T) {
	store := newFakeObjectStore()
	staging := rustfs.GetStagingPath(uploaderID, payloadHash)
	store.objects[staging] = payload
	metadata := &fakeFileMetadata{}
	handler := newTestUploadHandler(store, metadata)

	status, resp := verifyAs(t, handler, uploaderID, payloadHash)
	if status != http.StatusOK || !resp.Success {
		t.Fatalf("verify failed: status=%d %+v", status, resp)
	}
	if store.objects[rustfs.GetStoragePath(payloadHash)] != payload {
		t.Fatal("verified content was not copied to the canonical path")
	}
	if _, ok := store.objects[staging]; ok {
		t.Fatal("staging object should be removed after promotion")
	}
	if metadata.verified[payloadHash] != int64(len(payload)) {
		t.Fatalf("unexpected verified metadata: %v", metadata.verified)
	}
	if !metadata.grants[grantKey(payloadHash, uploaderID)] || len(metadata.grants) != 1 {
		t.Fatalf("only the uploader should be granted: %v", metadata.grants)
	}
}

func TestVerifyMismatchOnlyDeletesRequesterStagingObject(t *testing.T) {
	store := newFakeObjectStore()
	staging := rustfs.GetStagingPath(uploaderID, payloadHash)
	canonical := rustfs.GetStoragePath(payloadHash)
	store.objects[staging] = "forged"
	store.objects[canonical] = payload
	metadata := &fakeFileMetadata{}
	handler := newTestUploadHandler(store, metadata)

	status, resp := verifyAs(t, handler, uploaderID, payloadHash)
	if status != http.StatusOK || resp.Success || resp.ErrorMessage != "File hash mismatch" {
		t.Fatalf("mismatch was not reported: status=%d %+v", status, resp)
	}
	if len(store.deleted) != 1 || store.deleted[0] != staging {
		t.Fatalf("mismatch must only delete the requester's staging object, deleted %v", store.deleted)
	}
	if store.objects[canonical] != payload || len(store.copies) != 0 || len(metadata.verified) != 0 {
		t.Fatal("mismatch must not touch the canonical object or metadata")
	}
}

func TestVerifyCannotPromoteAnotherUsersStagedObject(t *testing.T) {
	store := newFakeObjectStore()
	staging := rustfs.GetStagingPath(uploaderID, payloadHash)
	store.objects[staging] = "forged"
	metadata := &fakeFileMetadata{}
	handler := newTestUploadHandler(store, metadata)

	status, resp := verifyAs(t, handler, otherID, payloadHash)
	if status != http.StatusOK || resp.Success || resp.ErrorMessage != "File not found in storage" {
		t.Fatalf("other user's verify was not rejected: status=%d %+v", status, resp)
	}
	if store.objects[staging] != "forged" || len(store.deleted) != 0 || len(store.copies) != 0 {
		t.Fatal("verify by another user must not read-modify the uploader's staging object")
	}
}

func verifiedFileStore() (*fakeObjectStore, *fakeFileMetadata) {
	store := newFakeObjectStore()
	store.objects[rustfs.GetStoragePath(payloadHash)] = payload
	return store, &fakeFileMetadata{
		verified: map[string]int64{payloadHash: int64(len(payload))},
		grants:   map[string]bool{grantKey(payloadHash, uploaderID): true},
	}
}

func TestVerifyForgedPossessionProofNeverRewritesVerifiedFile(t *testing.T) {
	store, metadata := verifiedFileStore()
	staging := rustfs.GetStagingPath(otherID, payloadHash)
	store.objects[staging] = "forged"
	handler := newTestUploadHandler(store, metadata)

	status, resp := verifyAs(t, handler, otherID, payloadHash)
	if status != http.StatusOK || resp.Success || resp.ErrorMessage != "File hash mismatch" {
		t.Fatalf("forged possession proof was accepted: status=%d %+v", status, resp)
	}
	if store.objects[rustfs.GetStoragePath(payloadHash)] != payload || len(store.copies) != 0 {
		t.Fatal("verified canonical object was rewritten")
	}
	if _, ok := store.objects[staging]; ok {
		t.Fatal("forged staging object should be cleaned up")
	}
	if metadata.grants[grantKey(payloadHash, otherID)] {
		t.Fatal("forged possession proof must not grant access")
	}
}

func TestVerifyPossessionProofGrantsWithoutCopy(t *testing.T) {
	store, metadata := verifiedFileStore()
	staging := rustfs.GetStagingPath(otherID, payloadHash)
	store.objects[staging] = payload
	handler := newTestUploadHandler(store, metadata)

	if status, resp := verifyAs(t, handler, otherID, payloadHash); status != http.StatusOK || !resp.Success {
		t.Fatalf("matching possession proof was rejected: status=%d %+v", status, resp)
	}
	if !metadata.grants[grantKey(payloadHash, otherID)] {
		t.Fatal("matching possession proof should grant access")
	}
	if _, ok := store.objects[staging]; ok || len(store.copies) != 0 {
		t.Fatalf("possession proof should only remove the staging object: copies=%v", store.copies)
	}
}

func TestVerifyVerifiedFileWithoutGrantRequiresUpload(t *testing.T) {
	store, metadata := verifiedFileStore()
	handler := newTestUploadHandler(store, metadata)

	status, resp := verifyAs(t, handler, otherID, payloadHash)
	if status != http.StatusOK || resp.Success || resp.ErrorMessage != "File not found in storage" {
		t.Fatalf("verify without a possession proof was accepted: status=%d %+v", status, resp)
	}
	if metadata.grants[grantKey(payloadHash, otherID)] {
		t.Fatal("verify without upload must not grant access")
	}
}

func TestVerifyGrantedUserIsIdempotentAndCleansStaging(t *testing.T) {
	store, metadata := verifiedFileStore()
	staging := rustfs.GetStagingPath(uploaderID, payloadHash)
	store.objects[staging] = "leftover"
	handler := newTestUploadHandler(store, metadata)

	if status, resp := verifyAs(t, handler, uploaderID, payloadHash); status != http.StatusOK || !resp.Success {
		t.Fatalf("verify by a granted user should be idempotent: status=%d %+v", status, resp)
	}
	if _, ok := store.objects[staging]; ok || len(store.copies) != 0 {
		t.Fatal("granted user's leftover staging object should be removed without copying")
	}
}

func TestVerifyRejectsStagedObjectChangedDuringCopy(t *testing.T) {
	store := newFakeObjectStore()
	store.objects[rustfs.GetStagingPath(uploaderID, payloadHash)] = payload
	store.copyETag = objectETag("forged")
	metadata := &fakeFileMetadata{}
	handler := newTestUploadHandler(store, metadata)

	status, resp := verifyAs(t, handler, uploaderID, payloadHash)
	if status != http.StatusOK || resp.Success || resp.ErrorMessage != "File changed during verification" {
		t.Fatalf("changed object was accepted: status=%d %+v", status, resp)
	}
	if len(metadata.verified) != 0 {
		t.Fatal("changed object must not be marked verified")
	}
}

func TestVerifyChecksCanonicalObjectWithoutDeletingIt(t *testing.T) {
	canonical := rustfs.GetStoragePath(payloadHash)

	store := newFakeObjectStore()
	store.objects[canonical] = payload
	metadata := &fakeFileMetadata{}
	if status, resp := verifyAs(t, newTestUploadHandler(store, metadata), uploaderID, payloadHash); status != http.StatusOK || !resp.Success {
		t.Fatalf("matching canonical object was not verified: status=%d %+v", status, resp)
	}
	if metadata.verified[payloadHash] != int64(len(payload)) {
		t.Fatalf("unexpected verified metadata: %v", metadata.verified)
	}
	if len(metadata.grants) != 0 {
		t.Fatalf("canonical-only verification proves no possession and must not grant: %v", metadata.grants)
	}

	store = newFakeObjectStore()
	store.objects[canonical] = "forged"
	metadata = &fakeFileMetadata{}
	status, resp := verifyAs(t, newTestUploadHandler(store, metadata), uploaderID, payloadHash)
	if status != http.StatusOK || resp.Success || resp.ErrorMessage != "File hash mismatch" {
		t.Fatalf("mismatching canonical object was accepted: status=%d %+v", status, resp)
	}
	if len(store.deleted) != 0 || len(metadata.verified) != 0 {
		t.Fatalf("canonical mismatch must not delete objects or write metadata: deleted=%v", store.deleted)
	}
}

func TestVerifyCanRetryAfterMetadataFailure(t *testing.T) {
	store := newFakeObjectStore()
	store.objects[rustfs.GetStagingPath(uploaderID, payloadHash)] = payload
	metadata := &fakeFileMetadata{storeErrs: []error{errors.New("database unavailable")}}
	handler := newTestUploadHandler(store, metadata)

	status, resp := verifyAs(t, handler, uploaderID, payloadHash)
	if status != http.StatusInternalServerError || resp.Success || resp.ErrorMessage != "Failed to save verified file metadata" {
		t.Fatalf("metadata failure was not reported: status=%d %+v", status, resp)
	}
	if _, ok := store.objects[rustfs.GetStagingPath(uploaderID, payloadHash)]; !ok {
		t.Fatal("staging object must survive a metadata failure so the retry can still grant the uploader")
	}
	if status, resp := verifyAs(t, handler, uploaderID, payloadHash); status != http.StatusOK || !resp.Success {
		t.Fatalf("retry did not complete: status=%d %+v", status, resp)
	}
	if metadata.verified[payloadHash] != int64(len(payload)) {
		t.Fatalf("unexpected verified metadata: %v", metadata.verified)
	}
	if !metadata.grants[grantKey(payloadHash, uploaderID)] {
		t.Fatal("retry should grant the uploader")
	}
	if _, ok := store.objects[rustfs.GetStagingPath(uploaderID, payloadHash)]; ok {
		t.Fatal("staging object should be removed after a successful retry")
	}
}

func TestUploadHandlersPropagateRequestCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	t.Run("upload URL", func(t *testing.T) {
		handler := newTestUploadHandler(newFakeObjectStore(), &fakeFileMetadata{})
		seenCanceled := false
		handler.getPresignedUploadURL = func(ctx context.Context, _ string, _ time.Duration, _ string) (string, error) {
			seenCanceled = ctx.Err() == context.Canceled
			return "", ctx.Err()
		}
		req := asUser(httptest.NewRequest(http.MethodPost, "/storage_service/upload", strings.NewReader(`{"file_hash":"`+payloadHash+`","file_size":10}`)).WithContext(ctx), uploaderID)
		rec := httptest.NewRecorder()
		handler.HandleUploadRequest(rec, req)
		if !seenCanceled || rec.Code != http.StatusInternalServerError {
			t.Fatalf("request cancellation was not propagated: seen=%v status=%d", seenCanceled, rec.Code)
		}
	})

	t.Run("upload verification", func(t *testing.T) {
		handler := newTestUploadHandler(newFakeObjectStore(), &fakeFileMetadata{})
		seenCanceled := false
		handler.objectExists = func(ctx context.Context, _ string) (bool, error) {
			seenCanceled = ctx.Err() == context.Canceled
			return false, ctx.Err()
		}
		req := asUser(httptest.NewRequest(http.MethodPost, "/storage_service/upload/verify", strings.NewReader(`{"file_hash":"`+payloadHash+`"}`)).WithContext(ctx), uploaderID)
		rec := httptest.NewRecorder()
		handler.HandleVerifyUpload(rec, req)
		if !seenCanceled || rec.Code != http.StatusInternalServerError {
			t.Fatalf("request cancellation was not propagated: seen=%v status=%d", seenCanceled, rec.Code)
		}
	})
}

func TestHandleUploadRequestRejectsOversizedFile(t *testing.T) {
	handler := newTestUploadHandler(newFakeObjectStore(), &fakeFileMetadata{})
	handler.getPresignedUploadURL = func(context.Context, string, time.Duration, string) (string, error) {
		t.Fatal("oversized upload must not be signed")
		return "", nil
	}
	body := fmt.Sprintf(`{"file_hash":"%s","file_size":%d}`, payloadHash, maxUploadFileBytes+1)
	rec := httptest.NewRecorder()
	handler.HandleUploadRequest(rec, asUser(httptest.NewRequest(http.MethodPost, "/storage_service/upload", strings.NewReader(body)), uploaderID))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized upload returned %d", rec.Code)
	}
}

func TestVerifyRejectsOversizedStagedObject(t *testing.T) {
	store := newFakeObjectStore()
	staging := rustfs.GetStagingPath(uploaderID, payloadHash)
	store.objects[staging] = payload
	metadata := &fakeFileMetadata{}
	handler := newTestUploadHandler(store, metadata)
	handler.maxFileBytes = int64(len(payload)) - 1

	status, resp := verifyAs(t, handler, uploaderID, payloadHash)
	if status != http.StatusOK || resp.Success || resp.ErrorMessage != "File too large" {
		t.Fatalf("oversized object accepted: status=%d %+v", status, resp)
	}
	if _, ok := store.objects[staging]; ok || len(store.copies) != 0 || len(metadata.verified) != 0 {
		t.Fatal("oversized staging object must be deleted without promotion")
	}

	// 恰好等于上限的对象仍然可以通过。
	store.objects[staging] = payload
	handler.maxFileBytes = int64(len(payload))
	if status, resp := verifyAs(t, handler, uploaderID, payloadHash); status != http.StatusOK || !resp.Success {
		t.Fatalf("object at the limit rejected: status=%d %+v", status, resp)
	}
}

func TestVerifyPossessionRejectsOversizedStagedObject(t *testing.T) {
	store := newFakeObjectStore()
	staging := rustfs.GetStagingPath(otherID, payloadHash)
	store.objects[staging] = payload
	metadata := &fakeFileMetadata{verified: map[string]int64{payloadHash: 7}}
	handler := newTestUploadHandler(store, metadata)
	handler.maxFileBytes = 1

	status, resp := verifyAs(t, handler, otherID, payloadHash)
	if status != http.StatusOK || resp.Success || resp.ErrorMessage != "File too large" {
		t.Fatalf("oversized possession proof accepted: status=%d %+v", status, resp)
	}
	if _, ok := store.objects[staging]; ok || metadata.grants[grantKey(payloadHash, otherID)] {
		t.Fatal("oversized possession proof must be deleted without granting access")
	}
}
