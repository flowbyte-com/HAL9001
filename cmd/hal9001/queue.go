package main

import (
	"path/filepath"

	"github.com/flowbyte-com/HAL9001/internal/queue"
)

// The operator's outbox.
//
// HAL works one turn at a time, and a turn can take a while. The
// operator is not idle in the meantime: they can write the next
// message, or change the direction of the work that is running.
//
// The queue is shared: the interface fills it and shows it, and the
// running turn asks it for a steering message at every boundary where
// the next request has not been sent yet.

// steerSource lets the running turn take a steering message out of the
// queue.
//
// It is the whole of the engine's contact with the queue. The engine
// never learns whether a turn is running and never holds a handle on
// the interface: it asks, and the answer is a message or nothing.
type steerSource struct {
	queue *queue.Queue
}

// Steer implements engine.SteerSource.
func (s steerSource) Steer() (string, bool) {
	if s.queue == nil {
		return "", false
	}
	item, ok := s.queue.TakeSteer()
	if !ok {
		return "", false
	}
	return item.Text, true
}

// queueStore returns the place the waiting messages are kept.
//
// The queue belongs to the session: a session change moves it with the
// conversation, and a message the operator wrote while HAL was working
// is still there after a restart.
func (a *app) queueStore() queue.Store {
	return queue.FileStore{Path: a.queuePath()}
}

// queuePath names the file the waiting messages are kept in.
func (a *app) queuePath() string {
	name := a.session
	if name == "" {
		return ""
	}
	return filepath.Join(a.cfg.StoreDir, name+".queue.json")
}
