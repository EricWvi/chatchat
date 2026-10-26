package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func open(t *testing.T) *Store {
	t.Helper()
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestConcurrentSendsGetGapFreeSequences(t *testing.T) {
	st := open(t)
	ctx := context.Background()
	u, err := st.UpsertUser(ctx, Identity{Issuer: "i", Subject: "s"})
	if err != nil {
		t.Fatal(err)
	}
	c, err := st.CreateChat(ctx, u.ID, "t", "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	var ps []Participation
	for i := range 4 {
		p, _, err := st.Join(ctx, c.ID, u.ID, fmt.Sprint("p", i))
		if err != nil {
			t.Fatal(err)
		}
		ps = append(ps, p)
	}
	var wg sync.WaitGroup
	for _, p := range ps {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 25 {
				if _, _, err := st.SendText(ctx, p, fmt.Sprint(i), ""); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	ms, more, err := st.Messages(ctx, c.ID, 0, 0, 1000)
	if err != nil || more {
		t.Fatal(err, more)
	}
	if len(ms) != 104 {
		t.Fatalf("messages = %d", len(ms))
	}
	for i, m := range ms {
		if m.Sequence != int64(i+1) {
			t.Fatalf("message %d has sequence %d", i, m.Sequence)
		}
	}
	if c, _ = st.Chat(ctx, c.ID); c.LastSequence != 104 {
		t.Fatalf("last sequence = %d", c.LastSequence)
	}
}

func TestSecretsAreOnlyMatchedByHash(t *testing.T) {
	st := open(t)
	ctx := context.Background()
	u, _ := st.UpsertUser(ctx, Identity{Issuer: "i", Subject: "s"})
	c, _ := st.CreateChat(ctx, u.ID, "t", "", time.Hour)
	p, secret, err := st.Join(ctx, c.ID, u.ID, "x")
	if err != nil {
		t.Fatal(err)
	}
	got, err := st.Participation(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.secretHash == secret || !got.SecretMatches(secret) || got.SecretMatches(secret+"x") || got.SecretMatches("") {
		t.Fatal("secret check is wrong")
	}
	// A web participation has no secret at all.
	w, _, err := st.Enter(ctx, c.ID, u)
	if err != nil {
		t.Fatal(err)
	}
	if w, _ = st.Participation(ctx, w.ID); w.SecretMatches("") || w.SecretMatches(secret) {
		t.Fatal("web participation matched a secret")
	}
}

func TestSummariesAreAppendedConditionally(t *testing.T) {
	st := open(t)
	ctx := context.Background()
	u, _ := st.UpsertUser(ctx, Identity{Issuer: "i", Subject: "s"})
	c, _ := st.CreateChat(ctx, u.ID, "t", "", time.Hour)
	p, _, _ := st.Join(ctx, c.ID, u.ID, "x") // sequence 1
	for i := range 3 {
		st.SendText(ctx, p, fmt.Sprint(i), "") // sequences 2-4
	}
	if _, err := st.LatestSummary(ctx, c.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("latest = %v, want none", err)
	}

	s1, err := st.AppendSummary(ctx, c.ID, 0, 2, "one", "m")
	if err != nil || s1.Revision != 1 || s1.CoversThrough != 2 {
		t.Fatal(s1, err)
	}
	// A summary built on a stale revision is refused.
	if _, err := st.AppendSummary(ctx, c.ID, 0, 3, "stale", "m"); !errors.Is(err, ErrSummaryConflict) {
		t.Fatalf("stale = %v", err)
	}
	// Coverage moves forward and stays within the messages.
	for _, covers := range []int64{2, 5} {
		if _, err := st.AppendSummary(ctx, c.ID, 1, covers, "bad", "m"); !errors.Is(err, ErrInvalid) {
			t.Fatalf("covers %d = %v", covers, err)
		}
	}
	if _, err := st.AppendSummary(ctx, c.ID, 1, 4, "two", "m"); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.LatestSummary(ctx, c.ID); got.Revision != 2 || got.Content != "two" || got.CoversThrough != 4 {
		t.Fatalf("latest = %+v", got)
	}
	if n, _ := st.CountText(ctx, c.ID, 2); n != 2 {
		t.Fatalf("text after 2 = %d", n)
	}

	// Destruction deletes the summaries and refuses new ones.
	if _, err := st.DestroyChat(ctx, c.ID, DestroyByCreator); err != nil {
		t.Fatal(err)
	}
	var n int
	st.db.QueryRow(`SELECT count(*) FROM summaries`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d summaries survived destruction", n)
	}
	if _, err := st.AppendSummary(ctx, c.ID, 0, 1, "late", "m"); !errors.Is(err, ErrChatDestroyed) {
		t.Fatalf("late = %v", err)
	}
}
