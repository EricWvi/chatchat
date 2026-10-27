package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"chatchat/internal/store"
)

func newService(t *testing.T, cfg Config) (*Service, http.Handler) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	svc := New(st, cfg)
	mux := http.NewServeMux()
	svc.Register(mux)
	mux.Handle("GET /whoami", whoami(svc))
	return svc, mux
}

// whoami answers with the signed-in user, or 401.
func whoami(svc *Service) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, ok := svc.SessionUser(w, r)
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"id": u.ID, "name": u.Name, "email": u.Email})
	})
}

func serve(h http.Handler, method, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func TestDevUserSignsEveryRequestIn(t *testing.T) {
	_, h := newService(t, Config{DevUser: "eric"})
	rec := serve(h, "GET", "/whoami")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var u map[string]any
	json.Unmarshal(rec.Body.Bytes(), &u)
	if u["name"] != "eric" {
		t.Fatalf("user = %+v", u)
	}
	if rec := serve(h, "GET", "/auth/login"); rec.Code != http.StatusFound || rec.Header().Get("Location") != "/" {
		t.Fatalf("login: status = %d, location = %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestCallbackWithoutLoginCookieIsRejected(t *testing.T) {
	_, h := newService(t, Config{PublicURL: "https://home.test", Issuer: "https://idp.invalid", ClientID: "x", ClientSecret: "y"})
	rec := serve(h, "GET", "/auth/callback?code=c&state=s")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", rec.Code)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie {
			t.Fatalf("session cookie set: %+v", c)
		}
	}
}

func TestLoginReportsUnavailableProvider(t *testing.T) {
	_, h := newService(t, Config{
		PublicURL: "https://home.test", Issuer: "http://127.0.0.1:1", ClientID: "x", ClientSecret: "y",
	})
	if rec := serve(h, "GET", "/auth/login"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestLogoutChecksOrigin(t *testing.T) {
	_, h := newService(t, Config{PublicURL: "https://home.test", Issuer: "https://idp.invalid", ClientID: "x", ClientSecret: "y"})
	req := httptest.NewRequest("POST", "/auth/logout", nil)
	req.Header.Set("Origin", "https://evil.test")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestLoginReturnsOnlyToLocalPaths(t *testing.T) {
	_, h := newService(t, Config{DevUser: "eric"})
	for next, want := range map[string]string{
		"/c/c_abc":           "/c/c_abc",
		"":                   "/",
		"https://evil.test/": "/",
		"//evil.test/":       "/",
		"/\\evil.test":       "/",
		"c/relative":         "/",
		"/tokens?x=1":        "/tokens?x=1",
	} {
		rec := serve(h, "GET", "/auth/login?next="+url.QueryEscape(next))
		if got := rec.Header().Get("Location"); rec.Code != http.StatusFound || got != want {
			t.Errorf("next %q: status %d, location %q, want %q", next, rec.Code, got, want)
		}
	}
}
