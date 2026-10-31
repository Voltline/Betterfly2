// portal 是网关根路径的导航页，只托管内嵌的index.html。
package main

import (
	_ "embed"
	"log"
	"net/http"
	"os"
	"time"
)

//go:embed index.html
var page []byte

func handler() http.Handler {
	mux := http.NewServeMux()
	serve := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(page)
	}
	mux.HandleFunc("GET /{$}", serve)
	mux.HandleFunc("GET /index.html", serve)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	return mux
}

func main() {
	addr := ":" + envOr("PORT", "8080")
	// 镜像里没有shell，容器健康检查直接调用自身
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		client := http.Client{Timeout: 3 * time.Second}
		response, err := client.Get("http://127.0.0.1" + addr + "/healthz")
		if err != nil || response.StatusCode != http.StatusOK {
			os.Exit(1)
		}
		return
	}
	server := &http.Server{Addr: addr, Handler: handler(), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	log.Printf("portal listening on %s", addr)
	log.Fatal(server.ListenAndServe())
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
