package server

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func TestPanelRoutes(test *testing.T) {
	panel := &app{
		configPath: filepath.Join(test.TempDir(), "missing.env"),
		ui:         fstest.MapFS{"ui/dist/index.html": {Data: []byte("panel")}},
	}
	authStore := &panelAuthStore{auth: panelAuth{username: "admin", password: "test-password"}}
	handler := panel.handler(authStore)
	for _, route := range []struct {
		path  string
		allow string
	}{
		{"/api/state", "GET, HEAD"},
		{"/api/servers/add", "POST"},
		{"/api/servers/select", "POST"},
		{"/api/servers/delete", "POST"},
		{"/api/servers/note", "POST"},
		{"/api/servers/test", "POST"},
		{"/api/config/update", "POST"},
		{"/api/restart", "POST"},
	} {
		for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodOptions} {
			test.Run(method+" "+route.path, func(test *testing.T) {
				request := httptest.NewRequest(method, route.path, nil)
				request.SetBasicAuth("admin", "test-password")
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)
				wantStatus, wantAllow := http.StatusMethodNotAllowed, route.allow
				if strings.Contains(route.allow, method) {
					wantStatus, wantAllow = http.StatusBadRequest, ""
				}
				if response.Code != wantStatus || response.Header().Get("Allow") != wantAllow {
					test.Fatalf("status = %d, Allow = %q; want %d, %q", response.Code, response.Header().Get("Allow"), wantStatus, wantAllow)
				}
			})
		}
	}
	for _, page := range []struct {
		path   string
		status int
	}{
		{"/", http.StatusOK},
		{"/missing", http.StatusNotFound},
		{"/api/missing", http.StatusNotFound},
		{"/api/state/", http.StatusNotFound},
	} {
		request := httptest.NewRequest(http.MethodGet, page.path, nil)
		request.SetBasicAuth("admin", "test-password")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != page.status {
			test.Fatalf("GET %s = %d, want %d", page.path, response.Code, page.status)
		}
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/restart", nil))
	if response.Code != http.StatusUnauthorized {
		test.Fatalf("unauthenticated API request = %d, want 401", response.Code)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/login", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), themeCSS) {
		test.Fatal("login page did not include the shared theme")
	}
	response = httptest.NewRecorder()
	writeLoginPage(response, http.StatusUnauthorized, "<script>bad</script>")
	if response.Code != http.StatusUnauthorized || !strings.Contains(response.Body.String(), "&lt;script&gt;bad&lt;/script&gt;") {
		test.Fatal("login error status or escaping changed")
	}

	// Cross-site unsafe requests must be rejected by CrossOriginProtection.
	crossSite := httptest.NewRequest(http.MethodPost, "/api/restart", nil)
	crossSite.Header.Set("Sec-Fetch-Site", "cross-site")
	crossSite.Header.Set("Origin", "https://evil.example")
	crossSite.SetBasicAuth("admin", "test-password")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, crossSite)
	if response.Code != http.StatusForbidden {
		test.Fatalf("cross-site POST status = %d, want 403", response.Code)
	}

	// Logout must require POST to avoid cross-site GET logout.
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/logout", nil))
	if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != http.MethodPost {
		test.Fatalf("GET /logout = %d Allow=%q, want 405 POST", response.Code, response.Header().Get("Allow"))
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/logout", nil))
	if response.Code != http.StatusSeeOther {
		test.Fatalf("POST /logout = %d, want 303", response.Code)
	}
}
