package server

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"chatchat/internal/notify"
	"chatchat/internal/store"
)

// accountAuth says which credentials an account endpoint accepts.
type accountAuth int

const (
	sessionOnly accountAuth = iota
	sessionOrToken
	tokenOnly
)

// bearer returns the token of an "Authorization: Bearer" header.
func bearer(r *http.Request) (token string, present bool) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return "", false
	}
	scheme, token, _ := strings.Cut(h, " ")
	if !strings.EqualFold(scheme, "Bearer") {
		return "", true
	}
	return strings.TrimSpace(token), true
}

// accountUser authenticates an account request with a login session or a
// user token, as allowed.
func (s *Server) accountUser(w http.ResponseWriter, r *http.Request, allow accountAuth) (store.User, error) {
	if token, present := bearer(r); present {
		switch {
		case strings.HasPrefix(token, store.ParticipationSecretPrefix):
			return store.User{}, wrongCredential("参与凭据不能用于账号接口，请使用用户 token")
		case allow == sessionOnly:
			return store.User{}, wrongCredential("该接口只接受网站登录状态")
		}
		u, err := s.store.TokenUser(r.Context(), token)
		switch {
		case errors.Is(err, store.ErrNotFound):
			return u, errUnauthenticated
		case errors.Is(err, store.ErrTokenRevoked):
			return u, errTokenRevoked
		}
		return u, err
	}
	if allow == tokenOnly {
		return store.User{}, wrongCredential("该接口只接受用户 token，请通过 chatchat CLI 调用")
	}
	return s.sessionUser(w, r)
}

// sessionUser authenticates a request by its login session alone.
func (s *Server) sessionUser(w http.ResponseWriter, r *http.Request) (store.User, error) {
	if _, present := bearer(r); present {
		return store.User{}, wrongCredential("该接口只接受网站登录状态")
	}
	if !s.auth.SameOrigin(r) {
		return store.User{}, errUntrustedOrigin
	}
	u, ok := s.auth.SessionUser(w, r)
	if !ok {
		return u, errUnauthenticated
	}
	return u, nil
}

func (s *Server) getMe(w http.ResponseWriter, r *http.Request) error {
	u, err := s.accountUser(w, r, sessionOrToken)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]string{"user_id": userID(u), "name": u.Name, "email": u.Email})
	return nil
}

func (s *Server) listTokens(w http.ResponseWriter, r *http.Request) error {
	u, err := s.accountUser(w, r, sessionOnly)
	if err != nil {
		return err
	}
	tokens, err := s.store.ListUserTokens(r.Context(), u.ID)
	if err != nil {
		return err
	}
	out := make([]tokenView, len(tokens))
	for i, t := range tokens {
		out[i] = viewToken(t)
	}
	writeJSON(w, http.StatusOK, map[string]any{"tokens": out})
	return nil
}

func (s *Server) createToken(w http.ResponseWriter, r *http.Request) error {
	u, err := s.accountUser(w, r, sessionOnly)
	if err != nil {
		return err
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := decode(w, r, &req); err != nil {
		return err
	}
	t, token, err := s.store.CreateUserToken(r.Context(), u.ID, req.Name)
	if err != nil {
		return err
	}
	v := viewToken(t)
	v.Token = token
	writeJSON(w, http.StatusCreated, v)
	return nil
}

func (s *Server) revokeToken(w http.ResponseWriter, r *http.Request) error {
	u, err := s.accountUser(w, r, sessionOnly)
	if err != nil {
		return err
	}
	if err := s.store.RevokeUserToken(r.Context(), u.ID, r.PathValue("token_id")); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return errTokenNotFound
		}
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) createChat(w http.ResponseWriter, r *http.Request) error {
	u, err := s.accountUser(w, r, sessionOrToken)
	if err != nil {
		return err
	}
	var req struct {
		Title string `json:"title"`
		Goal  string `json:"goal"`
	}
	if err := decode(w, r, &req); err != nil {
		return err
	}
	c, err := s.store.CreateChat(r.Context(), u.ID, req.Title, req.Goal, s.opts.Limits.MaxChatLifetime)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"chat_id": c.ID, "title": c.Title, "goal": c.Goal, "url": s.chatURL(r, c.ID),
		"created_at": jsonTime(c.CreatedAt), "expires_at": jsonTime(c.ExpiresAt),
	})
	return nil
}

// chatURL is the link that invites people to the chat.
func (s *Server) chatURL(r *http.Request, chatID string) string {
	return s.origin(r) + "/c/" + chatID
}

// origin is the site origin: the configured public URL, or in dev mode
// the request's host.
func (s *Server) origin(r *http.Request) string {
	if o := strings.TrimSuffix(s.opts.PublicURL, "/"); o != "" {
		return o
	}
	return "http://" + r.Host
}

func (s *Server) listChats(w http.ResponseWriter, r *http.Request) error {
	u, err := s.accountUser(w, r, sessionOrToken)
	if err != nil {
		return err
	}
	limit, err := s.pageSize(r)
	if err != nil {
		return err
	}
	offset := 0
	if c := r.URL.Query().Get("cursor"); c != "" {
		if offset, err = strconv.Atoi(c); err != nil || offset < 0 {
			return errInvalid("cursor 无效")
		}
	}
	chats, more, err := s.store.ListUserChats(r.Context(), u.ID, s.opts.Limits.PresenceLease, offset, limit)
	if err != nil {
		return err
	}
	out := make([]chatItemView, len(chats))
	for i, ci := range chats {
		out[i] = s.viewChatItem(ci, u)
	}
	var next *string
	if more {
		n := strconv.Itoa(offset + len(chats))
		next = &n
	}
	writeJSON(w, http.StatusOK, map[string]any{"chats": out, "next_cursor": next})
	return nil
}

// getChat shows a chat's metadata to any signed-in user who knows its ID,
// the same people who may join it. It is how a page learns that a chat
// was destroyed, and why.
func (s *Server) getChat(w http.ResponseWriter, r *http.Request) error {
	u, err := s.accountUser(w, r, sessionOrToken)
	if err != nil {
		return err
	}
	ci, err := s.store.ChatInfo(r.Context(), r.PathValue("chat_id"), s.opts.Limits.PresenceLease)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, s.viewChatItem(ci, u))
	return nil
}

func (s *Server) listParticipations(w http.ResponseWriter, r *http.Request) error {
	u, err := s.accountUser(w, r, sessionOrToken)
	if err != nil {
		return err
	}
	ps, err := s.store.UserParticipations(r.Context(), u.ID, r.URL.Query().Get("chat_id"))
	if err != nil {
		return err
	}
	out := make([]participationView, len(ps))
	for i, p := range ps {
		out[i] = viewParticipation(p)
	}
	writeJSON(w, http.StatusOK, map[string]any{"participations": out})
	return nil
}

// join creates a new CLI participant. Only a user token may do it, so the
// browser cannot mint participation secrets.
func (s *Server) join(w http.ResponseWriter, r *http.Request) error {
	u, err := s.accountUser(w, r, tokenOnly)
	if err != nil {
		return err
	}
	var req struct {
		DisplayName string `json:"display_name"`
	}
	if err := decode(w, r, &req); err != nil {
		return err
	}
	p, secret, err := s.store.Join(r.Context(), r.PathValue("chat_id"), u.ID, req.DisplayName)
	if err != nil {
		return err
	}
	s.hub.Notify(p.ChatID, notify.Messages|notify.Participants)
	writeJSON(w, http.StatusCreated, map[string]any{
		"chat_id": p.ChatID, "participation_id": p.ID, "participation_secret": secret,
		"display_name": p.DisplayName, "joined_at": jsonTime(p.JoinedAt),
	})
	return nil
}

// pageSize reads the limit parameter: the default when absent, capped at
// the maximum.
func (s *Server) pageSize(r *http.Request) (int, error) {
	l := s.opts.Limits
	v := r.URL.Query().Get("limit")
	if v == "" {
		return l.PageSizeDefault, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return 0, errInvalid("limit 必须是正整数")
	}
	return min(n, l.PageSizeMax), nil
}
