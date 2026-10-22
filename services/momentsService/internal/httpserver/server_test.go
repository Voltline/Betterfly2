package httpserver

import (
	pb "Betterfly2/proto/server_rpc/auth"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
)

type fakeAuth struct {
	err            error
	checks, logins int
	user           int64
	jwt            string
}

func (a *fakeAuth) Check(_ context.Context, user int64, jwt string) (int64, error) {
	a.checks++
	a.user = user
	a.jwt = jwt
	return user, a.err
}
func (a *fakeAuth) Login(context.Context, string, string) (int64, string, error) {
	a.logins++
	return 7, "jwt", a.err
}
func req(server http.Handler, method, path, body string, headers map[string]string, cookies []*http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	server.ServeHTTP(w, r)
	return w
}
func TestSessionCookieAndCSRFBoundaries(t *testing.T) {
	a := &fakeAuth{}
	s := New(nil, a, "https://bf.test")
	handler := s.Routes()
	for _, origin := range []string{"", "https://bf.test.evil", "https://bf.test:444", "http://bf.test"} {
		w := req(handler, "POST", base+"/session", `{"account":"user","password":"secret"}`, map[string]string{"Origin": origin, "Content-Type": "application/json"}, nil)
		if w.Code != 401 || a.logins != 0 {
			t.Fatal(origin, w.Code, a.logins)
		}
	}
	w := req(handler, "POST", base+"/session", `{"account":"user","password":"secret"}`, map[string]string{"Origin": "https://bf.test:443", "Content-Type": "application/json"}, nil)
	if w.Code != 200 || a.logins != 1 {
		t.Fatal(w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 3 {
		t.Fatal(cookies)
	}
	for _, c := range cookies {
		if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode || c.Path != "/moments/" {
			t.Fatal(c)
		}
	}
	var result map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	csrf := result["csrf_token"]
	for _, headers := range []map[string]string{{"Origin": "https://bf.test"}, {"Origin": "https://evil.test", "X-CSRF-Token": csrf}, {"Origin": "https://bf.test", "X-CSRF-Token": "bad"}} {
		w := req(handler, "DELETE", base+"/session", "", headers, cookies)
		if w.Code != 401 || len(w.Result().Cookies()) != 0 {
			t.Fatal(w.Code)
		}
	}
	// Expired JWT does not prevent clearing a browser's own CSRF-protected cookies.
	a.err = errUnauthorized
	w = req(handler, "DELETE", base+"/session", "", map[string]string{"Origin": "https://bf.test", "X-CSRF-Token": csrf}, cookies)
	if w.Code != 200 || a.checks != 0 {
		t.Fatal(w.Code, a.checks)
	}
	for _, c := range w.Result().Cookies() {
		if c.MaxAge != -1 {
			t.Fatal(c)
		}
	}
}
func TestBearerVerifiedIdentityNoCookieFallback(t *testing.T) {
	a := &fakeAuth{}
	s := New(nil, a, "http://localhost:8087")
	for _, headers := range []map[string]string{{}, {"Authorization": "Basic x"}, {"Authorization": "Bearer jwt", "X-User-ID": "0"}, {"Authorization": "Bearer jwt", "X-User-ID": "evil"}} {
		r := httptest.NewRequest("GET", base+"/me", nil)
		for k, v := range headers {
			r.Header.Set(k, v)
		}
		if _, err := s.authenticate(r); !errors.Is(err, errUnauthorized) {
			t.Fatal(headers, err)
		}
	}
	r := httptest.NewRequest("GET", base+"/me", nil)
	r.Header.Set("Authorization", "Bearer jwt")
	r.Header.Set("X-User-ID", "7")
	r.AddCookie(&http.Cookie{Name: "bf_moments_jwt", Value: "other"})
	if _, err := s.authenticate(r); !errors.Is(err, errUnauthorized) {
		t.Fatal(err)
	}
	r = httptest.NewRequest("GET", base+"/me", nil)
	r.Header.Set("Authorization", "Bearer jwt")
	r.Header.Set("X-User-ID", "7")
	user, err := s.authenticate(r)
	if err != nil || user != 7 || a.user != 7 || a.jwt != "jwt" {
		t.Fatal(user, err, a)
	}
}
func TestCookieMutationMustHaveOriginAndCSRFBeforeAuth(t *testing.T) {
	a := &fakeAuth{}
	s := New(nil, a, "https://bf.test")
	r := httptest.NewRequest("POST", base+"/posts", nil)
	for _, c := range []*http.Cookie{{Name: "bf_moments_jwt", Value: "jwt"}, {Name: "bf_moments_user", Value: "7"}, {Name: "bf_moments_csrf", Value: strings.Repeat("a", 48)}} {
		r.AddCookie(c)
	}
	if _, err := s.authenticate(r); !errors.Is(err, errUnauthorized) || a.checks != 0 {
		t.Fatal(err, a.checks)
	}
	r.Header.Set("Origin", "https://bf.test")
	r.Header.Set("X-CSRF-Token", strings.Repeat("a", 48))
	if _, err := s.authenticate(r); err != nil || a.checks != 1 {
		t.Fatal(err, a.checks)
	}
}
func TestInvalidAndOversizedJSONRejectedBeforeBusiness(t *testing.T) {
	handler := New(nil, &fakeAuth{}, "http://localhost:8087").Routes()
	for _, test := range []struct {
		body, kind string
		status     int
	}{{`{"content":"x","author_user_id":2}`, "application/json", 400}, {"\xff", "application/json", 400}, {`{} {}`, "application/json", 400}, {`{}`, "text/plain", 400}, {strings.Repeat("a", 65537), "application/json", 413}} {
		w := req(handler, "POST", base+"/posts", test.body, map[string]string{"Authorization": "Bearer jwt", "X-User-ID": "7", "Content-Type": test.kind}, nil)
		if w.Code != test.status {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}
func TestLoginFailureOrAuthOutageNeverSetsCookies(t *testing.T) {
	for err, want := range map[error]int{errUnauthorized: 401, errUnavailable: 503, errRateLimited: 429} {
		a := &fakeAuth{err: err}
		handler := New(nil, a, "https://bf.test").Routes()
		w := req(handler, "POST", base+"/session", `{"account":"user","password":"pass"}`, map[string]string{"Origin": "https://bf.test", "Content-Type": "application/json"}, nil)
		if w.Code != want || len(w.Result().Cookies()) != 0 {
			t.Fatal(w.Code, w.Result().Cookies())
		}
	}
}
func TestEmbeddedBearerSessionAndMutuallyExclusivePasswordMode(t *testing.T) {
	a := &fakeAuth{}
	h := New(nil, a, "https://bf.test").Routes()
	headers := map[string]string{"Origin": "https://bf.test", "Content-Type": "application/json", "Authorization": "Bearer jwt", "X-User-ID": "7"}
	if w := req(h, "POST", base+"/session", `{}`, headers, nil); w.Code != 200 || a.logins != 0 || a.checks != 1 {
		t.Fatal(w.Code)
	}
	if w := req(h, "POST", base+"/session", `{"account":"u","password":"p"}`, headers, nil); w.Code != 401 {
		t.Fatal(w.Code)
	}
}
func TestStaticWebUsesBrandAndSecurityHeaders(t *testing.T) {
	h := New(nil, &fakeAuth{}, "http://localhost:8087").Routes()
	w := req(h, "GET", "/moments/", "", nil, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "brand.jpg") || !strings.Contains(w.Body.String(), "app.js") || strings.Contains(w.Body.String(), "localStorage") {
		t.Fatal(w.Code)
	}
	if w.Header().Get("Content-Security-Policy") == "" || w.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatal(w.Header())
	}
}
func TestLoginLimiterConcurrentBoundedAndExpiry(t *testing.T) {
	var l loginLimiter
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() { defer wg.Done(); l.allow("127.0.0.1") }()
	}
	wg.Wait()
	if l.allow("127.0.0.1") {
		t.Fatal("unbounded login")
	}
	l.entries["127.0.0.1"] = attempts{time: time.Now().Add(-2 * time.Minute), count: 99}
	if !l.allow("127.0.0.1") {
		t.Fatal("not reset")
	}
	for i := 0; i < 4096; i++ {
		l.entries[string(rune(i))] = attempts{time: time.Now()}
	}
	// 表满时放行新来源但不记录，内存不再增长。
	full := len(l.entries)
	if !l.allow("new-host") || len(l.entries) != full {
		t.Fatal("full limiter rejected new source or grew", len(l.entries))
	}
}

type rpcClient struct {
	pb.AuthServiceClient
	check *pb.CheckJwtRsp
	login *pb.LoginRsp
	err   error
}

func (c rpcClient) CheckJwt(context.Context, *pb.CheckJwtReq, ...grpc.CallOption) (*pb.CheckJwtRsp, error) {
	return c.check, c.err
}
func (c rpcClient) Login(context.Context, *pb.LoginReq, ...grpc.CallOption) (*pb.LoginRsp, error) {
	return c.login, c.err
}
func TestRPCAuthChecksResultAndReturnedUser(t *testing.T) {
	for _, result := range []*pb.CheckJwtRsp{nil, {Result: pb.AuthResult_JWT_ERROR}, {Result: pb.AuthResult_OK, UserId: 8}, {Result: pb.AuthResult_SERVICE_ERROR}} {
		_, err := (RPCAuth{Client: rpcClient{check: result}}).Check(context.Background(), 7, "jwt")
		if err == nil {
			t.Fatal(result)
		}
	}
	if user, err := (RPCAuth{Client: rpcClient{check: &pb.CheckJwtRsp{UserId: 7}}}).Check(context.Background(), 7, "jwt"); err != nil || user != 7 {
		t.Fatal(user, err)
	}
	if _, err := (RPCAuth{Client: rpcClient{err: errors.New("unavailable")}}).Check(context.Background(), 7, "jwt"); !errors.Is(err, errUnavailable) {
		t.Fatal(err)
	}
}
func TestOriginParsingIsExact(t *testing.T) {
	for _, raw := range []string{"https://bf.test/path", "https://user@bf.test", "https://bf.test?x=1", "https://bf.test#x", "https://bf.test:0", "file:///tmp"} {
		if _, err := Origin(raw); err == nil {
			t.Fatal(raw)
		}
	}
	origin, err := Origin("https://BF.test:443/")
	if err != nil || origin != "https://bf.test" {
		t.Fatal(origin, err)
	}
}

func TestSessionLimitsByForwardedClientIP(t *testing.T) {
	a := &fakeAuth{}
	handler := New(nil, a, "https://bf.test").Routes()
	login := func(remote, forwarded string) int {
		r := httptest.NewRequest("POST", base+"/session", strings.NewReader(`{"account":"user","password":"secret"}`))
		r.RemoteAddr = remote
		r.Header.Set("Origin", "https://bf.test")
		r.Header.Set("Content-Type", "application/json")
		if forwarded != "" {
			r.Header.Set("X-Forwarded-For", forwarded)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Code
	}
	for i := 0; i < 10; i++ {
		if code := login("10.0.0.5:4000", "203.0.113.1"); code != 200 {
			t.Fatalf("attempt %d: %d", i+1, code)
		}
	}
	if code := login("10.0.0.5:4000", "203.0.113.1"); code != 429 {
		t.Fatalf("client behind ingress not limited: %d", code)
	}
	// 同一ingress后面的其他用户不受影响。
	if code := login("10.0.0.5:4000", "203.0.113.2"); code != 200 {
		t.Fatalf("other client behind ingress limited: %d", code)
	}
	// 直连客户端伪造的转发头被忽略，仍按对端地址计数。
	for i := 0; i < 10; i++ {
		login("198.51.100.7:4000", fmt.Sprintf("192.0.2.%d", i))
	}
	if code := login("198.51.100.7:4000", "192.0.2.99"); code != 429 {
		t.Fatalf("spoofed forwarded header bypassed limit: %d", code)
	}
	// ingress没带可用转发头时不按IP限流，交给auth的账号限流。
	for i := 0; i < 12; i++ {
		if code := login("10.0.0.6:4000", ""); code != 200 {
			t.Fatalf("unknown client ip limited: %d", code)
		}
	}
}
func TestRPCAuthLoginMapsRateLimit(t *testing.T) {
	_, _, err := (RPCAuth{Client: rpcClient{login: &pb.LoginRsp{Result: pb.AuthResult_RATE_LIMITED}}}).Login(context.Background(), "user", "pass")
	if !errors.Is(err, errRateLimited) {
		t.Fatal(err)
	}
}
