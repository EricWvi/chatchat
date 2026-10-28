// Package auth signs users in through OIDC (Authelia) and keeps them signed
// in with a server-side session.
//
// The goal is only to tell users apart. A session never expires on the
// server; it ends when the user signs out. There are no refresh tokens and
// no periodic re-verification with the identity provider.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"

	"chatchat/internal/store"
)

const (
	sessionCookie = "chatchat_session"
	loginCookie   = "chatchat_login"
	// Browsers cap persistent cookies at 400 days, so the cookie is
	// re-issued while in use to keep the session effectively permanent.
	cookieLifetime = 400 * 24 * time.Hour
	refreshAfter   = 24 * time.Hour
	loginLifetime  = 10 * time.Minute
)

// SessionCookie is the name of the cookie that carries the session.
const SessionCookie = sessionCookie

// Config configures the service. Either DevUser or all OIDC fields are set.
type Config struct {
	PublicURL    string
	Issuer       string
	ClientID     string
	ClientSecret string
	// DevUser signs every request in as this local user without OIDC.
	DevUser string
	// HTTPClient talks to the identity provider; nil uses a default client.
	HTTPClient *http.Client
}

// Service handles login, logout and session lookup.
type Service struct {
	store    *store.Store
	provider *lazyProvider
	origin   string // empty in dev mode
	secure   bool

	devUser     string
	devOnce     sync.Once
	devErr      error
	devResolved store.User
}

// New returns the auth service. It does not contact the identity provider.
func New(st *store.Store, cfg Config) *Service {
	s := &Service{store: st, devUser: cfg.DevUser}
	if cfg.DevUser != "" {
		return s
	}
	public := strings.TrimSuffix(cfg.PublicURL, "/")
	s.origin = public
	s.secure = strings.HasPrefix(public, "https://")
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	s.provider = &lazyProvider{
		issuer:       cfg.Issuer,
		clientID:     cfg.ClientID,
		clientSecret: cfg.ClientSecret,
		redirectURL:  public + "/auth/callback",
		http:         client,
	}
	return s
}

// Register adds the /auth endpoints to mux.
func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /auth/login", s.login)
	mux.HandleFunc("GET /auth/callback", s.callback)
	mux.HandleFunc("POST /auth/logout", s.logout)
}

// SessionUser returns the user signed in on this browser. In dev mode every
// request is signed in as the dev user.
func (s *Service) SessionUser(w http.ResponseWriter, r *http.Request) (store.User, bool) {
	if s.devUser != "" {
		s.devOnce.Do(func() {
			s.devResolved, s.devErr = s.store.UpsertUser(context.WithoutCancel(r.Context()),
				store.Identity{Issuer: "dev", Subject: s.devUser, Name: s.devUser})
		})
		if s.devErr != nil {
			slog.Error("dev user", "err", s.devErr)
		}
		return s.devResolved, s.devErr == nil
	}
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return store.User{}, false
	}
	user, lastSeen, err := s.store.SessionUser(r.Context(), c.Value)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			slog.Error("look up session", "err", err)
		}
		s.clearCookie(w, sessionCookie, "/")
		return store.User{}, false
	}
	// Keep the browser cookie from ever reaching its max age while the
	// session is in use. Once a day is plenty.
	if time.Since(lastSeen) > refreshAfter {
		if err := s.store.TouchSession(r.Context(), c.Value); err != nil {
			slog.Error("touch session", "err", err)
		}
		s.setSessionCookie(w, c.Value)
	}
	return user, true
}

// SameOrigin rejects state-changing requests sent from other sites,
// including sibling subdomains that SameSite=Lax treats as same-site.
// Only requests authenticated by the session cookie need it.
func (s *Service) SameOrigin(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	origin := r.Header.Get("Origin")
	return s.origin == "" || origin == "" || origin == s.origin
}

// loginFlow is kept in a short-lived cookie between /auth/login and
// /auth/callback. State ties the callback to this browser; the nonce and
// PKCE verifier tie the tokens to this login attempt.
type loginFlow struct {
	State    string `json:"s"`
	Nonce    string `json:"n"`
	Verifier string `json:"v"`
	// Next is the page to return to after signing in.
	Next string `json:"r,omitempty"`
}

// localPath returns next when it is a path on this site, else "/", so the
// login cannot be used to redirect elsewhere.
func localPath(next string) string {
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.ContainsAny(next, "\\\r\n") {
		return "/"
	}
	return next
}

// login starts signing in; ?next= names the page to come back to.
func (s *Service) login(w http.ResponseWriter, r *http.Request) {
	next := localPath(r.URL.Query().Get("next"))
	if s.devUser != "" {
		http.Redirect(w, r, next, http.StatusFound)
		return
	}
	p, err := s.provider.get(r.Context())
	if err != nil {
		slog.Error("oidc discovery", "err", err)
		s.page(w, http.StatusServiceUnavailable, "认证服务暂时不可用，请稍后重试。")
		return
	}
	flow := loginFlow{State: random(), Nonce: random(), Verifier: oauth2.GenerateVerifier(), Next: next}
	raw, _ := json.Marshal(flow)
	http.SetCookie(w, &http.Cookie{
		Name:     loginCookie,
		Value:    base64.RawURLEncoding.EncodeToString(raw),
		Path:     "/auth/",
		MaxAge:   int(loginLifetime.Seconds()),
		HttpOnly: true,
		Secure:   s.secure,
		SameSite: http.SameSiteLaxMode,
	})
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, p.oauth.AuthCodeURL(flow.State,
		oauth2.S256ChallengeOption(flow.Verifier),
		oauth2.SetAuthURLParam("nonce", flow.Nonce)), http.StatusFound)
}

func (s *Service) callback(w http.ResponseWriter, r *http.Request) {
	if s.devUser != "" {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	flow, ok := readFlow(r)
	s.clearCookie(w, loginCookie, "/auth/")
	q := r.URL.Query()
	if !ok || subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(flow.State)) != 1 {
		s.page(w, http.StatusBadRequest, "登录已过期或无效，请重新登录。")
		return
	}
	if e := q.Get("error"); e != "" {
		slog.Warn("oidc authorization denied", "error", e, "description", q.Get("error_description"))
		s.page(w, http.StatusForbidden, "登录未完成。")
		return
	}
	id, err := s.provider.identify(r.Context(), q.Get("code"), flow)
	if err != nil {
		slog.Error("oidc callback", "err", err)
		s.page(w, http.StatusBadGateway, "无法完成登录，请重试。")
		return
	}
	user, err := s.store.UpsertUser(r.Context(), id)
	if err != nil {
		slog.Error("save user", "err", err)
		s.page(w, http.StatusInternalServerError, "无法完成登录，请重试。")
		return
	}
	token, err := s.store.CreateSession(r.Context(), user.ID)
	if err != nil {
		slog.Error("create session", "err", err)
		s.page(w, http.StatusInternalServerError, "无法完成登录，请重试。")
		return
	}
	s.setSessionCookie(w, token)
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, localPath(flow.Next), http.StatusFound)
}

// logout ends only this browser's session. The identity provider's own
// session is left alone.
func (s *Service) logout(w http.ResponseWriter, r *http.Request) {
	if !s.SameOrigin(r) {
		writeError(w, http.StatusForbidden, "untrusted_origin", "untrusted request origin")
		return
	}
	if c, err := r.Cookie(sessionCookie); err == nil {
		if err := s.store.DeleteSession(r.Context(), c.Value); err != nil {
			slog.Error("delete session", "err", err)
			writeError(w, http.StatusInternalServerError, "internal", "internal server error")
			return
		}
	}
	s.clearCookie(w, sessionCookie, "/")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		MaxAge:   int(cookieLifetime.Seconds()),
		HttpOnly: true,
		Secure:   s.secure,
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Service) clearCookie(w http.ResponseWriter, name, path string) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Path: path, MaxAge: -1, HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteLaxMode,
	})
}

func readFlow(r *http.Request) (loginFlow, bool) {
	var flow loginFlow
	c, err := r.Cookie(loginCookie)
	if err != nil {
		return flow, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil || json.Unmarshal(raw, &flow) != nil || flow.State == "" {
		return flow, false
	}
	return flow, true
}

var pageTmpl = template.Must(template.New("page").Parse(`<!doctype html>
<html lang="zh-CN"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="color-scheme" content="light dark"><title>chatchat</title>
<style>body{font:15px system-ui,sans-serif;background:#faf8f5;color:#1d1d1f;display:grid;place-items:center;min-height:100vh;margin:0}
@media (prefers-color-scheme:dark){body{background:#1b1b1d;color:#f2f2f4}}a{color:inherit}</style>
<p>{{.}} <a href="/auth/login">重新登录</a></p></html>`))

func (s *Service) page(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	pageTmpl.Execute(w, msg)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"code": code, "error": msg})
}

func random() string {
	b := make([]byte, 24)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
