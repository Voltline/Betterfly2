package http_server

import (
	"Betterfly2/shared/db"
	"Betterfly2/shared/logger"
	"context"
	"net/http"
	"os"
	"time"

	"storageService/internal/rustfs"
)

// HTTPServer HTTP服务器
type HTTPServer struct {
	server          *http.Server
	rustfsClient    *rustfs.RustFSClient
	uploadHandler   *UploadHandler
	downloadHandler *DownloadHandler
}

// NewHTTPServer 创建新的HTTP服务器
func NewHTTPServer() (*HTTPServer, error) {
	sugar := logger.Sugar()

	// 初始化数据库连接并自动迁移表（确保FileMetadata表存在）
	_ = db.DB(&db.User{}, &db.Friend{}, &db.Message{}, &db.FileMetadata{})

	// 初始化RustFS客户端
	rustfsClient, err := rustfs.NewRustFSClient()
	if err != nil {
		return nil, err
	}

	// 创建处理器
	uploadHandler := NewUploadHandler(rustfsClient)
	downloadHandler := NewDownloadHandler(rustfsClient)

	// 创建HTTP服务器
	port := os.Getenv("HTTP_PORT")
	if port == "" {
		port = "8080"
	}

	mux := http.NewServeMux()

	// 注册路由
	mux.HandleFunc("/storage_service/upload", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		uploadHandler.HandleUploadRequest(w, r)
	})

	mux.HandleFunc("/storage_service/upload/verify", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		uploadHandler.HandleVerifyUpload(w, r)
	})

	mux.HandleFunc("/storage_service/download", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		downloadHandler.HandleDownloadRequest(w, r)
	})

	// 健康检查端点
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	server := &http.Server{
		Addr:         ":" + port,
		Handler:      JWTAuthMiddleware(mux), // 应用JWT验证中间件
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	sugar.Infof("HTTP服务器初始化完成，端口: %s", port)

	return &HTTPServer{
		server:          server,
		rustfsClient:    rustfsClient,
		uploadHandler:   uploadHandler,
		downloadHandler: downloadHandler,
	}, nil
}

// Start 启动HTTP服务器
func (s *HTTPServer) Start() error {
	sugar := logger.Sugar()
	sugar.Infof("启动HTTP服务器，监听端口: %s", s.server.Addr)
	return s.server.ListenAndServe()
}

// Shutdown 优雅关闭HTTP服务器
func (s *HTTPServer) Shutdown(ctx context.Context) error {
	sugar := logger.Sugar()
	sugar.Info("正在关闭HTTP服务器...")
	return s.server.Shutdown(ctx)
}
