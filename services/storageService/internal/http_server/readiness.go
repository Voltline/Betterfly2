package http_server

import (
	"Betterfly2/shared/logger"
	"context"
	"encoding/json"
	"net/http"
	"time"

	"storageService/internal/rustfs"
)

type readinessResponse struct {
	Ready        bool   `json:"ready"`
	ErrorMessage string `json:"error_message,omitempty"`
}

// ReadinessHandler 用于判断文件控制面的关键依赖是否可用。
type ReadinessHandler struct {
	pingDB             func(context.Context) error
	checkObjectStorage func(context.Context) error
}

// NewReadinessHandler 创建新的就绪检查处理器
func NewReadinessHandler(rustfsClient *rustfs.RustFSClient) *ReadinessHandler {
	return &ReadinessHandler{
		pingDB:             pingDatabase,
		checkObjectStorage: rustfsClient.HealthCheck,
	}
}

// HandleReady 处理 /ready 请求
func (h *ReadinessHandler) HandleReady(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	if err := h.pingDB(ctx); err != nil {
		h.sendNotReady(w, "database not ready: "+err.Error())
		return
	}
	if err := h.checkObjectStorage(ctx); err != nil {
		h.sendNotReady(w, "object storage not ready: "+err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(readinessResponse{Ready: true}); err != nil {
		logger.Sugar().Errorf("编码就绪检查响应失败: %v", err)
	}
}

func (h *ReadinessHandler) sendNotReady(w http.ResponseWriter, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusServiceUnavailable)
	if err := json.NewEncoder(w).Encode(readinessResponse{
		Ready:        false,
		ErrorMessage: message,
	}); err != nil {
		logger.Sugar().Errorf("编码就绪检查失败响应失败: %v", err)
	}
}
