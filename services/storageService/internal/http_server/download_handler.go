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

// DownloadHandler 处理文件下载请求
type DownloadHandler struct {
	rustfsClient *rustfs.RustFSClient
}

// NewDownloadHandler 创建新的下载处理器
func NewDownloadHandler(rustfsClient *rustfs.RustFSClient) *DownloadHandler {
	return &DownloadHandler{
		rustfsClient: rustfsClient,
	}
}

// HandleDownloadRequest 处理下载请求
func (h *DownloadHandler) HandleDownloadRequest(w http.ResponseWriter, r *http.Request) {
	sugar := logger.Sugar()

	// 从Query参数获取file_hash
	fileHash := r.URL.Query().Get("file_hash")
	if fileHash == "" {
		// 尝试从请求体获取
		body, err := io.ReadAll(r.Body)
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

	if fileHash == "" {
		http.Error(w, "file_hash is required", http.StatusBadRequest)
		return
	}

	sugar.Debugf("收到下载请求: file_hash=%s", fileHash)

	// 检查文件是否存在于数据库
	fileMetadata, err := db.GetFileMetadata(fileHash)
	if err != nil {
		sugar.Errorf("查询文件元数据失败: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	if fileMetadata == nil {
		// 文件不存在
		resp := &storage.DownloadFileResponse{
			Exists:       false,
			ErrorMessage: "File not found",
		}
		h.sendResponse(w, resp)
		sugar.Debugf("文件不存在: file_hash=%s", fileHash)
		return
	}

	// 检查文件是否存在于RustFS
	ctx := context.Background()
	exists, err := h.rustfsClient.FileExists(ctx, fileHash)
	if err != nil {
		sugar.Errorf("检查文件是否存在失败: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	if !exists {
		// 文件在数据库中但不在存储中，删除数据库记录
		_ = db.DeleteFileMetadata(fileHash)
		resp := &storage.DownloadFileResponse{
			Exists:       false,
			ErrorMessage: "File not found in storage",
		}
		h.sendResponse(w, resp)
		sugar.Warnf("文件在数据库中但不在存储中，已删除记录: file_hash=%s", fileHash)
		return
	}

	// 生成预签名下载URL
	expiresIn := 1 * time.Hour // URL有效期1小时
	downloadURL, err := h.rustfsClient.GetPresignedDownloadURL(ctx, fileHash, expiresIn)
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
