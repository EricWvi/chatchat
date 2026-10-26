// Package notify wakes long polls when something in a chat changes. The
// server runs as a single instance, so this lives in memory.
package notify

import "sync"

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

// Hub fans changes out to the watchers of each chat.
type Hub struct {
	mu       sync.Mutex
	watchers map[string]map[*Watcher]struct{}
}

// NewHub returns an empty hub.
func NewHub() *Hub {
	return &Hub{watchers: map[string]map[*Watcher]struct{}{}}
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

// Notify records changes for every watcher of the chat.
func (h *Hub) Notify(chatID string, c Change) {
	h.mu.Lock()
	defer h.mu.Unlock()
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
