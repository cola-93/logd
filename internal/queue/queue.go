package queue

import (
	"context"
	"encoding/json"
	"sync"
	"time"
)

type Event struct {
	EventID        string
	EventTime      time.Time
	Level          string
	ProjectKey     string
	NodeKey        string
	RequestIP      string
	Member         string
	SessionID      string
	RequestMethod  string
	RequestURL     string
	RequestHeaders json.RawMessage
	RequestParams  json.RawMessage
	ErrorScene     string
	ErrorMessage   string
	ErrorFile      string
	ErrorLine      int
	ErrorStack     string
}

type Queue struct {
	mu       sync.Mutex
	events   []Event
	capacity int
	closed   bool
	signal   chan struct{}
}

func New(capacity int) *Queue {
	return &Queue{
		capacity: capacity,
		signal:   make(chan struct{}, 1),
	}
}

func (q *Queue) Enqueue(events []Event) bool {
	q.mu.Lock()
	if q.closed || len(q.events)+len(events) > q.capacity {
		q.mu.Unlock()
		return false
	}
	q.events = append(q.events, events...)
	q.mu.Unlock()

	select {
	case q.signal <- struct{}{}:
	default:
	}
	return true
}

func (q *Queue) Depth() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.events)
}

func (q *Queue) Capacity() int {
	return q.capacity
}

func (q *Queue) Close() {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()

	select {
	case q.signal <- struct{}{}:
	default:
	}
}

func (q *Queue) ReadBatch(
	ctx context.Context,
	max int,
	flushInterval time.Duration,
) ([]Event, bool) {
	for {
		q.mu.Lock()
		if q.closed {
			batch := q.takeLocked(max)
			q.mu.Unlock()
			return batch, len(batch) > 0
		}
		if len(q.events) >= max {
			batch := q.takeLocked(max)
			q.mu.Unlock()
			return batch, true
		}
		if len(q.events) == 0 {
			q.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, false
			case <-q.signal:
			}
			continue
		}
		q.mu.Unlock()

		timer := time.NewTimer(flushInterval)
		for {
			select {
			case <-ctx.Done():
				stopTimer(timer)
				return nil, false
			case <-q.signal:
				q.mu.Lock()
				if q.closed || len(q.events) >= max {
					batch := q.takeLocked(max)
					q.mu.Unlock()
					stopTimer(timer)
					return batch, len(batch) > 0
				}
				q.mu.Unlock()
			case <-timer.C:
				q.mu.Lock()
				batch := q.takeLocked(max)
				q.mu.Unlock()
				if len(batch) > 0 {
					return batch, true
				}
			}
		}
	}
}

func (q *Queue) takeLocked(max int) []Event {
	if len(q.events) == 0 {
		return nil
	}
	size := min(max, len(q.events))
	batch := append([]Event(nil), q.events[:size]...)
	q.events = q.events[size:]
	if len(q.events) == 0 {
		q.events = nil
	}
	return batch
}

func stopTimer(timer *time.Timer) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
}
