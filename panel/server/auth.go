package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

type panelAuth struct {
	username string
	password string
}

type panelAuthStore struct {
	path    string
	mu      sync.Mutex
	modTime time.Time
	auth    panelAuth
}

func newPanelAuthStore(path string) (*panelAuthStore, error) {
	auth, modTime, err := loadPanelAuth(path)
	if err != nil {
		return nil, err
	}
	return &panelAuthStore{path: path, modTime: modTime, auth: auth}, nil
}

func (s *panelAuthStore) current() panelAuth {
	s.mu.Lock()
	defer s.mu.Unlock()

	info, err := os.Stat(s.path)
	if err != nil {
		return s.auth
	}
	modTime := info.ModTime()
	if !modTime.After(s.modTime) && !modTime.Before(s.modTime) {
		return s.auth
	}

	auth, loadedModTime, err := loadPanelAuth(s.path)
	if err != nil {
		return s.auth
	}
	s.auth = auth
	s.modTime = loadedModTime
	return s.auth
}

func (s *panelAuthStore) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" || r.URL.Path == "/logout" {
			next.ServeHTTP(w, r)
			return
		}

		auth := s.current()
		if !auth.enabled() {
			next.ServeHTTP(w, r)
			return
		}
		if auth.requestAllowed(r) {
			next.ServeHTTP(w, r)
			return
		}
		if wantsLoginPage(r) {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		writeUnauthorized(w)
	})
}

func (a panelAuth) enabled() bool {
	return a.username != "" && a.password != ""
}

func (a panelAuth) requestAllowed(r *http.Request) bool {
	username, password, ok := r.BasicAuth()
	if ok && a.valid(username, password) {
		return true
	}
	cookie, err := r.Cookie(panelSessionCookie)
	return err == nil && a.validSession(cookie.Value)
}

func (a panelAuth) valid(username string, password string) bool {
	return a.enabled() && constantTimeEqual(username, a.username) && constantTimeEqual(password, a.password)
}

func (a panelAuth) sessionValue() string {
	if !a.enabled() {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(a.password))
	_, _ = mac.Write([]byte(a.username))
	_, _ = mac.Write([]byte("\nturnsocks-panel-session-v1"))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (a panelAuth) validSession(value string) bool {
	expected := a.sessionValue()
	return expected != "" && constantTimeEqual(value, expected)
}

func constantTimeEqual(a string, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func wantsLoginPage(r *http.Request) bool {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		return false
	}
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return true
	}
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

func writeUnauthorized(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusUnauthorized)
	writeJSON(w, apiResponse{OK: false, Message: "请先登录面板"})
}

func (s *panelAuthStore) handleLogin(w http.ResponseWriter, r *http.Request) {
	auth := s.current()
	if !auth.enabled() {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if auth.requestAllowed(r) && r.Method == http.MethodGet {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	switch r.Method {
	case http.MethodGet, http.MethodHead:
		writeLoginPage(w, http.StatusOK, "")
	case http.MethodPost:
		r.Body = http.MaxBytesReader(w, r.Body, 8*1024)
		if err := r.ParseForm(); err != nil {
			writeLoginPage(w, http.StatusBadRequest, "登录请求格式错误")
			return
		}
		if !auth.valid(r.FormValue("username"), r.FormValue("password")) {
			writeLoginPage(w, http.StatusUnauthorized, "用户名或密码不正确")
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name:     panelSessionCookie,
			Value:    auth.sessionValue(),
			Path:     "/",
			MaxAge:   panelSessionMaxAge,
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
		})
		http.Redirect(w, r, "/", http.StatusSeeOther)
	default:
		writeMethodNotAllowed(w)
	}
}

func (s *panelAuthStore) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     panelSessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func loadPanelAuth(path string) (panelAuth, time.Time, error) {
	info, err := os.Stat(path)
	if err != nil {
		return panelAuth{}, time.Time{}, err
	}
	cfg, err := readProxyConfig(path)
	if err != nil {
		return panelAuth{}, time.Time{}, err
	}
	return panelAuth{
		username: cfg.PanelUsername,
		password: cfg.PanelPassword,
	}, info.ModTime(), nil
}
