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

	"storageService/internal/rustfs"

	"google.golang.org/protobuf/proto"
)

// downloadURLTTL 限制下载URL的有效期；客户端拿到URL后立即下载，不缓存URL。
const downloadURLTTL = 10 * time.Minute

// DownloadHandler 处理文件下载请求
//
// 只有持有者、头像，或当前能看到引用该文件且未撤回消息的用户才能拿到下载URL；
// 无权访问与文件不存在返回同样的响应，避免借下载接口探测文件是否存在。
type DownloadHandler struct {
	getFileMetadata         func(string) (*db.FileMetadata, error)
	canDownloadFile         func(string, int64) (bool, error)
	deleteFileMetadata      func(string) error
	fileExistsInStorage     func(context.Context, string) (bool, error)
	getPresignedDownloadURL func(context.Context, string, time.Duration) (string, error)
	getPresignedDownloadFor func(context.Context, string, time.Duration, string) (string, error)
}

// NewDownloadHandler 创建新的下载处理器
func NewDownloadHandler(rustfsClient *rustfs.RustFSClient) *DownloadHandler {
	return &DownloadHandler{
		getFileMetadata:         db.GetFileMetadata,
		canDownloadFile:         db.CanDownloadFile,
		deleteFileMetadata:      db.DeleteFileMetadata,
		fileExistsInStorage:     rustfsClient.FileExists,
		getPresignedDownloadURL: rustfsClient.GetPresignedDownloadURL,
		getPresignedDownloadFor: rustfsClient.GetPresignedDownloadURLForEndpoint,
	}
}

// HandleDownloadRequest 处理下载请求
func (h *DownloadHandler) HandleDownloadRequest(w http.ResponseWriter, r *http.Request) {
	sugar := logger.Sugar()

	userID, ok := authenticatedUserID(r)
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// 从Query参数获取file_hash
	fileHash := r.URL.Query().Get("file_hash")
	if fileHash == "" {
		// 尝试从请求体获取
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxStorageRequestBodyBytes))
		if err == nil && len(body) > 0 {
			var req storage.DownloadFileRequest
			// 尝试解析为JSON
			if err := json.Unmarshal(body, &req); err != nil {
				// 如果JSON解析失败，尝试解析为Protobuf
				if err := proto.Unmarshal(body, &req); err == nil {
					fileHash = req.FileHash
				}
			} else {
				fileHash = req.FileHash
			}
		}
	}

	fileHash, validHash := normalizeFileHash(fileHash)
	if !validHash {
		http.Error(w, "file_hash must be a SHA-512 hex digest", http.StatusBadRequest)
		return
	}

	sugar.Debugf("收到下载请求: user_id=%d, file_hash=%s", userID, fileHash)

	// 检查文件是否存在于数据库
	fileMetadata, err := h.getFileMetadata(fileHash)
	if err != nil {
		sugar.Errorf("查询文件元数据失败: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	if fileMetadata == nil {
		h.sendNotFound(w)
		sugar.Debugf("文件不存在: file_hash=%s", fileHash)
		return
	}

	allowed, err := h.canDownloadFile(fileHash, userID)
	if err != nil {
		sugar.Errorf("查询文件下载授权失败: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	if !allowed {
		h.sendNotFound(w)
		sugar.Debugf("用户无权下载文件: user_id=%d, file_hash=%s", userID, fileHash)
		return
	}
	if !fileMetadata.IsVerified {
		resp := &storage.DownloadFileResponse{
			Exists:       false,
			ErrorMessage: "File not verified",
		}
		h.sendResponse(w, resp)
		sugar.Debugf("文件尚未完成校验: file_hash=%s", fileHash)
		return
	}

	// 检查文件是否存在于RustFS
	ctx := r.Context()
	exists, err := h.fileExistsInStorage(ctx, fileHash)
	if err != nil {
		sugar.Errorf("检查文件是否存在失败: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	if !exists {
		// 文件在数据库中但不在存储中，删除数据库记录
		_ = h.deleteFileMetadata(fileHash)
		resp := &storage.DownloadFileResponse{
			Exists:       false,
			ErrorMessage: "File not found in storage",
		}
		h.sendResponse(w, resp)
		sugar.Warnf("文件在数据库中但不在存储中，已删除记录: file_hash=%s", fileHash)
		return
	}

	// 生成预签名下载URL
	expiresIn := downloadURLTTL
	downloadURL, err := h.getDownloadURL(ctx, r, fileHash, expiresIn)
	if err != nil {
		sugar.Errorf("生成预签名下载URL失败: %v", err)
		http.Error(w, "Failed to generate download URL", http.StatusInternalServerError)
		return
	}

	resp := &storage.DownloadFileResponse{
		Exists:      true,
		DownloadUrl: downloadURL,
		ExpiresIn:   int64(expiresIn.Seconds()),
		FileSize:    fileMetadata.FileSize,
	}

	h.sendResponse(w, resp)
	sugar.Debugf("生成下载URL成功: file_hash=%s", fileHash)
}

func (h *DownloadHandler) getDownloadURL(ctx context.Context, r *http.Request, fileHash string, expiresIn time.Duration) (string, error) {
	if h.getPresignedDownloadFor != nil {
		return h.getPresignedDownloadFor(ctx, fileHash, expiresIn, resolveRustFSExternalEndpoint(r))
	}
	return h.getPresignedDownloadURL(ctx, fileHash, expiresIn)
}

func (h *DownloadHandler) sendNotFound(w http.ResponseWriter) {
	h.sendResponse(w, &storage.DownloadFileResponse{Exists: false, ErrorMessage: "File not found"})
}

// sendResponse 发送响应（支持JSON和Protobuf）
func (h *DownloadHandler) sendResponse(w http.ResponseWriter, resp proto.Message) {
	// 默认使用JSON
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		logger.Sugar().Errorf("编码JSON响应失败: %v", err)
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
		return
	}
}
