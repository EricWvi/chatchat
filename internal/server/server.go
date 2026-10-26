// Package server exposes the store over the JSON API and serves the
// embedded frontend.
package server

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"chatchat/internal/auth"
	"chatchat/internal/config"
	"chatchat/internal/notify"
	"chatchat/internal/store"
)

// ProtocolVersion is the version of the structured protocol the CLI checks.
const ProtocolVersion = 1

// Options configure the server.
type Options struct {
	Version string
	// PublicURL is the site origin used in chat links; empty derives it
	// from the request (dev mode).
	PublicURL string
	Limits    config.Limits
}

// Server is the HTTP handler for auth, the API and the frontend.
type Server struct {
	store   *store.Store
	auth    *auth.Service
	hub     *notify.Hub
	opts    Options
	handler http.Handler

	sweepMu   sync.Mutex
	lastSweep time.Time
}

// New returns the server. web is the built frontend.
func New(st *store.Store, authn *auth.Service, web fs.FS, opts Options) (*Server, error) {
	s := &Server{store: st, auth: authn, hub: notify.NewHub(), opts: opts}
	static, err := newStatic(web)
	if err != nil {
		return nil, err
	}

	mux := http.NewServeMux()
	authn.Register(mux)
	mux.Handle("/", static)

	api := http.NewServeMux()
	mux.Handle("/api/", withProtocol(api))
	api.Handle("GET /api/v1/meta", s.handle(s.getMeta))

	// Account: login session or user token, as each handler allows.
	api.Handle("GET /api/v1/me", s.handle(s.getMe))
	api.Handle("GET /api/v1/tokens", s.handle(s.listTokens))
	api.Handle("POST /api/v1/tokens", s.handle(s.createToken))
	api.Handle("DELETE /api/v1/tokens/{token_id}", s.handle(s.revokeToken))
	api.Handle("POST /api/v1/chats", s.handle(s.createChat))
	api.Handle("GET /api/v1/chats", s.handle(s.listChats))
	api.Handle("GET /api/v1/chats/{chat_id}", s.handle(s.getChat))
	api.Handle("POST /api/v1/chats/{chat_id}/participations", s.handle(s.join))
	api.Handle("GET /api/v1/participations", s.handle(s.listParticipations))

	// CLI participants: participation secret only.
	const agent = "/api/v1/agent/chats/{chat_id}"
	api.Handle("GET "+agent+"/state", s.participant(s.cliParticipant, s.getState))
	api.Handle("GET "+agent+"/messages", s.participant(s.cliParticipant, s.getMessages))
	api.Handle("GET "+agent+"/messages/wait", s.participant(s.cliParticipant, s.waitMessages))
	api.Handle("POST "+agent+"/messages", s.participant(s.cliParticipant, s.sendMessage))
	api.Handle("POST "+agent+"/propose-end", s.participant(s.cliParticipant, s.proposeEnd))
	api.Handle("POST "+agent+"/leave", s.participant(s.cliParticipant, s.leave))

	// Web participants: login session only.
	const webChat = "/api/v1/web/chats/{chat_id}"
	api.Handle("POST "+webChat+"/enter", s.handle(s.enter))
	api.Handle("DELETE "+webChat, s.handle(s.destroyChat))
	api.Handle("GET "+webChat+"/state", s.participant(s.webParticipant, s.getState))
	api.Handle("GET "+webChat+"/messages", s.participant(s.webParticipant, s.getMessages))
	api.Handle("GET "+webChat+"/messages/wait", s.participant(s.webParticipant, s.waitMessages))
	api.Handle("POST "+webChat+"/messages", s.participant(s.webParticipant, s.sendMessage))
	api.Handle("POST "+webChat+"/propose-end", s.participant(s.webParticipant, s.proposeEnd))
	api.Handle("POST "+webChat+"/leave", s.participant(s.webParticipant, s.leave))

	api.Handle("/", s.handle(func(w http.ResponseWriter, r *http.Request) error {
		return &apiError{http.StatusNotFound, "not_found", "接口不存在"}
	}))
	s.handler = logRequests(mux)
	return s, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.handler.ServeHTTP(w, r) }

func (s *Server) getMeta(w http.ResponseWriter, r *http.Request) error {
	l := s.opts.Limits
	writeJSON(w, http.StatusOK, map[string]any{
		"protocol_version": ProtocolVersion,
		"server_version":   s.opts.Version,
		"limits": map[string]int{
			"presence_lease_seconds":     int(l.PresenceLease.Seconds()),
			"idle_destroy_after_seconds": int(l.IdleDestroyAfter.Seconds()),
			"max_chat_lifetime_seconds":  int(l.MaxChatLifetime.Seconds()),
			"wait_max_seconds":           int(l.WaitMax.Seconds()),
			"page_size_default":          l.PageSizeDefault,
			"page_size_max":              l.PageSizeMax,
			"message_max_chars":          store.MaxMessageChars,
			"display_name_max_chars":     store.MaxDisplayName,
			"summary_threshold":          l.SummaryThreshold,
		},
	})
	return nil
}

// withProtocol stamps every API response with the protocol version.
func withProtocol(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Chatchat-Protocol", "1")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// logRequests logs API requests. Only the method, path and status are
// logged: headers, query strings and bodies may carry credentials or chat
// content.
func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		slog.Info("request", "method", r.Method, "path", r.URL.Path, "status", rec.status,
			"ms", time.Since(start).Milliseconds())
	})
}

// apiError is an error response with its stable code.
type apiError struct {
	status int
	code   string
	msg    string
}

func (e *apiError) Error() string { return e.code + ": " + e.msg }

func errInvalid(msg string) error { return &apiError{http.StatusBadRequest, "invalid_request", msg} }

var (
	errUnauthenticated  = &apiError{http.StatusUnauthorized, "unauthenticated", "未登录，或用户 token 不存在"}
	errTokenRevoked     = &apiError{http.StatusUnauthorized, "token_revoked", "用户 token 已吊销"}
	errInvalidCreds     = &apiError{http.StatusUnauthorized, "invalid_credentials", "参与凭据无效：参与者不存在或 secret 不匹配"}
	errChatMismatch     = &apiError{http.StatusUnauthorized, "chat_mismatch", "参与凭据属于另一个 chat"}
	errLeft             = &apiError{http.StatusGone, "participation_left", "该参与者已退出，需要继续参与请重新 join"}
	errDestroyed        = &apiError{http.StatusGone, "chat_destroyed", "chat 已销毁"}
	errChatNotFound     = &apiError{http.StatusNotFound, "chat_not_found", "没有这个 chat"}
	errUntrustedOrigin  = &apiError{http.StatusForbidden, "untrusted_origin", "请求来源不受信任"}
	errRequestIDReused  = &apiError{http.StatusConflict, "request_id_conflict", "同一 client_request_id 已用于不同的正文"}
	errNotEntered       = &apiError{http.StatusForbidden, "forbidden", "尚未进入这个 chat"}
	errNotCreator       = &apiError{http.StatusForbidden, "forbidden", "只有创建者可以销毁 chat"}
	errTokenNotFound    = &apiError{http.StatusNotFound, "not_found", "没有这个用户 token"}
	errUserTokenOnAgent = wrongCredential("用户 token 不能用于参与者接口，请使用 join 返回的参与凭据")
)

func wrongCredential(msg string) error {
	return &apiError{http.StatusUnauthorized, "wrong_credential_type", msg}
}

// handle adapts a handler that reports failure as an error.
func (s *Server) handle(fn func(http.ResponseWriter, *http.Request) error) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := fn(w, r); err != nil {
			writeErr(w, r, err)
		}
	})
}

// writeErr turns store errors into their API codes. A store.ErrNotFound
// reaching here means a chat; other lookups translate theirs first.
func writeErr(w http.ResponseWriter, r *http.Request, err error) {
	var ae *apiError
	switch {
	case errors.As(err, &ae):
	case errors.Is(err, store.ErrInvalid):
		ae = &apiError{http.StatusBadRequest, "invalid_request", detail(err, store.ErrInvalid)}
	case errors.Is(err, store.ErrTooLarge):
		ae = &apiError{http.StatusRequestEntityTooLarge, "too_large", detail(err, store.ErrTooLarge)}
	case errors.Is(err, store.ErrRequestIDConflict):
		ae = errRequestIDReused
	case errors.Is(err, store.ErrChatDestroyed):
		ae = errDestroyed
	case errors.Is(err, store.ErrParticipationLeft):
		ae = errLeft
	case errors.Is(err, store.ErrNotFound):
		ae = errChatNotFound
	case r.Context().Err() != nil:
		// The client went away; nobody reads the response.
		return
	default:
		slog.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
		ae = &apiError{http.StatusInternalServerError, "internal", "服务器内部错误"}
	}
	writeJSON(w, ae.status, map[string]string{"code": ae.code, "error": ae.msg})
}

// detail strips the sentinel prefix from a wrapped store error.
func detail(err, sentinel error) string {
	return strings.TrimPrefix(err.Error(), sentinel.Error()+": ")
}

// defaultBody caps request bodies; a message of 16000 characters takes at
// most 64 KiB of UTF-8.
const defaultBody = 128 << 10

// decode reads a JSON body. An empty body leaves v untouched.
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	err := json.NewDecoder(http.MaxBytesReader(w, r.Body, defaultBody)).Decode(v)
	var tooLarge *http.MaxBytesError
	switch {
	case err == nil || errors.Is(err, io.EOF):
		return nil
	case errors.As(err, &tooLarge):
		return &apiError{http.StatusRequestEntityTooLarge, "too_large", "请求体太大"}
	}
	return errInvalid("请求体不是有效的 JSON")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
