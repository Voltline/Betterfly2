package platform

import (
	"chatbotService/internal/config"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 网关把管理页面挂在/chatbot/admin/，直连端口仍是/admin/，两处的页面与接口都要可用。
func TestAdminRoutesServeDirectAndGatewayPrefixes(t *testing.T) {
	p := &Platform{config: config.Config{AdminToken: "admin-token-0123456789"}}
	mux := http.NewServeMux()
	p.AdminRoutes(mux)
	for _, prefix := range []string{"/admin", "/chatbot/admin"} {
		page := httptest.NewRecorder()
		mux.ServeHTTP(page, httptest.NewRequest(http.MethodGet, prefix+"/", nil))
		if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), `"api/bots"`) {
			t.Fatalf("%s page: %d", prefix, page.Code)
		}
		denied := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, prefix+"/api/bots", nil)
		request.Header.Set("X-Admin-Token", "wrong")
		mux.ServeHTTP(denied, request)
		if denied.Code != http.StatusUnauthorized {
			t.Fatalf("%s api without token: %d", prefix, denied.Code)
		}
	}
	redirect := httptest.NewRecorder()
	mux.ServeHTTP(redirect, httptest.NewRequest(http.MethodGet, "/chatbot/admin", nil))
	if location := redirect.Header().Get("Location"); redirect.Code != http.StatusTemporaryRedirect || location != "/chatbot/admin/" {
		t.Fatalf("redirect %d %q", redirect.Code, location)
	}
	if strings.Contains(string(adminPage), `"/admin/`) {
		t.Fatal("admin page still uses absolute /admin/ paths")
	}
}
