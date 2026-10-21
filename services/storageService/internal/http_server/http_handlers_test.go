package http_server

import (
	"Betterfly2/shared/db"
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

var (
	newFileHash      = strings.Repeat("b", sha512HexLength)
	verifiedFileHash = strings.Repeat("d", sha512HexLength)
	pendingFileHash  = strings.Repeat("e", sha512HexLength)
)

func allowDownload(string, int64) (bool, error) { return true, nil }

func TestFileHandlersPropagateRequestCancellation(t *testing.T) {
	t.Run("download", func(t *testing.T) {
		seenCanceled := false
		handler := &DownloadHandler{
			getFileMetadata: func(string) (*db.FileMetadata, error) {
				return &db.FileMetadata{IsVerified: true}, nil
			},
			canDownloadFile: allowDownload,
			fileExistsInStorage: func(ctx context.Context, _ string) (bool, error) {
				seenCanceled = ctx.Err() == context.Canceled
				return false, ctx.Err()
			},
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		req := asUser(httptest.NewRequest(http.MethodGet, "/storage_service/download?file_hash="+newFileHash, nil).WithContext(ctx), uploaderID)
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
			uploadRequest := asUser(httptest.NewRequest(http.MethodPost, "/storage_service/upload", strings.NewReader(`{"file_hash":"`+fileHash+`","file_size":10}`)), uploaderID)
			uploadResponse := httptest.NewRecorder()
			upload.HandleUploadRequest(uploadResponse, uploadRequest)
			if uploadResponse.Code != http.StatusBadRequest || storageCalled {
				t.Fatalf("invalid hash reached storage: status=%d called=%v", uploadResponse.Code, storageCalled)
			}

			download := &DownloadHandler{getFileMetadata: func(string) (*db.FileMetadata, error) {
				storageCalled = true
				return nil, nil
			}}
			downloadRequest := asUser(httptest.NewRequest(http.MethodGet, "/storage_service/download?file_hash="+fileHash, nil), uploaderID)
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
	request := asUser(httptest.NewRequest(http.MethodPost, "/storage_service/upload", strings.NewReader(strings.Repeat("x", maxStorageRequestBodyBytes+1))), uploaderID)
	response := httptest.NewRecorder()
	handler.HandleUploadRequest(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("oversized request was accepted: status=%d", response.Code)
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
		canDownloadFile: allowDownload,
	}

	req := asUser(httptest.NewRequest(http.MethodGet, "/storage_service/download?file_hash="+pendingFileHash, nil), uploaderID)
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
		canDownloadFile:     allowDownload,
		fileExistsInStorage: func(context.Context, string) (bool, error) { return true, nil },
		getPresignedDownloadFor: func(ctx context.Context, fileHash string, expiresIn time.Duration, endpoint string) (string, error) {
			if fileHash != verifiedFileHash {
				t.Fatalf("unexpected file hash: %s", fileHash)
			}
			if expiresIn != 10*time.Minute {
				t.Fatalf("unexpected expiresIn: %s", expiresIn)
			}
			if endpoint != "https://storage.example.com:9000" {
				t.Fatalf("unexpected endpoint: %s", endpoint)
			}
			return "https://example.com/download", nil
		},
	}

	req := asUser(httptest.NewRequest(http.MethodGet, "/storage_service/download?file_hash="+verifiedFileHash, nil), uploaderID)
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
	if resp.ExpiresIn != 600 {
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

func TestHandleDownloadRequestRequiresAuthenticatedUser(t *testing.T) {
	handler := &DownloadHandler{getFileMetadata: func(string) (*db.FileMetadata, error) {
		t.Fatal("unauthenticated download reached metadata lookup")
		return nil, nil
	}}
	rec := httptest.NewRecorder()
	handler.HandleDownloadRequest(rec, httptest.NewRequest(http.MethodGet, "/storage_service/download?file_hash="+verifiedFileHash, nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestHandleDownloadRequestHidesUnauthorizedFileAsNotFound(t *testing.T) {
	var checkedHash string
	var checkedUser int64
	handler := &DownloadHandler{
		getFileMetadata: func(fileHash string) (*db.FileMetadata, error) {
			return &db.FileMetadata{FileHash: fileHash, FileSize: 7, IsVerified: true}, nil
		},
		canDownloadFile: func(fileHash string, userID int64) (bool, error) {
			checkedHash, checkedUser = fileHash, userID
			return false, nil
		},
		fileExistsInStorage: func(context.Context, string) (bool, error) {
			t.Fatal("unauthorized download reached object storage")
			return false, nil
		},
	}
	rec := httptest.NewRecorder()
	handler.HandleDownloadRequest(rec, asUser(httptest.NewRequest(http.MethodGet, "/storage_service/download?file_hash="+strings.ToUpper(verifiedFileHash), nil), otherID))
	var resp struct {
		Exists       bool   `json:"exists"`
		DownloadURL  string `json:"download_url"`
		ErrorMessage string `json:"error_message"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || resp.Exists || resp.DownloadURL != "" || resp.ErrorMessage != "File not found" {
		t.Fatalf("unauthorized download was not hidden: status=%d %+v", rec.Code, resp)
	}
	if checkedHash != verifiedFileHash || checkedUser != otherID {
		t.Fatalf("authorization used hash=%s user=%d", checkedHash, checkedUser)
	}
}
