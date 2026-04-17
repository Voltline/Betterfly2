package http_server

import (
	"Betterfly2/shared/db"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHandleUploadRequest_ReturnsExistsWhenVerifiedFileAlreadyPresent(t *testing.T) {
	handler := &UploadHandler{
		fileExists: func(fileHash string) (bool, error) {
			if fileHash != "known-hash" {
				t.Fatalf("unexpected file hash: %s", fileHash)
			}
			return true, nil
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/storage_service/upload", strings.NewReader(`{"file_hash":"known-hash","file_size":123}`))
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
			if fileHash != "new-hash" || fileSize != 456 || storagePath != "ne/new-hash" {
				t.Fatalf("unexpected pending metadata: hash=%s size=%d path=%s", fileHash, fileSize, storagePath)
			}
			return nil
		},
		getPresignedUploadURLFor: func(ctx context.Context, fileHash string, expiresIn time.Duration, endpoint string) (string, error) {
			if fileHash != "new-hash" {
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

	req := httptest.NewRequest(http.MethodPost, "/storage_service/upload", strings.NewReader(`{"file_hash":"new-hash","file_size":456}`))
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

func TestHandleVerifyUpload_RemovesPendingMetadataWhenObjectMissing(t *testing.T) {
	deleted := false
	handler := &UploadHandler{
		fileExistsInStorage: func(context.Context, string) (bool, error) { return false, nil },
		deleteFileMetadata: func(fileHash string) error {
			deleted = true
			if fileHash != "missing-hash" {
				t.Fatalf("unexpected file hash: %s", fileHash)
			}
			return nil
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/storage_service/upload/verify", strings.NewReader(`{"file_hash":"missing-hash"}`))
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
			if string(data) != "payload" || expectedHash != "verified-hash" {
				t.Fatalf("unexpected verify payload: data=%q hash=%s", string(data), expectedHash)
			}
			return true, nil
		},
		updateFileMetadata: func(fileHash string, fileSize int64, storagePath string) error {
			updated = true
			if fileHash != "verified-hash" || fileSize != int64(len("payload")) || storagePath != "ve/verified-hash" {
				t.Fatalf("unexpected metadata update: hash=%s size=%d path=%s", fileHash, fileSize, storagePath)
			}
			return nil
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/storage_service/upload/verify", strings.NewReader(`{"file_hash":"verified-hash"}`))
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

	req := httptest.NewRequest(http.MethodGet, "/storage_service/download?file_hash=pending-hash", nil)
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
			if fileHash != "verified-hash" {
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

	req := httptest.NewRequest(http.MethodGet, "/storage_service/download?file_hash=verified-hash", nil)
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
