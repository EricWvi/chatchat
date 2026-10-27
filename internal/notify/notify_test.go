package notify

import "testing"

func TestWatcherCollectsChangesOfItsChat(t *testing.T) {
	h := NewHub()
	w := h.Watch("a")
	other := h.Watch("b")
	h.Notify("a", Messages)
	h.Notify("a", Participants)
	select {
	case <-w.C():
	default:
		t.Fatal("no signal")
	}
	if got := w.Take(); got != Messages|Participants {
		t.Fatalf("changes = %b", got)
	}
	if got := w.Take(); got != 0 {
		t.Fatalf("changes after take = %b", got)
	}
	select {
	case <-other.C():
		t.Fatal("other chat was signalled")
	default:
	}
	w.Stop()
	other.Stop()
	h.Notify("a", Messages) // no watchers left; must not block
	if len(h.watchers) != 0 {
		t.Fatalf("watchers = %v", h.watchers)
	}
}

func TestVersionCountsSummaryAndParticipantChanges(t *testing.T) {
	h := NewHub()
	v0 := h.Version("a")
	h.Notify("a", Messages)
	h.Notify("b", Summary)
	if v := h.Version("a"); v != v0 || h.Diff(v0, v) != 0 {
		t.Fatalf("messages or another chat changed the version: %s → %s", v0, v)
	}
	h.Notify("a", Participants)
	v1 := h.Version("a")
	if got := h.Diff(v0, v1); got != Participants {
		t.Fatalf("diff = %b", got)
	}
	h.Notify("a", Summary|Participants)
	if got := h.Diff(v1, h.Version("a")); got != Summary|Participants {
		t.Fatalf("diff = %b", got)
	}
	if got := h.Diff(Version("x.0.0"), v1); got != Summary|Participants {
		t.Fatalf("foreign version diff = %b", got)
	}
	h.Notify("a", Destroyed)
	if len(h.counts) != 1 {
		t.Fatalf("counts of a destroyed chat kept: %v", h.counts)
	}
}
