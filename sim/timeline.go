package sim

import (
	"container/heap"
	"time"
)

// Timeline is a generic event-driven clock. It has no knowledge of processors,
// peripherals or power models, so new device types can reuse it unchanged.
type Timeline struct {
	now   time.Duration
	items timelineHeap
	seq   uint64
}

type timelineEvent struct {
	at  time.Duration
	seq uint64
	tag string
	fn  func(now time.Duration)
}

type timelineHeap []*timelineEvent

func (h timelineHeap) Len() int { return len(h) }

func (h timelineHeap) Less(i, j int) bool {
	if h[i].at != h[j].at {
		return h[i].at < h[j].at
	}
	return h[i].seq < h[j].seq
}

func (h timelineHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }

func (h *timelineHeap) Push(x any) { *h = append(*h, x.(*timelineEvent)) }

func (h *timelineHeap) Pop() any {
	old := *h
	n := len(old)
	ev := old[n-1]
	*h = old[:n-1]
	return ev
}

// Now returns the current simulation time.
func (tl *Timeline) Now() time.Duration { return tl.now }

// ScheduleAt registers fn to run at time at. The returned function cancels the
// event if it has not fired yet.
func (tl *Timeline) ScheduleAt(at time.Duration, tag string, fn func(now time.Duration)) (cancel func()) {
	tl.seq++
	ev := &timelineEvent{at: at, seq: tl.seq, tag: tag, fn: fn}
	heap.Push(&tl.items, ev)
	cancelled := false
	return func() {
		if cancelled {
			return
		}
		cancelled = true
		ev.fn = nil
	}
}

// ScheduleAfter registers fn after d elapses.
func (tl *Timeline) ScheduleAfter(d time.Duration, tag string, fn func(now time.Duration)) func() {
	return tl.ScheduleAt(tl.now+d, tag, fn)
}

// Run processes every event scheduled at or before until, in time order.
func (tl *Timeline) Run(until time.Duration) {
	for tl.items.Len() > 0 {
		ev := tl.items[0]
		if ev.at > until {
			break
		}
		heap.Pop(&tl.items)
		tl.now = ev.at
		if ev.fn != nil {
			ev.fn(ev.at)
		}
	}
	tl.now = until
}
