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
