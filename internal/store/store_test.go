package store

import (
	"context"
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
