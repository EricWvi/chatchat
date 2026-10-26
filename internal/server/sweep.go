package server

import (
	"context"
	"log/slog"
	"time"

	"chatchat/internal/notify"
)

// Sweep destroys chats that idled or outlived their lifetime and tells
// watchers about participants whose lease ran out since the last sweep.
// It is called periodically; tests call it directly.
func (s *Server) Sweep(ctx context.Context) error {
	l := s.opts.Limits
	destroyed, err := s.store.DestroyDue(ctx, l.PresenceLease, l.IdleDestroyAfter)
	if err != nil {
		return err
	}
	for _, c := range destroyed {
		slog.Info("chat destroyed", "chat_id", c.ID, "reason", c.DestroyReason)
		s.summary.Forget(c.ID)
		s.hub.Notify(c.ID, notify.Destroyed)
	}

	now := s.store.Now()
	s.sweepMu.Lock()
	from := s.lastSweep
	s.lastSweep = now
	s.sweepMu.Unlock()
	if from.IsZero() {
		// At start-up nobody is watching yet.
		return nil
	}
	lapsed, err := s.store.LapsedChats(ctx, from, now, l.PresenceLease)
	if err != nil {
		return err
	}
	for _, id := range lapsed {
		s.hub.Notify(id, notify.Participants)
	}
	return nil
}

// RunSweeper sweeps every interval until ctx ends.
func (s *Server) RunSweeper(ctx context.Context, interval time.Duration) {
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		if err := s.Sweep(ctx); err != nil && ctx.Err() == nil {
			slog.Error("sweep", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
