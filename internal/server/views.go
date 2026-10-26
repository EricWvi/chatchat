package server

import (
	"fmt"
	"time"

	"chatchat/internal/store"
)

// jsonTime is a store time (Unix milliseconds) rendered as RFC 3339 UTC,
// or null when unset.
type jsonTime int64

func (t jsonTime) MarshalJSON() ([]byte, error) {
	if t == 0 {
		return []byte("null"), nil
	}
	return []byte(`"` + time.UnixMilli(int64(t)).UTC().Format(time.RFC3339) + `"`), nil
}

func userID(u store.User) string { return fmt.Sprintf("u_%d", u.ID) }

type tokenView struct {
	TokenID    string   `json:"token_id"`
	Name       string   `json:"name"`
	Token      string   `json:"token,omitempty"`
	CreatedAt  jsonTime `json:"created_at"`
	LastUsedAt jsonTime `json:"last_used_at"`
}

func viewToken(t store.UserToken) tokenView {
	return tokenView{TokenID: t.ID, Name: t.Name, CreatedAt: jsonTime(t.CreatedAt), LastUsedAt: jsonTime(t.LastUsedAt)}
}

// chatItemView is a chat in the account's chat list. The presence figures
// are null once the chat is destroyed.
type chatItemView struct {
	ChatID        string   `json:"chat_id"`
	Title         string   `json:"title"`
	CreatedByMe   bool     `json:"created_by_me"`
	CreatedAt     jsonTime `json:"created_at"`
	ExpiresAt     jsonTime `json:"expires_at"`
	OnlineCount   *int     `json:"online_count"`
	ActiveCount   *int     `json:"active_count"`
	LastMessageAt jsonTime `json:"last_message_at"`
	IdleDestroyAt jsonTime `json:"idle_destroy_at"`
	DestroyedAt   jsonTime `json:"destroyed_at"`
	DestroyReason *string  `json:"destroy_reason"`
}

func (s *Server) viewChatItem(ci store.ChatInfo, u store.User) chatItemView {
	v := chatItemView{
		ChatID: ci.ID, Title: ci.Title, CreatedByMe: ci.CreatorID == u.ID,
		CreatedAt: jsonTime(ci.CreatedAt), ExpiresAt: jsonTime(ci.ExpiresAt),
	}
	if ci.Destroyed() {
		v.DestroyedAt, v.DestroyReason = jsonTime(ci.DestroyedAt), &ci.DestroyReason
		return v
	}
	v.OnlineCount, v.ActiveCount = &ci.OnlineCount, &ci.ActiveCount
	v.LastMessageAt = jsonTime(ci.LastMessageAt)
	v.IdleDestroyAt = s.idleDestroyAt(ci)
	return v
}

// idleDestroyAt is when an empty chat will be destroyed for idling, or
// unset while someone is online.
func (s *Server) idleDestroyAt(ci store.ChatInfo) jsonTime {
	if ci.OnlineCount > 0 {
		return 0
	}
	return jsonTime(ci.IdleSince + s.opts.Limits.IdleDestroyAfter.Milliseconds())
}

type participationView struct {
	ParticipationID string   `json:"participation_id"`
	ChatID          string   `json:"chat_id"`
	Kind            string   `json:"kind"`
	DisplayName     string   `json:"display_name"`
	JoinedAt        jsonTime `json:"joined_at"`
	LeftAt          jsonTime `json:"left_at"`
	EndReason       *string  `json:"end_reason"`
}

func viewParticipation(p store.Participation) participationView {
	v := participationView{
		ParticipationID: p.ID, ChatID: p.ChatID, Kind: p.Kind, DisplayName: p.DisplayName,
		JoinedAt: jsonTime(p.JoinedAt), LeftAt: jsonTime(p.LeftAt),
	}
	if p.EndReason != "" {
		v.EndReason = &p.EndReason
	}
	return v
}

// participantView is a participant as other participants see it.
type participantView struct {
	ParticipationID string   `json:"participation_id"`
	Kind            string   `json:"kind"`
	DisplayName     string   `json:"display_name"`
	Status          string   `json:"status"`
	JoinedAt        jsonTime `json:"joined_at"`
	LeftAt          jsonTime `json:"left_at"`
	LastSeenAt      jsonTime `json:"last_seen_at"`
}

func (s *Server) viewParticipant(p store.Participation, now int64) participantView {
	return participantView{
		ParticipationID: p.ID, Kind: p.Kind, DisplayName: p.DisplayName,
		Status:   p.Status(now, s.opts.Limits.PresenceLease),
		JoinedAt: jsonTime(p.JoinedAt), LeftAt: jsonTime(p.LeftAt), LastSeenAt: jsonTime(p.LastSeenAt),
	}
}

type messageView struct {
	MessageID       string   `json:"message_id"`
	Sequence        int64    `json:"sequence"`
	Kind            string   `json:"kind"`
	ParticipationID string   `json:"participation_id"`
	DisplayName     string   `json:"display_name"`
	Text            string   `json:"text"`
	CreatedAt       jsonTime `json:"created_at"`
}

func viewMessage(m store.Message) messageView {
	return messageView{
		MessageID: m.ID, Sequence: m.Sequence, Kind: m.Kind, ParticipationID: m.ParticipationID,
		DisplayName: m.DisplayName, Text: m.Text, CreatedAt: jsonTime(m.CreatedAt),
	}
}

func viewMessages(ms []store.Message) []messageView {
	out := make([]messageView, len(ms))
	for i, m := range ms {
		out[i] = viewMessage(m)
	}
	return out
}
