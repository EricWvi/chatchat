// Package notify wakes long polls when something in a chat changes. The
// server runs as a single instance, so this lives in memory.
package notify

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Change is a set of things that changed in a chat.
type Change uint8

// Things that can change.
const (
	Messages Change = 1 << iota
	Summary
	Participants
	// Destroyed means the chat is gone; every watcher should stop.
	Destroyed
)

// Hub fans changes out to the watchers of each chat and counts the
// summary and participant changes of each chat, so a client can tell what
// changed while it was not watching.
type Hub struct {
	mu       sync.Mutex
	watchers map[string]map[*Watcher]struct{}
	counts   map[string]*counts
	// epoch tells versions of this process from those of an earlier one,
	// whose counts started over.
	epoch string
}

type counts struct{ summary, participants uint64 }

// NewHub returns an empty hub.
func NewHub() *Hub {
	return &Hub{
		watchers: map[string]map[*Watcher]struct{}{},
		counts:   map[string]*counts{},
		epoch:    strconv.FormatInt(time.Now().UnixNano(), 36),
	}
}

// Version is an opaque token for the summary and participant changes of a
// chat so far.
type Version string

// Version returns the chat's current version. Read it before the state it
// describes, so a change in between shows up as a newer version.
func (h *Hub) Version(chatID string) Version {
	h.mu.Lock()
	defer h.mu.Unlock()
	c := h.counts[chatID]
	if c == nil {
		c = &counts{}
	}
	return Version(fmt.Sprintf("%s.%d.%d", h.epoch, c.summary, c.participants))
}

// Diff reports what changed between two versions of a chat. A version
// that does not parse or comes from another process counts as everything
// having changed.
func (h *Hub) Diff(from, to Version) Change {
	a, b := strings.Split(string(from), "."), strings.Split(string(to), ".")
	if len(a) != 3 || len(b) != 3 || a[0] != h.epoch || b[0] != h.epoch {
		return Summary | Participants
	}
	var c Change
	if a[1] != b[1] {
		c |= Summary
	}
	if a[2] != b[2] {
		c |= Participants
	}
	return c
}

// Watcher collects the changes of one chat until it is stopped.
type Watcher struct {
	hub    *Hub
	chatID string
	c      chan struct{}

	mu      sync.Mutex
	changes Change
}

// Watch starts collecting changes to the chat. Start watching before
// reading the state to compare against, so no change falls in between.
func (h *Hub) Watch(chatID string) *Watcher {
	w := &Watcher{hub: h, chatID: chatID, c: make(chan struct{}, 1)}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.watchers[chatID] == nil {
		h.watchers[chatID] = map[*Watcher]struct{}{}
	}
	h.watchers[chatID][w] = struct{}{}
	return w
}

// Stop unregisters the watcher.
func (w *Watcher) Stop() {
	h := w.hub
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.watchers[w.chatID], w)
	if len(h.watchers[w.chatID]) == 0 {
		delete(h.watchers, w.chatID)
	}
}

// C receives a value when changes are waiting to be taken.
func (w *Watcher) C() <-chan struct{} { return w.c }

// Take returns the changes collected since the last call and clears them.
func (w *Watcher) Take() Change {
	w.mu.Lock()
	defer w.mu.Unlock()
	c := w.changes
	w.changes = 0
	return c
}

// Notify records changes for every watcher of the chat. A destroyed chat
// drops its counts.
func (h *Hub) Notify(chatID string, c Change) {
	h.mu.Lock()
	defer h.mu.Unlock()
	switch {
	case c&Destroyed != 0:
		delete(h.counts, chatID)
	case c&(Summary|Participants) != 0:
		n := h.counts[chatID]
		if n == nil {
			n = &counts{}
			h.counts[chatID] = n
		}
		if c&Summary != 0 {
			n.summary++
		}
		if c&Participants != 0 {
			n.participants++
		}
	}
	for w := range h.watchers[chatID] {
		w.mu.Lock()
		w.changes |= c
		w.mu.Unlock()
		select {
		case w.c <- struct{}{}:
		default:
		}
	}
}
