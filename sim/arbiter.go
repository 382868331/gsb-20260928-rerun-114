package sim

import "time"

// WakeArbiter funnels every wake source into one processor request. It never
// deduplicates reasons by itself: repeated calls during one recovery still leave
// every recorded reason, while the processor guarantees a single recovery.
type WakeArbiter struct {
	proc *Processor
}

func NewWakeArbiter(proc *Processor) *WakeArbiter {
	return &WakeArbiter{proc: proc}
}

func (a *WakeArbiter) Wake(_ time.Duration, reason string) {
	a.proc.RequestWake(reason)
}
