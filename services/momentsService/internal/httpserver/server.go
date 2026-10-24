package httpserver

import (
	"Betterfly2/shared/logger"
	sharedutils "Betterfly2/shared/utils"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"momentsService/internal/moments"
	"momentsService/web"
	"net/http"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const base = "/moments/api/v1"

type Server struct {
	service     *moments.Service
	auth        Auth
	origin      string
	secure      bool
	logins      loginLimiter
	verifySlots chan struct{}
	// 只信任来自这些地址（ingress）的X-Forwarded-For/X-Real-IP。
	trustedProxies []netip.Prefix
}

func New(service *moments.Service, auth Auth, origin string) *Server {
	proxies := os.Getenv("MOMENTS_TRUSTED_PROXY_CIDRS")
	if proxies == "" {
		proxies = sharedutils.DefaultTrustedProxyCIDRs
	}
	return &Server{service: service, auth: auth, origin: origin, secure: strings.HasPrefix(origin, "https://"), verifySlots: make(chan struct{}, 2), trustedProxies: sharedutils.ParseTrustedProxies(proxies)}
}
func write(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func writeError(w http.ResponseWriter, status int, code, message string) {
	write(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

// unavailableRetryAfter 是503时建议客户端等待的秒数；auth或数据库瞬断通常几秒内恢复。
const unavailableRetryAfter = "2"

func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	status, code, message := failure(err)
	if status == 503 {
		logger.Sugar().Warnw("Moments request temporarily unavailable", "method", r.Method, "route", r.Pattern, "error", err)
		w.Header().Set("Retry-After", unavailableRetryAfter)
	} else {
		logger.Sugar().Infow("Moments request rejected", "method", r.Method, "route", r.Pattern, "status", status, "code", code, "error", err)
	}
	writeError(w, status, code, message)
}
func failure(err error) (int, string, string) {
	switch {
	case errors.Is(err, moments.ErrInvalid):
		return 400, "INVALID_REQUEST", "参数或图片格式不正确"
	case errors.Is(err, moments.ErrNotFound):
		return 404, "NOT_FOUND", "内容不存在或不可访问"
	case errors.Is(err, moments.ErrConflict):
		return 409, "CONFLICT", "幂等请求内容不同，或图片已被使用"
	case errors.Is(err, moments.ErrGone):
		return 410, "DELETED", "内容已删除，请使用新的客户端 ID"
	case errors.Is(err, errRateLimited):
		return 429, "RATE_LIMITED", "请稍后再尝试登录"
	case errors.Is(err, moments.ErrQuota):
		return 429, "TOO_MANY_UPLOADS", "未发布的图片过多，请先发布或稍后再试"
	case errors.Is(err, errUnauthorized):
		return 401, "UNAUTHORIZED", "请重新登录或检查请求凭据"
	case isTooLarge(err):
		return 413, "TOO_LARGE", "请求体过大"
	default:
		return 503, "UNAVAILABLE", "服务暂时不可用，请保留原请求 ID 重试"
	}
}
func isTooLarge(err error) bool { var large *http.MaxBytesError; return errors.As(err, &large) }
func decode(w http.ResponseWriter, r *http.Request, target any) error {
	kind, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || kind != "application/json" {
		return moments.ErrInvalid
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		return err
	}
	if !utf8.Valid(data) {
		return moments.ErrInvalid
	}
	d := json.NewDecoder(strings.NewReader(string(data)))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return moments.ErrInvalid
	}
	if d.Decode(&struct{}{}) != io.EOF {
		return moments.ErrInvalid
	}
	return nil
}
func (s *Server) protected(fn func(http.ResponseWriter, *http.Request, int64)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, err := s.authenticate(r)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		fn(w, r, user)
	}
}
func pathID(r *http.Request) (int64, error) { return moments.ID(r.PathValue("id")) }
func limit(r *http.Request) (int, error) {
	if r.URL.Query().Get("limit") == "" {
		return 20, nil
	}
	n, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || n <= 0 || n > 50 {
		return 0, moments.ErrInvalid
	}
	return n, nil
}
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) { write(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("GET /ready", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		if err := s.service.Health(ctx); err != nil {
			s.fail(w, r, err)
			return
		}
		write(w, 200, map[string]string{"status": "ready"})
	})
	if os.Getenv("METRICS_ENABLED") != "false" {
		mux.Handle("GET /metrics", promhttp.Handler())
	}
	mux.HandleFunc("POST "+base+"/session", s.session)
	mux.HandleFunc("DELETE "+base+"/session", s.session)
	mux.HandleFunc("GET "+base+"/me", s.protected(func(w http.ResponseWriter, r *http.Request, user int64) {
		me, err := s.service.Me(r.Context(), user)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		write(w, 200, map[string]any{"user": me, "csrf_token": csrfValue(r)})
	}))
	mux.HandleFunc("GET "+base+"/feed", s.protected(s.feed))
	mux.HandleFunc("GET "+base+"/users/{id}/posts", s.protected(s.feed))
	mux.HandleFunc("GET "+base+"/users/{id}/avatar", s.protected(func(w http.ResponseWriter, r *http.Request, user int64) {
		n, err := pathID(r)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		item, err := s.service.Avatar(r.Context(), n)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		write(w, 200, item)
	}))
	mux.HandleFunc("POST "+base+"/posts", s.protected(func(w http.ResponseWriter, r *http.Request, user int64) {
		var input moments.CreatePost
		if err := decode(w, r, &input); err != nil {
			s.fail(w, r, err)
			return
		}
		post, err := s.service.Create(r.Context(), user, input)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		write(w, 200, post)
	}))
	mux.HandleFunc("GET "+base+"/posts/{id}", s.protected(func(w http.ResponseWriter, r *http.Request, user int64) {
		n, err := pathID(r)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		post, err := s.service.Get(r.Context(), user, n)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		write(w, 200, post)
	}))
	mux.HandleFunc("DELETE "+base+"/posts/{id}", s.protected(func(w http.ResponseWriter, r *http.Request, user int64) {
		n, err := pathID(r)
		if err == nil {
			err = s.service.Delete(r.Context(), user, n)
		}
		if err != nil {
			s.fail(w, r, err)
			return
		}
		write(w, 200, map[string]bool{"success": true})
	}))
	mux.HandleFunc("PUT "+base+"/posts/{id}/like", s.protected(s.like))
	mux.HandleFunc("DELETE "+base+"/posts/{id}/like", s.protected(s.like))
	mux.HandleFunc("GET "+base+"/posts/{id}/likes", s.protected(s.likes))
	mux.HandleFunc("GET "+base+"/posts/{id}/comments", s.protected(s.comments))
	mux.HandleFunc("POST "+base+"/posts/{id}/comments", s.protected(func(w http.ResponseWriter, r *http.Request, user int64) {
		var input moments.CreateComment
		n, err := pathID(r)
		if err == nil {
			err = decode(w, r, &input)
		}
		if err != nil {
			s.fail(w, r, err)
			return
		}
		item, err := s.service.AddComment(r.Context(), user, n, input)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		write(w, 200, item)
	}))
	mux.HandleFunc("DELETE "+base+"/comments/{id}", s.protected(func(w http.ResponseWriter, r *http.Request, user int64) {
		n, err := pathID(r)
		if err == nil {
			err = s.service.DeleteComment(r.Context(), user, n)
		}
		if err != nil {
			s.fail(w, r, err)
			return
		}
		write(w, 200, map[string]bool{"success": true})
	}))
	mux.HandleFunc("POST "+base+"/assets", s.protected(func(w http.ResponseWriter, r *http.Request, user int64) {
		var input moments.CreateAsset
		if err := decode(w, r, &input); err != nil {
			s.fail(w, r, err)
			return
		}
		item, err := s.service.PrepareAsset(r.Context(), user, input)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		write(w, 200, item)
	}))
	mux.HandleFunc("POST "+base+"/assets/{id}/verify", s.protected(func(w http.ResponseWriter, r *http.Request, user int64) {
		n, err := pathID(r)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		select {
		case s.verifySlots <- struct{}{}:
			defer func() { <-s.verifySlots }()
		case <-r.Context().Done():
			s.fail(w, r, r.Context().Err())
			return
		}
		item, err := s.service.VerifyAsset(r.Context(), user, n)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		write(w, 200, item)
	}))
	mux.HandleFunc("GET "+base+"/assets/{id}/download", s.protected(func(w http.ResponseWriter, r *http.Request, user int64) {
		n, err := pathID(r)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		item, err := s.service.AssetDownload(r.Context(), user, n)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		write(w, 200, item)
	}))
	mux.Handle("GET /moments/", http.StripPrefix("/moments/", http.FileServer(http.FS(web.Files))))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' https: http: blob:; connect-src 'self' https: http:; frame-ancestors 'self'; base-uri 'self'; form-action 'self'")
		ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
		defer cancel()
		mux.ServeHTTP(w, r.WithContext(ctx))
	})
}
func (s *Server) feed(w http.ResponseWriter, r *http.Request, user int64) {
	var author int64
	var err error
	if r.PathValue("id") != "" {
		author, err = pathID(r)
	}
	l, e := limit(r)
	if err == nil {
		err = e
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	page, err := s.service.Feed(r.Context(), user, author, r.URL.Query().Get("cursor"), l)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, page)
}
func (s *Server) like(w http.ResponseWriter, r *http.Request, user int64) {
	n, err := pathID(r)
	if err == nil {
		err = s.service.SetLike(r.Context(), user, n, r.Method == "PUT")
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, map[string]bool{"liked_by_me": r.Method == "PUT"})
}
func (s *Server) likes(w http.ResponseWriter, r *http.Request, user int64) {
	n, err := pathID(r)
	l, e := limit(r)
	if err == nil {
		err = e
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	page, err := s.service.Likes(r.Context(), user, n, r.URL.Query().Get("cursor"), l)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, page)
}
func (s *Server) comments(w http.ResponseWriter, r *http.Request, user int64) {
	n, err := pathID(r)
	l, e := limit(r)
	if err == nil {
		err = e
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	page, err := s.service.Comments(r.Context(), user, n, r.URL.Query().Get("cursor"), l)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, page)
}
