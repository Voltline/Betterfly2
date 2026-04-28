package http_server

import (
	"Betterfly2/proto/storage"
	"Betterfly2/shared/db"
	"Betterfly2/shared/logger"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"google.golang.org/protobuf/proto"
	"storageService/internal/rustfs"
)

// UploadHandler 处理文件上传请求
type UploadHandler struct {
	fileExists                func(string) (bool, error)
	upsertPendingFileMetadata func(string, int64, string) error
	deleteFileMetadata        func(string) error
	updateFileMetadata        func(string, int64, string) error
	storeFileMetadata         func(string, int64, string) error
	fileExistsInStorage       func(context.Context, string) (bool, error)
	downloadFile              func(context.Context, string) (io.ReadCloser, error)
	deleteFile                func(context.Context, string) error
	getPresignedUploadURL     func(context.Context, string, time.Duration) (string, error)
	getPresignedUploadURLFor  func(context.Context, string, time.Duration, string) (string, error)
	verifyFileHash            func(io.Reader, string) (bool, error)
}

type countingReader struct {
	reader io.Reader
	bytes  int64
}

func (r *countingReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.bytes += int64(n)
	return n, err
}

// NewUploadHandler 创建新的上传处理器
func NewUploadHandler(rustfsClient *rustfs.RustFSClient) *UploadHandler {
	return &UploadHandler{
		fileExists:                db.FileExists,
		upsertPendingFileMetadata: db.UpsertPendingFileMetadata,
		deleteFileMetadata:        db.DeleteFileMetadata,
		updateFileMetadata:        db.UpdateFileMetadata,
		storeFileMetadata:         db.StoreFileMetadata,
		fileExistsInStorage:       rustfsClient.FileExists,
		downloadFile:              rustfsClient.DownloadFile,
		deleteFile:                rustfsClient.DeleteFile,
		getPresignedUploadURL:     rustfsClient.GetPresignedUploadURL,
		getPresignedUploadURLFor:  rustfsClient.GetPresignedUploadURLForEndpoint,
		verifyFileHash:            rustfs.VerifyFileHash,
	}
}

// HandleUploadRequest 处理上传请求（第一阶段：获取上传URL）
func (h *UploadHandler) HandleUploadRequest(w http.ResponseWriter, r *http.Request) {
	sugar := logger.Sugar()

	// 解析请求
	var req storage.UploadFileRequest
	body, err := io.ReadAll(r.Body)
	if err != nil {
		sugar.Errorf("读取请求体失败: %v", err)
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}

	// 尝试解析为JSON（客户端可能使用JSON）
	if err := json.Unmarshal(body, &req); err != nil {
		// 如果JSON解析失败，尝试解析为Protobuf
		if err := proto.Unmarshal(body, &req); err != nil {
			sugar.Errorf("解析请求失败: %v", err)
			http.Error(w, "Failed to parse request", http.StatusBadRequest)
			return
		}
	}

	fileHash := req.FileHash
	fileSize := req.FileSize

	if fileHash == "" {
		http.Error(w, "file_hash is required", http.StatusBadRequest)
		return
	}

	if fileSize <= 0 {
		http.Error(w, "file_size must be greater than 0", http.StatusBadRequest)
		return
	}

	sugar.Debugf("收到上传请求: file_hash=%s, file_size=%d", fileHash, fileSize)

	storagePath := rustfs.GetStoragePath(fileHash)

	// 检查文件是否已存在
	exists, err := h.fileExists(fileHash)
	if err != nil {
		sugar.Errorf("查询文件是否存在失败: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	if exists {
		// 文件已存在，返回exists=true
		resp := &storage.UploadFileResponse{
			Exists: true,
		}
		h.sendResponse(w, resp)
		sugar.Debugf("文件已存在: file_hash=%s", fileHash)
		return
	}

	// 记录待验证状态，确保上传完成前后元数据状态可追踪。
	if err := h.upsertPendingFileMetadata(fileHash, fileSize, storagePath); err != nil {
		sugar.Errorf("记录待验证文件元数据失败: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// 生成预签名上传URL
	ctx := context.Background()
	expiresIn := 1 * time.Hour // URL有效期1小时
	uploadURL, err := h.getUploadURL(ctx, r, fileHash, expiresIn)
	if err != nil {
		sugar.Errorf("生成预签名上传URL失败: %v", err)
		http.Error(w, "Failed to generate upload URL", http.StatusInternalServerError)
		return
	}

	resp := &storage.UploadFileResponse{
		Exists:    false,
		UploadUrl: uploadURL,
		ExpiresIn: int64(expiresIn.Seconds()),
	}

	h.sendResponse(w, resp)
	sugar.Debugf("生成上传URL成功: file_hash=%s", fileHash)
}

func (h *UploadHandler) getUploadURL(ctx context.Context, r *http.Request, fileHash string, expiresIn time.Duration) (string, error) {
	if h.getPresignedUploadURLFor != nil {
		return h.getPresignedUploadURLFor(ctx, fileHash, expiresIn, resolveRustFSExternalEndpoint(r))
	}
	return h.getPresignedUploadURL(ctx, fileHash, expiresIn)
}

// HandleVerifyUpload 处理上传验证请求（第二阶段：验证上传的文件）
func (h *UploadHandler) HandleVerifyUpload(w http.ResponseWriter, r *http.Request) {
	sugar := logger.Sugar()

	// 解析请求
	var req storage.VerifyUploadRequest
	body, err := io.ReadAll(r.Body)
	if err != nil {
		sugar.Errorf("读取请求体失败: %v", err)
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}

	// 尝试解析为JSON
	if err := json.Unmarshal(body, &req); err != nil {
		// 如果JSON解析失败，尝试解析为Protobuf
		if err := proto.Unmarshal(body, &req); err != nil {
			sugar.Errorf("解析请求失败: %v", err)
			http.Error(w, "Failed to parse request", http.StatusBadRequest)
			return
		}
	}

	fileHash := req.FileHash
	if fileHash == "" {
		http.Error(w, "file_hash is required", http.StatusBadRequest)
		return
	}

	sugar.Debugf("收到上传验证请求: file_hash=%s", fileHash)

	// 检查文件是否存在于RustFS
	ctx := context.Background()
	exists, err := h.fileExistsInStorage(ctx, fileHash)
	if err != nil {
		sugar.Errorf("检查文件是否存在失败: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	if !exists {
		_ = h.deleteFileMetadata(fileHash)
		resp := &storage.VerifyUploadResponse{
			Success:      false,
			ErrorMessage: "File not found in storage",
		}
		h.sendResponse(w, resp)
		return
	}

	// 下载文件并验证哈希
	fileReader, err := h.downloadFile(ctx, fileHash)
	if err != nil {
		sugar.Errorf("下载文件失败: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	defer fileReader.Close()

	// 验证时流式读取对象，避免大文件整体进入内存。
	counter := &countingReader{reader: fileReader}
	valid, err := h.verifyFileHash(counter, fileHash)
	if err != nil {
		sugar.Errorf("验证文件哈希失败: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	if !valid {
		// 哈希不匹配，删除文件
		_ = h.deleteFile(ctx, fileHash)
		_ = h.deleteFileMetadata(fileHash)
		resp := &storage.VerifyUploadResponse{
			Success:      false,
			ErrorMessage: "File hash mismatch",
		}
		h.sendResponse(w, resp)
		sugar.Warnf("文件哈希不匹配，已删除: file_hash=%s", fileHash)
		return
	}

	// 保存文件元数据到数据库
	fileSize := counter.bytes
	storagePath := rustfs.GetStoragePath(fileHash)
	err = h.updateFileMetadata(fileHash, fileSize, storagePath)
	if err != nil {
		sugar.Errorf("保存文件元数据失败: %v", err)
		// 如果待验证记录不存在，回退为创建已验证记录，兼容旧流程。
		if fallbackErr := h.storeFileMetadata(fileHash, fileSize, storagePath); fallbackErr != nil {
			sugar.Errorf("回退创建文件元数据失败: %v", fallbackErr)
		}
	}

	resp := &storage.VerifyUploadResponse{
		Success: true,
	}

	h.sendResponse(w, resp)
	sugar.Infof("文件上传验证成功: file_hash=%s, file_size=%d", fileHash, fileSize)
}

// sendResponse 发送响应（支持JSON和Protobuf）
func (h *UploadHandler) sendResponse(w http.ResponseWriter, resp proto.Message) {
	// 默认使用JSON
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		logger.Sugar().Errorf("编码JSON响应失败: %v", err)
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
		return
	}
}
