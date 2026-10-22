package httpserver

import (
	pb "Betterfly2/proto/server_rpc/auth"
	sharedutils "Betterfly2/shared/utils"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

var errUnauthorized = errors.New("unauthorized")
var errUnavailable = errors.New("auth unavailable")
var errRateLimited = errors.New("rate limited")

type Auth interface {
	Check(context.Context, int64, string) (int64, error)
	Login(context.Context, string, string) (int64, string, error)
}
type RPCAuth struct{ Client pb.AuthServiceClient }

func (a RPCAuth) Check(ctx context.Context, user int64, jwt string) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	r, err := a.Client.CheckJwt(ctx, &pb.CheckJwtReq{UserId: user, Jwt: jwt})
	if err != nil || r == nil {
		return 0, errUnavailable
	}
	if r.GetResult() == pb.AuthResult_SERVICE_ERROR {
		return 0, errUnavailable
	}
	if r.GetResult() != pb.AuthResult_OK || r.GetUserId() != user {
		return 0, errUnauthorized
	}
	return r.GetUserId(), nil
}
func (a RPCAuth) Login(ctx context.Context, account, password string) (int64, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	r, err := a.Client.Login(ctx, &pb.LoginReq{Account: account, Password: password})
	if err != nil || r == nil || r.GetResult() == pb.AuthResult_SERVICE_ERROR {
		return 0, "", errUnavailable
	}
	if r.GetResult() == pb.AuthResult_RATE_LIMITED {
		return 0, "", errRateLimited
	}
	if r.GetResult() != pb.AuthResult_OK || r.GetUserId() <= 0 || r.GetJwt() == "" || len(r.GetJwt()) > 3500 {
		return 0, "", errUnauthorized
	}
	return r.GetUserId(), r.GetJwt(), nil
}

func Origin(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Hostname() == "" || u.Scheme != "http" && u.Scheme != "https" || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" {
		return "", errors.New("MOMENTS_PUBLIC_URL must be an HTTP(S) origin")
	}
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if port != "" {
		n, e := strconv.Atoi(port)
		if e != nil || n < 1 || n > 65535 {
			return "", errors.New("invalid origin port")
		}
	}
	if u.Scheme == "https" && port == "443" || u.Scheme == "http" && port == "80" {
		port = ""
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port != "" {
		host += ":" + port
	}
	return u.Scheme + "://" + host, nil
}
func (s *Server) sameOrigin(r *http.Request) bool {
	value, err := Origin(r.Header.Get("Origin"))
	return err == nil && value == s.origin
}
func (s *Server) cookie(w http.ResponseWriter, name, value string, age int) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/moments/", HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteStrictMode, MaxAge: age})
}
func (s *Server) clearCookies(w http.ResponseWriter) {
	for _, name := range []string{"bf_moments_jwt", "bf_moments_user", "bf_moments_csrf"} {
		s.cookie(w, name, "", -1)
	}
}
func csrfValue(r *http.Request) string {
	c, err := r.Cookie("bf_moments_csrf")
	if err != nil {
		return ""
	}
	return c.Value
}
func (s *Server) authenticate(r *http.Request) (int64, error) {
	header := r.Header.Get("Authorization")
	jwtCookie, cookieErr := r.Cookie("bf_moments_jwt")
	var user int64
	var jwt string
	if header != "" {
		parts := strings.Fields(header)
		if len(parts) != 2 || parts[0] != "Bearer" {
			return 0, errUnauthorized
		}
		var err error
		user, err = strconv.ParseInt(r.Header.Get("X-User-ID"), 10, 64)
		if err != nil || user <= 0 {
			return 0, errUnauthorized
		}
		jwt = parts[1]
		if cookieErr == nil && jwtCookie.Value != "" && jwtCookie.Value != jwt {
			return 0, errUnauthorized
		}
	} else {
		if cookieErr != nil || jwtCookie.Value == "" {
			return 0, errUnauthorized
		}
		c, err := r.Cookie("bf_moments_user")
		if err != nil {
			return 0, errUnauthorized
		}
		user, err = strconv.ParseInt(c.Value, 10, 64)
		if err != nil || user <= 0 {
			return 0, errUnauthorized
		}
		jwt = jwtCookie.Value
		if r.Method != "GET" && r.Method != "HEAD" {
			csrf := csrfValue(r)
			if !s.sameOrigin(r) || len(csrf) != 48 || subtle.ConstantTimeCompare([]byte(csrf), []byte(r.Header.Get("X-CSRF-Token"))) != 1 {
				return 0, errUnauthorized
			}
		}
	}
	if len(jwt) > 3500 {
		return 0, errUnauthorized
	}
	return s.auth.Check(r.Context(), user, jwt)
}
func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	if !s.sameOrigin(r) {
		s.fail(w, r, errUnauthorized)
		return
	}
	if r.Method == "DELETE" {
		csrf := csrfValue(r)
		if len(csrf) != 48 || subtle.ConstantTimeCompare([]byte(csrf), []byte(r.Header.Get("X-CSRF-Token"))) != 1 {
			s.fail(w, r, errUnauthorized)
			return
		}
		s.clearCookies(w)
		write(w, 200, map[string]bool{"success": true})
		return
	}
	var input struct {
		Account  string `json:"account"`
		Password string `json:"password"`
	}
	if err := decode(w, r, &input); err != nil {
		s.fail(w, r, err)
		return
	}
	var user int64
	var jwt string
	var err error
	if r.Header.Get("Authorization") != "" {
		if input.Account != "" || input.Password != "" {
			s.fail(w, r, errUnauthorized)
			return
		}
		user, err = s.authenticate(r)
		parts := strings.Fields(r.Header.Get("Authorization"))
		if len(parts) == 2 {
			jwt = parts[1]
		}
	} else {
		// 账号级失败限流在auth里；这里只按真实来源IP限制尝试频率，确定不了IP时不限。
		if ip := sharedutils.ClientIP(r, s.trustedProxies); ip != "" && !s.logins.allow(ip) {
			writeError(w, 429, "RATE_LIMITED", "请稍后再尝试登录")
			return
		}
		if input.Account == "" || input.Password == "" || len(input.Account) > 200 || len(input.Password) > 72 {
			s.fail(w, r, errUnauthorized)
			return
		}
		user, jwt, err = s.auth.Login(r.Context(), input.Account, input.Password)
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var data [24]byte
	if _, err := rand.Read(data[:]); err != nil {
		s.fail(w, r, errUnavailable)
		return
	}
	csrf := hex.EncodeToString(data[:])
	s.cookie(w, "bf_moments_jwt", jwt, 0)
	s.cookie(w, "bf_moments_user", strconv.FormatInt(user, 10), 0)
	s.cookie(w, "bf_moments_csrf", csrf, 0)
	write(w, 200, map[string]string{"user_id": strconv.FormatInt(user, 10), "csrf_token": csrf})
}

type attempts struct {
	time  time.Time
	count int
}
type loginLimiter struct {
	mu      sync.Mutex
	entries map[string]attempts
}

func (l *loginLimiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	timestamp := time.Now()
	if l.entries == nil {
		l.entries = map[string]attempts{}
	}
	a := l.entries[ip]
	if timestamp.Sub(a.time) >= time.Minute {
		a = attempts{time: timestamp}
	}
	if _, ok := l.entries[ip]; !ok && len(l.entries) >= 4096 {
		for key, value := range l.entries {
			if timestamp.Sub(value.time) >= time.Minute {
				delete(l.entries, key)
			}
		}
		// 表满时放行但不记录：内存有界，大量来源也不能把所有新IP挡在门外。
		if len(l.entries) >= 4096 {
			return true
		}
	}
	a.count++
	l.entries[ip] = a
	return a.count <= 10
}
