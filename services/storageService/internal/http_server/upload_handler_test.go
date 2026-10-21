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
	pending   []string
	storeErrs []error
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

func newTestUploadHandler(store *fakeObjectStore, metadata *fakeFileMetadata) *UploadHandler {
	if metadata.verified == nil {
		metadata.verified = map[string]int64{}
	}
	return &UploadHandler{
		fileExists:                metadata.exists,
		upsertPendingFileMetadata: metadata.upsertPending,
		storeFileMetadata:         metadata.store,
		objectExists:              store.exists,
		openObject:                store.open,
		copyObject:                store.copy,
		deleteObject:              store.delete,
		verifyFileHash:            rustfs.VerifyFileHash,
	}
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

func TestHandleUploadRequestReturnsExistsWithoutUploadURL(t *testing.T) {
	metadata := &fakeFileMetadata{verified: map[string]int64{payloadHash: 7}}
	handler := newTestUploadHandler(newFakeObjectStore(), metadata)
	handler.getPresignedUploadURL = func(context.Context, string, time.Duration, string) (string, error) {
		t.Fatal("verified files must not get an upload URL")
		return "", nil
	}

	req := asUser(httptest.NewRequest(http.MethodPost, "/storage_service/upload", strings.NewReader(`{"file_hash":"`+payloadHash+`","file_size":7}`)), uploaderID)
	rec := httptest.NewRecorder()
	handler.HandleUploadRequest(rec, req)

	var resp struct {
		Exists    bool   `json:"exists"`
		UploadURL string `json:"upload_url"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || !resp.Exists || resp.UploadURL != "" {
		t.Fatalf("unexpected response: status=%d %+v", rec.Code, resp)
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

func TestVerifyNeverRewritesAlreadyVerifiedFile(t *testing.T) {
	store := newFakeObjectStore()
	staging := rustfs.GetStagingPath(otherID, payloadHash)
	canonical := rustfs.GetStoragePath(payloadHash)
	store.objects[staging] = "forged"
	store.objects[canonical] = payload
	metadata := &fakeFileMetadata{verified: map[string]int64{payloadHash: int64(len(payload))}}
	handler := newTestUploadHandler(store, metadata)

	status, resp := verifyAs(t, handler, otherID, payloadHash)
	if status != http.StatusOK || !resp.Success {
		t.Fatalf("verify of a verified file should be idempotent: status=%d %+v", status, resp)
	}
	if store.objects[canonical] != payload || len(store.copies) != 0 {
		t.Fatal("verified canonical object was rewritten")
	}
	if _, ok := store.objects[staging]; ok {
		t.Fatal("requester's leftover staging object should be cleaned up")
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
	if status, resp := verifyAs(t, handler, uploaderID, payloadHash); status != http.StatusOK || !resp.Success {
		t.Fatalf("retry did not complete from the canonical object: status=%d %+v", status, resp)
	}
	if metadata.verified[payloadHash] != int64(len(payload)) {
		t.Fatalf("unexpected verified metadata: %v", metadata.verified)
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
