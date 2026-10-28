package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

// Response shapes the text output reads; --json prints the server's body
// as it is.

type messageView struct {
	Sequence        int64  `json:"sequence"`
	Kind            string `json:"kind"`
	ParticipationID string `json:"participation_id"`
	DisplayName     string `json:"display_name"`
	Text            string `json:"text"`
	CreatedAt       string `json:"created_at"`
}

type messagePage struct {
	Messages       []messageView `json:"messages"`
	HasMore        bool          `json:"has_more"`
	LatestSequence int64         `json:"latest_sequence"`
}

type stateView struct {
	Chat struct {
		ChatID         string  `json:"chat_id"`
		Title          string  `json:"title"`
		Goal           string  `json:"goal"`
		ExpiresAt      string  `json:"expires_at"`
		IdleDestroyAt  *string `json:"idle_destroy_at"`
		LatestSequence int64   `json:"latest_sequence"`
	} `json:"chat"`
	Summary *struct {
		Revision      int64  `json:"revision"`
		CoversThrough int64  `json:"covers_through_sequence"`
		Content       string `json:"content"`
		CreatedAt     string `json:"created_at"`
	} `json:"summary"`
	SummaryJob struct {
		Status       string  `json:"status"`
		LastFailedAt *string `json:"last_failed_at"`
	} `json:"summary_job"`
	Participants []struct {
		ParticipationID string `json:"participation_id"`
		Kind            string `json:"kind"`
		DisplayName     string `json:"display_name"`
		Status          string `json:"status"`
	} `json:"participants"`
	You struct {
		ParticipationID string `json:"participation_id"`
	} `json:"you"`
}

func decodeJSON(raw []byte, v any) error {
	if err := json.Unmarshal(raw, v); err != nil {
		return &cliError{exit: exitError, code: "bad_response", msg: "could not parse the server response"}
	}
	return nil
}

// localTime renders an RFC 3339 time in the local zone.
func localTime(s string) string {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return s
	}
	return t.Local().Format("2006-01-02 15:04:05")
}

// shortID is the suffix that tells participants with the same name apart.
func shortID(id string) string {
	if len(id) <= 4 {
		return id
	}
	return "…" + id[len(id)-4:]
}

func speaker(name, id string) string { return fmt.Sprintf("%s (%s)", name, shortID(id)) }

func kindName(kind string) string {
	switch kind {
	case "web":
		return "web"
	case "cli":
		return "CLI"
	}
	return kind
}

var statusNames = map[string]string{"online": "online", "offline": "offline", "left": "left"}

func printState(w io.Writer, st stateView) {
	c := st.Chat
	fmt.Fprintf(w, "chat: %s (%s)\n", c.Title, c.ChatID)
	goal := c.Goal
	if goal == "" {
		goal = "(none)"
	}
	fmt.Fprintf(w, "Goal: %s\n", goal)
	life := "destroyed by " + localTime(c.ExpiresAt) + " at the latest"
	if c.IdleDestroyAt != nil {
		life += "; nobody online, destroyed for idleness at " + localTime(*c.IdleDestroyAt)
	}
	fmt.Fprintf(w, "Latest message: #%d; %s\n", c.LatestSequence, life)
	fmt.Fprintf(w, "You: %s\n\n", st.You.ParticipationID)

	covers := int64(0)
	job := ""
	switch st.SummaryJob.Status {
	case "pending":
		job = "; a new summary is being generated"
	case "failed":
		job = "; the last attempt failed"
		if st.SummaryJob.LastFailedAt != nil {
			job += " (" + localTime(*st.SummaryJob.LastFailedAt) + ")"
		}
		job += ", retried when next due"
	}
	if s := st.Summary; s != nil {
		covers = s.CoversThrough
		fmt.Fprintf(w, "Summary: revision %d, covers through #%d, %d newer messages%s\n", s.Revision, s.CoversThrough, c.LatestSequence-s.CoversThrough, job)
		fmt.Fprintln(w, strings.TrimRight(s.Content, "\n"))
	} else {
		fmt.Fprintf(w, "Summary: none yet%s\n", job)
	}

	online := 0
	for _, p := range st.Participants {
		if p.Status == "online" {
			online++
		}
	}
	fmt.Fprintf(w, "\nParticipants (%d online / %d total):\n", online, len(st.Participants))
	for _, p := range st.Participants {
		me := ""
		if p.ParticipationID == st.You.ParticipationID {
			me = " (you)"
		}
		fmt.Fprintf(w, "  %s%s %s %s\n", speaker(p.DisplayName, p.ParticipationID), me, kindName(p.Kind), statusNames[p.Status])
	}
	if covers < c.LatestSequence {
		fmt.Fprintf(w, "\n-- Messages after the summary: chatchat read --after %d\n", covers)
	} else {
		fmt.Fprintf(w, "\n-- Up to date: chatchat wait --after %d\n", c.LatestSequence)
	}
}

// printPage prints messages and where to continue from.
func printPage(w io.Writer, page messagePage, after int64, waited bool) {
	last := after
	for _, m := range page.Messages {
		last = m.Sequence
		who := speaker(m.DisplayName, m.ParticipationID)
		if m.Kind == "system" {
			fmt.Fprintf(w, "#%d %s [system] %s %s\n", m.Sequence, localTime(m.CreatedAt), who, m.Text)
			continue
		}
		fmt.Fprintf(w, "#%d %s %s:\n", m.Sequence, localTime(m.CreatedAt), who)
		for _, line := range strings.Split(strings.TrimRight(m.Text, "\n"), "\n") {
			fmt.Fprintf(w, "  %s\n", line)
		}
	}
	switch {
	case len(page.Messages) == 0 && waited:
		fmt.Fprintf(w, "-- Wait timed out with no new messages; to keep waiting use --after %d\n", last)
	case len(page.Messages) == 0:
		fmt.Fprintf(w, "-- No new messages; to wait for new ones: chatchat wait --after %d\n", last)
	case page.HasMore:
		fmt.Fprintf(w, "-- Read through #%d, more available (latest #%d): chatchat read --after %d\n", last, page.LatestSequence, last)
	default:
		fmt.Fprintf(w, "-- Read through #%d, up to date; next time use --after %d\n", last, last)
	}
}
