package http_server

import (
	"Betterfly2/proto/storage"
	"Betterfly2/shared/db"
	"Betterfly2/shared/logger"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"
	"storageService/internal/rustfs"
)

// uploadURLTTL 限制上传URL的有效期；URL只能写入请求者自己的暂存路径。
const uploadURLTTL = 15 * time.Minute

// UploadHandler 处理文件上传请求
//
// 客户端只拿到 staging/<user_id>/<hash> 的上传URL。校验时服务端读取暂存对象计算哈希，
// 通过后再在桶内复制到正式路径 <hash[:2]>/<hash>，因此任何人都无法直接改写正式对象，
// 哈希不匹配时也只会删除请求者自己的暂存对象。
//
// 秒传只对已持有该文件的用户生效：仅知道哈希的用户也会拿到上传URL，
// 上传内容校验通过后才获得该文件的访问授权，哈希本身不再是访问凭证。
type UploadHandler struct {
	fileExists                func(string) (bool, error)
	hasFileAccess             func(string, int64) (bool, error)
	grantFileAccess           func(string, int64) error
	upsertPendingFileMetadata func(string, int64, string) error
	storeFileMetadata         func(string, int64, string) error
	storeVerifiedFile         func(string, int64, string, int64) error
	objectExists              func(context.Context, string) (bool, error)
	openObject                func(context.Context, string) (io.ReadCloser, string, error)
	copyObject                func(context.Context, string, string, string) (string, error)
	deleteObject              func(context.Context, string) error
	getPresignedUploadURL     func(context.Context, string, time.Duration, string) (string, error)
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
		hasFileAccess:             db.HasFileAccess,
		grantFileAccess:           db.GrantFileAccess,
		upsertPendingFileMetadata: db.UpsertPendingFileMetadata,
		storeFileMetadata:         db.StoreFileMetadata,
		storeVerifiedFile:         db.StoreVerifiedFile,
		objectExists:              rustfsClient.ObjectExists,
		openObject:                rustfsClient.OpenObject,
		copyObject:                rustfsClient.CopyObject,
		deleteObject:              rustfsClient.DeleteObject,
		getPresignedUploadURL:     rustfsClient.GetPresignedUploadURLForKey,
		verifyFileHash:            rustfs.VerifyFileHash,
	}
}

// HandleUploadRequest 处理上传请求（第一阶段：获取上传URL）
func (h *UploadHandler) HandleUploadRequest(w http.ResponseWriter, r *http.Request) {
	sugar := logger.Sugar()

	userID, ok := authenticatedUserID(r)
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// 解析请求
	var req storage.UploadFileRequest
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxStorageRequestBodyBytes))
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

	fileHash, validHash := normalizeFileHash(req.FileHash)
	fileSize := req.FileSize

	if !validHash {
		http.Error(w, "file_hash must be a SHA-512 hex digest", http.StatusBadRequest)
		return
	}

	if fileSize <= 0 {
		http.Error(w, "file_size must be greater than 0", http.StatusBadRequest)
		return
	}

	sugar.Debugf("收到上传请求: user_id=%d, file_hash=%s, file_size=%d", userID, fileHash, fileSize)

	// 检查文件是否已存在
	exists, err := h.fileExists(fileHash)
	if err != nil {
		sugar.Errorf("查询文件是否存在失败: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	if exists {
		granted, err := h.hasFileAccess(fileHash, userID)
		if err != nil {
			sugar.Errorf("查询文件访问授权失败: %v", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
		if granted {
			h.sendResponse(w, &storage.UploadFileResponse{Exists: true})
			sugar.Debugf("文件已存在且用户已持有: user_id=%d, file_hash=%s", userID, fileHash)
			return
		}
		// 文件已存在但请求者未证明持有：照常签发暂存上传URL，校验通过后再授权。
	} else if err := h.upsertPendingFileMetadata(fileHash, fileSize, rustfs.GetStoragePath(fileHash)); err != nil {
		// 记录待验证状态，确保上传完成前后元数据状态可追踪。
		sugar.Errorf("记录待验证文件元数据失败: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// 生成指向请求者暂存路径的预签名上传URL
	uploadURL, err := h.getPresignedUploadURL(r.Context(), rustfs.GetStagingPath(userID, fileHash), uploadURLTTL, resolveRustFSExternalEndpoint(r))
	if err != nil {
		sugar.Errorf("生成预签名上传URL失败: %v", err)
		http.Error(w, "Failed to generate upload URL", http.StatusInternalServerError)
		return
	}

	resp := &storage.UploadFileResponse{
		Exists:    false,
		UploadUrl: uploadURL,
		ExpiresIn: int64(uploadURLTTL.Seconds()),
	}

	h.sendResponse(w, resp)
	sugar.Debugf("生成上传URL成功: user_id=%d, file_hash=%s", userID, fileHash)
}

// HandleVerifyUpload 处理上传验证请求（第二阶段：验证上传的文件）
func (h *UploadHandler) HandleVerifyUpload(w http.ResponseWriter, r *http.Request) {
	sugar := logger.Sugar()

	userID, ok := authenticatedUserID(r)
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// 解析请求
	var req storage.VerifyUploadRequest
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxStorageRequestBodyBytes))
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

	fileHash, validHash := normalizeFileHash(req.FileHash)
	if !validHash {
		http.Error(w, "file_hash must be a SHA-512 hex digest", http.StatusBadRequest)
		return
	}

	sugar.Debugf("收到上传验证请求: user_id=%d, file_hash=%s", userID, fileHash)

	ctx := r.Context()
	stagingPath := rustfs.GetStagingPath(userID, fileHash)
	storagePath := rustfs.GetStoragePath(fileHash)

	verified, err := h.fileExists(fileHash)
	if err != nil {
		sugar.Errorf("查询文件是否存在失败: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	if verified {
		h.verifyPossession(ctx, w, userID, stagingPath, fileHash)
		return
	}

	staged, err := h.objectExists(ctx, stagingPath)
	if err != nil {
		sugar.Errorf("检查暂存文件是否存在失败: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	if !staged {
		// 没有暂存对象时，正式对象可能来自元数据写入失败后的重试或升级前签发的旧URL；
		// 只校验不删除，避免误删其他用户已上传的内容。请求者没有证明持有，因此不授权。
		fileSize, result := h.checkCanonicalObject(ctx, storagePath, fileHash)
		if result != nil {
			h.writeVerifyFailure(w, result)
			return
		}
		if err := h.storeFileMetadata(fileHash, fileSize, storagePath); err != nil {
			h.writeMetadataFailure(w, fileHash, err)
			return
		}
		h.sendResponse(w, &storage.VerifyUploadResponse{Success: true})
		sugar.Infof("正式路径文件校验成功: user_id=%d, file_hash=%s, file_size=%d", userID, fileHash, fileSize)
		return
	}

	fileSize, result := h.promoteStagedObject(ctx, stagingPath, storagePath, fileHash)
	if result != nil {
		h.writeVerifyFailure(w, result)
		return
	}
	// 元数据和授权写入成功后才删除暂存对象，失败重试时上传者仍能凭暂存对象拿到授权。
	if err := h.storeVerifiedFile(fileHash, fileSize, storagePath, userID); err != nil {
		h.writeMetadataFailure(w, fileHash, err)
		return
	}
	h.removeStagingObject(ctx, stagingPath)

	h.sendResponse(w, &storage.VerifyUploadResponse{Success: true})
	sugar.Infof("文件上传验证成功: user_id=%d, file_hash=%s, file_size=%d", userID, fileHash, fileSize)
}

// verifyPossession 处理已校验文件的再次上传：正式对象不再改写，
// 请求者的暂存对象哈希匹配即证明持有并获得授权，随后清理暂存对象。
func (h *UploadHandler) verifyPossession(ctx context.Context, w http.ResponseWriter, userID int64, stagingPath, fileHash string) {
	sugar := logger.Sugar()

	granted, err := h.hasFileAccess(fileHash, userID)
	if err != nil {
		sugar.Errorf("查询文件访问授权失败: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	if granted {
		h.removeStagingObject(ctx, stagingPath)
		h.sendResponse(w, &storage.VerifyUploadResponse{Success: true})
		return
	}

	staged, err := h.objectExists(ctx, stagingPath)
	if err != nil {
		sugar.Errorf("检查暂存文件是否存在失败: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	if !staged {
		h.writeVerifyFailure(w, &verifyFailure{message: "File not found in storage"})
		return
	}

	_, _, valid, err := h.hashObject(ctx, stagingPath, fileHash)
	if err != nil {
		sugar.Errorf("校验暂存文件失败: %v", err)
		h.writeVerifyFailure(w, errVerifyInternal)
		return
	}
	if !valid {
		h.removeStagingObject(ctx, stagingPath)
		sugar.Warnf("暂存文件哈希不匹配，已删除暂存对象: path=%s", stagingPath)
		h.writeVerifyFailure(w, &verifyFailure{message: "File hash mismatch"})
		return
	}
	if err := h.grantFileAccess(fileHash, userID); err != nil {
		sugar.Errorw("持有证明校验成功但授权保存失败", "file_hash", fileHash, "user_id", userID, "error", err)
		h.writeVerifyFailure(w, errVerifyInternal)
		return
	}
	h.removeStagingObject(ctx, stagingPath)
	h.sendResponse(w, &storage.VerifyUploadResponse{Success: true})
	sugar.Infof("文件持有证明校验成功: user_id=%d, file_hash=%s", userID, fileHash)
}

// verifyFailure 描述一次未通过的校验；status 为 0 表示业务失败（HTTP 200 + success=false）。
type verifyFailure struct {
	status  int
	message string
}

var errVerifyInternal = &verifyFailure{status: http.StatusInternalServerError, message: "Internal server error"}

// promoteStagedObject 校验暂存对象并复制到正式路径，返回文件大小；暂存对象由调用方在元数据写入后删除。
func (h *UploadHandler) promoteStagedObject(ctx context.Context, stagingPath, storagePath, fileHash string) (int64, *verifyFailure) {
	sugar := logger.Sugar()

	size, etag, valid, err := h.hashObject(ctx, stagingPath, fileHash)
	if err != nil {
		sugar.Errorf("校验暂存文件失败: %v", err)
		return 0, errVerifyInternal
	}
	if !valid {
		h.removeStagingObject(ctx, stagingPath)
		sugar.Warnf("暂存文件哈希不匹配，已删除暂存对象: path=%s", stagingPath)
		return 0, &verifyFailure{message: "File hash mismatch"}
	}

	// 条件复制保证复制的仍是刚才校验过的内容，防止校验期间暂存对象被再次改写。
	copiedETag, err := h.copyObject(ctx, stagingPath, storagePath, etag)
	if err != nil {
		sugar.Errorf("复制暂存文件到正式路径失败: %v", err)
		return 0, errVerifyInternal
	}
	if etag != "" && copiedETag != "" && normalizeETag(etag) != normalizeETag(copiedETag) {
		sugar.Warnf("暂存文件在校验期间被改写: path=%s", stagingPath)
		return 0, &verifyFailure{message: "File changed during verification"}
	}
	return size, nil
}

// checkCanonicalObject 校验已在正式路径中的对象，不匹配时只报告失败。
func (h *UploadHandler) checkCanonicalObject(ctx context.Context, storagePath, fileHash string) (int64, *verifyFailure) {
	sugar := logger.Sugar()

	exists, err := h.objectExists(ctx, storagePath)
	if err != nil {
		sugar.Errorf("检查文件是否存在失败: %v", err)
		return 0, errVerifyInternal
	}
	if !exists {
		return 0, &verifyFailure{message: "File not found in storage"}
	}

	size, _, valid, err := h.hashObject(ctx, storagePath, fileHash)
	if err != nil {
		sugar.Errorf("校验文件失败: %v", err)
		return 0, errVerifyInternal
	}
	if !valid {
		sugar.Warnf("正式路径文件哈希不匹配: file_hash=%s", fileHash)
		return 0, &verifyFailure{message: "File hash mismatch"}
	}
	return size, nil
}

// hashObject 流式读取对象计算哈希，避免大文件整体进入内存。
func (h *UploadHandler) hashObject(ctx context.Context, key, fileHash string) (int64, string, bool, error) {
	reader, etag, err := h.openObject(ctx, key)
	if err != nil {
		return 0, "", false, err
	}
	defer reader.Close()

	counter := &countingReader{reader: reader}
	valid, err := h.verifyFileHash(counter, fileHash)
	if err != nil {
		return 0, "", false, err
	}
	return counter.bytes, etag, valid, nil
}

func (h *UploadHandler) removeStagingObject(ctx context.Context, stagingPath string) {
	if err := h.deleteObject(ctx, stagingPath); err != nil {
		logger.Sugar().Warnf("删除暂存对象失败: path=%s err=%v", stagingPath, err)
	}
}

func (h *UploadHandler) writeVerifyFailure(w http.ResponseWriter, failure *verifyFailure) {
	if failure.status != 0 {
		http.Error(w, failure.message, failure.status)
		return
	}
	h.sendResponse(w, &storage.VerifyUploadResponse{Success: false, ErrorMessage: failure.message})
}

func (h *UploadHandler) writeMetadataFailure(w http.ResponseWriter, fileHash string, err error) {
	logger.Sugar().Errorw("上传对象校验成功但元数据保存失败", "file_hash", fileHash, "error", err)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusInternalServerError)
	h.sendResponse(w, &storage.VerifyUploadResponse{
		Success:      false,
		ErrorMessage: "Failed to save verified file metadata",
	})
}

func normalizeETag(etag string) string {
	return strings.Trim(etag, `"`)
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
