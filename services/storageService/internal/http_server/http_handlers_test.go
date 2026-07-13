package http_server

import (
	"Betterfly2/shared/db"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

var (
	knownFileHash    = strings.Repeat("a", sha512HexLength)
	newFileHash      = strings.Repeat("b", sha512HexLength)
	missingFileHash  = strings.Repeat("c", sha512HexLength)
	verifiedFileHash = strings.Repeat("d", sha512HexLength)
	pendingFileHash  = strings.Repeat("e", sha512HexLength)
)

func TestHandleUploadRequest_ReturnsExistsWhenVerifiedFileAlreadyPresent(t *testing.T) {
	handler := &UploadHandler{
		fileExists: func(fileHash string) (bool, error) {
			if fileHash != knownFileHash {
				t.Fatalf("unexpected file hash: %s", fileHash)
			}
			return true, nil
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/storage_service/upload", strings.NewReader(`{"file_hash":"`+knownFileHash+`","file_size":123}`))
	rec := httptest.NewRecorder()

	handler.HandleUploadRequest(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	var resp struct {
		Exists    bool   `json:"exists"`
		UploadURL string `json:"upload_url"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if !resp.Exists {
		t.Fatal("expected exists=true")
	}
	if resp.UploadURL != "" {
		t.Fatalf("expected empty upload_url, got %q", resp.UploadURL)
	}
}

func TestHandleUploadRequest_CreatesPendingMetadataAndReturnsUploadURL(t *testing.T) {
	calledPending := false
	handler := &UploadHandler{
		fileExists: func(string) (bool, error) { return false, nil },
		upsertPendingFileMetadata: func(fileHash string, fileSize int64, storagePath string) error {
			calledPending = true
			if fileHash != newFileHash || fileSize != 456 || storagePath != "bb/"+newFileHash {
				t.Fatalf("unexpected pending metadata: hash=%s size=%d path=%s", fileHash, fileSize, storagePath)
			}
			return nil
		},
		getPresignedUploadURLFor: func(ctx context.Context, fileHash string, expiresIn time.Duration, endpoint string) (string, error) {
			if fileHash != newFileHash {
				t.Fatalf("unexpected file hash: %s", fileHash)
			}
			if expiresIn != time.Hour {
				t.Fatalf("unexpected expiresIn: %s", expiresIn)
			}
			if endpoint != "https://storage.example.com:9000" {
				t.Fatalf("unexpected endpoint: %s", endpoint)
			}
			return "https://example.com/upload", nil
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/storage_service/upload", strings.NewReader(`{"file_hash":"`+newFileHash+`","file_size":456}`))
	req.Host = "storage.example.com:8081"
	req.Header.Set("X-Forwarded-Proto", "https")
	rec := httptest.NewRecorder()

	handler.HandleUploadRequest(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	if !calledPending {
		t.Fatal("expected pending metadata to be stored")
	}

	var resp struct {
		Exists    bool   `json:"exists"`
		UploadURL string `json:"upload_url"`
		ExpiresIn int64  `json:"expires_in"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Exists {
		t.Fatal("expected exists=false")
	}
	if resp.UploadURL != "https://example.com/upload" {
		t.Fatalf("unexpected upload URL: %s", resp.UploadURL)
	}
	if resp.ExpiresIn != 3600 {
		t.Fatalf("unexpected expires_in: %d", resp.ExpiresIn)
	}
}

func TestFileHandlersPropagateRequestCancellation(t *testing.T) {
	t.Run("upload URL", func(t *testing.T) {
		seenCanceled := false
		handler := &UploadHandler{
			fileExists:                func(string) (bool, error) { return false, nil },
			upsertPendingFileMetadata: func(string, int64, string) error { return nil },
			getPresignedUploadURLFor: func(ctx context.Context, _ string, _ time.Duration, _ string) (string, error) {
				seenCanceled = ctx.Err() == context.Canceled
				return "", ctx.Err()
			},
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		req := httptest.NewRequest(http.MethodPost, "/storage_service/upload", strings.NewReader(`{"file_hash":"`+newFileHash+`","file_size":10}`)).WithContext(ctx)
		rec := httptest.NewRecorder()
		handler.HandleUploadRequest(rec, req)
		if !seenCanceled || rec.Code != http.StatusInternalServerError {
			t.Fatalf("request cancellation was not propagated: seen=%v status=%d", seenCanceled, rec.Code)
		}
	})

	t.Run("upload verification", func(t *testing.T) {
		seenCanceled := false
		handler := &UploadHandler{fileExistsInStorage: func(ctx context.Context, _ string) (bool, error) {
			seenCanceled = ctx.Err() == context.Canceled
			return false, ctx.Err()
		}}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		req := httptest.NewRequest(http.MethodPost, "/storage_service/upload/verify", strings.NewReader(`{"file_hash":"`+newFileHash+`"}`)).WithContext(ctx)
		rec := httptest.NewRecorder()
		handler.HandleVerifyUpload(rec, req)
		if !seenCanceled || rec.Code != http.StatusInternalServerError {
			t.Fatalf("request cancellation was not propagated: seen=%v status=%d", seenCanceled, rec.Code)
		}
	})

	t.Run("download", func(t *testing.T) {
		seenCanceled := false
		handler := &DownloadHandler{
			getFileMetadata: func(string) (*db.FileMetadata, error) {
				return &db.FileMetadata{IsVerified: true}, nil
			},
			fileExistsInStorage: func(ctx context.Context, _ string) (bool, error) {
				seenCanceled = ctx.Err() == context.Canceled
				return false, ctx.Err()
			},
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		req := httptest.NewRequest(http.MethodGet, "/storage_service/download?file_hash="+newFileHash, nil).WithContext(ctx)
		rec := httptest.NewRecorder()
		handler.HandleDownloadRequest(rec, req)
		if !seenCanceled || rec.Code != http.StatusInternalServerError {
			t.Fatalf("request cancellation was not propagated: seen=%v status=%d", seenCanceled, rec.Code)
		}
	})
}

func TestFileHandlersRejectMalformedSHA512BeforeStorageAccess(t *testing.T) {
	invalidHashes := []string{"", "short", "../escape", strings.Repeat("g", sha512HexLength), strings.Repeat("a", sha512HexLength+1)}
	for _, fileHash := range invalidHashes {
		t.Run(fileHash, func(t *testing.T) {
			storageCalled := false
			upload := &UploadHandler{fileExists: func(string) (bool, error) {
				storageCalled = true
				return false, nil
			}}
			uploadRequest := httptest.NewRequest(http.MethodPost, "/storage_service/upload", strings.NewReader(`{"file_hash":"`+fileHash+`","file_size":10}`))
			uploadResponse := httptest.NewRecorder()
			upload.HandleUploadRequest(uploadResponse, uploadRequest)
			if uploadResponse.Code != http.StatusBadRequest || storageCalled {
				t.Fatalf("invalid hash reached storage: status=%d called=%v", uploadResponse.Code, storageCalled)
			}

			download := &DownloadHandler{getFileMetadata: func(string) (*db.FileMetadata, error) {
				storageCalled = true
				return nil, nil
			}}
			downloadRequest := httptest.NewRequest(http.MethodGet, "/storage_service/download?file_hash="+fileHash, nil)
			downloadResponse := httptest.NewRecorder()
			download.HandleDownloadRequest(downloadResponse, downloadRequest)
			if downloadResponse.Code != http.StatusBadRequest || storageCalled {
				t.Fatalf("invalid hash reached metadata lookup: status=%d called=%v", downloadResponse.Code, storageCalled)
			}
		})
	}
}

func TestNormalizeFileHashAcceptsUppercaseAndCanonicalizesIt(t *testing.T) {
	upper := strings.Repeat("AB", sha512HexLength/2)
	got, ok := normalizeFileHash(upper)
	if !ok || got != strings.ToLower(upper) {
		t.Fatalf("valid uppercase SHA-512 was not normalized: ok=%v hash=%q", ok, got)
	}
}

func TestUploadRejectsOversizedRequestBody(t *testing.T) {
	handler := &UploadHandler{fileExists: func(string) (bool, error) {
		t.Fatal("oversized request must not reach storage")
		return false, nil
	}}
	request := httptest.NewRequest(http.MethodPost, "/storage_service/upload", strings.NewReader(strings.Repeat("x", maxStorageRequestBodyBytes+1)))
	response := httptest.NewRecorder()
	handler.HandleUploadRequest(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("oversized request was accepted: status=%d", response.Code)
	}
}

func TestHandleVerifyUpload_RemovesPendingMetadataWhenObjectMissing(t *testing.T) {
	deleted := false
	handler := &UploadHandler{
		fileExistsInStorage: func(context.Context, string) (bool, error) { return false, nil },
		deleteFileMetadata: func(fileHash string) error {
			deleted = true
			if fileHash != missingFileHash {
				t.Fatalf("unexpected file hash: %s", fileHash)
			}
			return nil
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/storage_service/upload/verify", strings.NewReader(`{"file_hash":"`+missingFileHash+`"}`))
	rec := httptest.NewRecorder()

	handler.HandleVerifyUpload(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	if !deleted {
		t.Fatal("expected metadata deletion when object is missing")
	}

	var resp struct {
		Success      bool   `json:"success"`
		ErrorMessage string `json:"error_message"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Success {
		t.Fatal("expected success=false")
	}
	if resp.ErrorMessage != "File not found in storage" {
		t.Fatalf("unexpected error message: %s", resp.ErrorMessage)
	}
}

func TestHandleVerifyUpload_MarksFileVerifiedOnSuccess(t *testing.T) {
	updated := false
	handler := &UploadHandler{
		fileExistsInStorage: func(context.Context, string) (bool, error) { return true, nil },
		downloadFile: func(context.Context, string) (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewBufferString("payload")), nil
		},
		verifyFileHash: func(reader io.Reader, expectedHash string) (bool, error) {
			data, err := io.ReadAll(reader)
			if err != nil {
				return false, err
			}
			if string(data) != "payload" || expectedHash != verifiedFileHash {
				t.Fatalf("unexpected verify payload: data=%q hash=%s", string(data), expectedHash)
			}
			return true, nil
		},
		updateFileMetadata: func(fileHash string, fileSize int64, storagePath string) error {
			updated = true
			if fileHash != verifiedFileHash || fileSize != int64(len("payload")) || storagePath != "dd/"+verifiedFileHash {
				t.Fatalf("unexpected metadata update: hash=%s size=%d path=%s", fileHash, fileSize, storagePath)
			}
			return nil
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/storage_service/upload/verify", strings.NewReader(`{"file_hash":"`+verifiedFileHash+`"}`))
	rec := httptest.NewRecorder()

	handler.HandleVerifyUpload(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	if !updated {
		t.Fatal("expected verified metadata update")
	}

	var resp struct {
		Success bool `json:"success"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if !resp.Success {
		t.Fatal("expected success=true")
	}
}

func newMetadataVerifyHandler(update func(string, int64, string) error, store func(string, int64, string) error, deleted *bool) *UploadHandler {
	return &UploadHandler{
		fileExistsInStorage: func(context.Context, string) (bool, error) { return true, nil },
		downloadFile: func(context.Context, string) (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewBufferString("payload")), nil
		},
		verifyFileHash: func(reader io.Reader, _ string) (bool, error) {
			_, err := io.Copy(io.Discard, reader)
			return err == nil, err
		},
		updateFileMetadata: update,
		storeFileMetadata:  store,
		deleteFile: func(context.Context, string) error {
			if deleted != nil {
				*deleted = true
			}
			return nil
		},
	}
}

func verifyUpload(t *testing.T, handler *UploadHandler) (int, storageVerifyResponse) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/storage_service/upload/verify", strings.NewReader(`{"file_hash":"`+verifiedFileHash+`"}`))
	rec := httptest.NewRecorder()
	handler.HandleVerifyUpload(rec, req)
	var response storageVerifyResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("failed to decode verify response: %v body=%q", err, rec.Body.String())
	}
	return rec.Code, response
}

type storageVerifyResponse struct {
	Success      bool   `json:"success"`
	ErrorMessage string `json:"error_message"`
}

func TestHandleVerifyUploadFallsBackToCreateWhenPendingMetadataIsMissing(t *testing.T) {
	storeCalls := 0
	handler := newMetadataVerifyHandler(
		func(string, int64, string) error { return errors.New("record not found") },
		func(string, int64, string) error {
			storeCalls++
			return nil
		},
		nil,
	)
	status, response := verifyUpload(t, handler)
	if status != http.StatusOK || !response.Success || storeCalls != 1 {
		t.Fatalf("fallback did not complete verification: status=%d response=%+v store_calls=%d", status, response, storeCalls)
	}
}

func TestHandleVerifyUploadReturnsFailureWhenBothMetadataWritesFail(t *testing.T) {
	deleted := false
	handler := newMetadataVerifyHandler(
		func(string, int64, string) error { return errors.New("update database unavailable") },
		func(string, int64, string) error { return errors.New("fallback database unavailable") },
		&deleted,
	)
	status, response := verifyUpload(t, handler)
	if status != http.StatusInternalServerError || response.Success {
		t.Fatalf("dual metadata failure was reported as success: status=%d response=%+v", status, response)
	}
	if response.ErrorMessage != "Failed to save verified file metadata" {
		t.Fatalf("unexpected public error: %q", response.ErrorMessage)
	}
	if deleted {
		t.Fatal("verified object was deleted after metadata failure")
	}
}

func TestHandleVerifyUploadCanRetryAfterMetadataFailure(t *testing.T) {
	updateCalls := 0
	storeCalls := 0
	deleted := false
	handler := newMetadataVerifyHandler(
		func(string, int64, string) error {
			updateCalls++
			if updateCalls == 1 {
				return errors.New("temporary update failure")
			}
			return nil
		},
		func(string, int64, string) error {
			storeCalls++
			return errors.New("temporary fallback failure")
		},
		&deleted,
	)
	if status, response := verifyUpload(t, handler); status != http.StatusInternalServerError || response.Success {
		t.Fatalf("first verify unexpectedly succeeded: status=%d response=%+v", status, response)
	}
	if status, response := verifyUpload(t, handler); status != http.StatusOK || !response.Success {
		t.Fatalf("retry did not complete: status=%d response=%+v", status, response)
	}
	if updateCalls != 2 || storeCalls != 1 || deleted {
		t.Fatalf("unexpected retry side effects: updates=%d stores=%d deleted=%v", updateCalls, storeCalls, deleted)
	}
}

func TestHandleVerifyUploadIsIdempotentForVerifiedMetadata(t *testing.T) {
	updateCalls := 0
	handler := newMetadataVerifyHandler(
		func(string, int64, string) error {
			updateCalls++
			return nil
		},
		func(string, int64, string) error {
			t.Fatal("verified metadata must not use fallback")
			return nil
		},
		nil,
	)
	for i := 0; i < 2; i++ {
		if status, response := verifyUpload(t, handler); status != http.StatusOK || !response.Success {
			t.Fatalf("verify %d failed: status=%d response=%+v", i+1, status, response)
		}
	}
	if updateCalls != 2 {
		t.Fatalf("expected both verifies to update idempotently, got %d calls", updateCalls)
	}
}

func TestHandleDownloadRequest_RejectsUnverifiedFile(t *testing.T) {
	handler := &DownloadHandler{
		getFileMetadata: func(fileHash string) (*db.FileMetadata, error) {
			return &db.FileMetadata{
				FileHash:   fileHash,
				FileSize:   321,
				IsVerified: false,
			}, nil
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/storage_service/download?file_hash="+pendingFileHash, nil)
	rec := httptest.NewRecorder()

	handler.HandleDownloadRequest(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	var resp struct {
		Exists       bool   `json:"exists"`
		ErrorMessage string `json:"error_message"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Exists {
		t.Fatal("expected exists=false")
	}
	if resp.ErrorMessage != "File not verified" {
		t.Fatalf("unexpected error message: %s", resp.ErrorMessage)
	}
}

func TestHandleDownloadRequest_ReturnsDownloadURLForVerifiedFile(t *testing.T) {
	handler := &DownloadHandler{
		getFileMetadata: func(fileHash string) (*db.FileMetadata, error) {
			return &db.FileMetadata{
				FileHash:   fileHash,
				FileSize:   789,
				IsVerified: true,
			}, nil
		},
		fileExistsInStorage: func(context.Context, string) (bool, error) { return true, nil },
		getPresignedDownloadFor: func(ctx context.Context, fileHash string, expiresIn time.Duration, endpoint string) (string, error) {
			if fileHash != verifiedFileHash {
				t.Fatalf("unexpected file hash: %s", fileHash)
			}
			if expiresIn != time.Hour {
				t.Fatalf("unexpected expiresIn: %s", expiresIn)
			}
			if endpoint != "https://storage.example.com:9000" {
				t.Fatalf("unexpected endpoint: %s", endpoint)
			}
			return "https://example.com/download", nil
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/storage_service/download?file_hash="+verifiedFileHash, nil)
	req.Host = "storage.example.com:8081"
	req.Header.Set("X-Forwarded-Proto", "https")
	rec := httptest.NewRecorder()

	handler.HandleDownloadRequest(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	var resp struct {
		Exists      bool   `json:"exists"`
		DownloadURL string `json:"download_url"`
		ExpiresIn   int64  `json:"expires_in"`
		FileSize    int64  `json:"file_size"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if !resp.Exists {
		t.Fatal("expected exists=true")
	}
	if resp.DownloadURL != "https://example.com/download" {
		t.Fatalf("unexpected download URL: %s", resp.DownloadURL)
	}
	if resp.ExpiresIn != 3600 {
		t.Fatalf("unexpected expires_in: %d", resp.ExpiresIn)
	}
	if resp.FileSize != 789 {
		t.Fatalf("unexpected file size: %d", resp.FileSize)
	}
}

func TestResolveRustFSExternalEndpoint_UsesExplicitConfigWhenPresent(t *testing.T) {
	t.Setenv("RUSTFS_EXTERNAL_ENDPOINT_URL", "https://files.example.com")

	req := httptest.NewRequest(http.MethodGet, "/storage_service/upload", nil)
	req.Host = "storage.example.com:8081"
	req.Header.Set("X-Forwarded-Proto", "https")

	if got := resolveRustFSExternalEndpoint(req); got != "https://files.example.com" {
		t.Fatalf("unexpected endpoint: %s", got)
	}
}

func TestResolveRustFSExternalEndpoint_FromRequestAndProxyHeaders(t *testing.T) {
	tests := []struct {
		name         string
		host         string
		proto        string
		tls          bool
		externalHost string
		externalPort string
		want         string
	}{
		{name: "forwarded HTTPS", host: "chat.example.com:8080", proto: "https, http", want: "https://chat.example.com:9000"},
		{name: "request TLS", host: "files.example.com", tls: true, want: "https://files.example.com:9000"},
		{name: "IPv6 request", host: "[2001:db8::1]:8080", want: "http://[2001:db8::1]:9000"},
		{name: "configured host and port", host: "ignored.example.com", externalHost: "cdn.example.com", externalPort: "9443", want: "http://cdn.example.com:9443"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("RUSTFS_EXTERNAL_ENDPOINT_URL", "")
			t.Setenv("RUSTFS_EXTERNAL_SCHEME", "")
			t.Setenv("RUSTFS_EXTERNAL_HOST", tt.externalHost)
			t.Setenv("RUSTFS_EXTERNAL_PORT", tt.externalPort)
			req := httptest.NewRequest(http.MethodGet, "/storage_service/upload", nil)
			req.Host = tt.host
			req.Header.Set("X-Forwarded-Proto", tt.proto)
			if tt.tls {
				req.TLS = &tls.ConnectionState{}
			}
			if got := resolveRustFSExternalEndpoint(req); got != tt.want {
				t.Fatalf("endpoint=%q want %q", got, tt.want)
			}
		})
	}
}

func TestReadinessHandler_ReturnsReadyWhenDependenciesHealthy(t *testing.T) {
	handler := &ReadinessHandler{
		pingDB:             func(context.Context) error { return nil },
		checkObjectStorage: func(context.Context) error { return nil },
	}

	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	rec := httptest.NewRecorder()

	handler.HandleReady(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	var resp readinessResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if !resp.Ready {
		t.Fatal("expected ready=true")
	}
}

func TestReadinessHandler_ReturnsServiceUnavailableWhenDependencyFails(t *testing.T) {
	handler := &ReadinessHandler{
		pingDB: func(context.Context) error { return nil },
		checkObjectStorage: func(context.Context) error {
			return context.DeadlineExceeded
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	rec := httptest.NewRecorder()

	handler.HandleReady(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status 503, got %d", rec.Code)
	}

	var resp readinessResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Ready {
		t.Fatal("expected ready=false")
	}
	if !strings.Contains(resp.ErrorMessage, "object storage not ready") {
		t.Fatalf("unexpected error message: %s", resp.ErrorMessage)
	}
}
