package sim

import "time"

type ProcState int

const (
	ProcActive ProcState = iota
	ProcEnteringSleep
	ProcAsleep
	ProcWaking
)

func (s ProcState) String() string {
	switch s {
	case ProcActive:
		return "active"
	case ProcEnteringSleep:
		return "entering-sleep"
	case ProcAsleep:
		return "asleep"
	case ProcWaking:
		return "waking"
	default:
		return "unknown"
	}
}

// ProcPower is the processor draw in milliwatts per state.
type ProcPower struct {
	Active        int64
	EnteringSleep int64
	Asleep        int64
	Waking        int64
}

// ProcTimings are the sleep-entry and recovery latencies.
type ProcTimings struct {
	SleepEntry time.Duration
	Wakeup     time.Duration
}

// Processor is a four-state machine: active -> entering-sleep -> asleep ->
// waking -> active. Every transition consumes modeled time and power.
type Processor struct {
	tl     *Timeline
	meter  *Meter
	power  ProcPower
	timing ProcTimings

	state ProcState

	// pendingWake reasons accumulate during entry/sleep/waking and are handed
	// over as one batch when the processor becomes active.
	pendingWake []string

	onActive      func(now time.Duration, reasons []string)
	onAsleep      func(now time.Duration)
	onStateChange func(now time.Duration, state ProcState)
	onLiveWake    func(reason string)
}

func NewProcessor(tl *Timeline, meter *Meter, power ProcPower, timing ProcTimings) *Processor {
	p := &Processor{tl: tl, meter: meter, power: power, timing: timing, state: ProcActive}
	meter.sources = append(meter.sources, p)
	return p
}

func (p *Processor) PowerMilliWatts() int64 {
	switch p.state {
	case ProcEnteringSleep:
		return p.power.EnteringSleep
	case ProcAsleep:
		return p.power.Asleep
	case ProcWaking:
		return p.power.Waking
	default:
		return p.power.Active
	}
}

func (p *Processor) State() ProcState         { return p.state }
func (p *Processor) IsAvailable() bool        { return p.state == ProcActive }
func (p *Processor) PendingReasons() []string { return append([]string(nil), p.pendingWake...) }

func (p *Processor) SetHandlers(onActive func(now time.Duration, reasons []string), onAsleep func(now time.Duration)) {
	p.onActive = onActive
	p.onAsleep = onAsleep
}

func (p *Processor) SetStateChange(fn func(now time.Duration, state ProcState)) {
	p.onStateChange = fn
}

func (p *Processor) SetLiveWake(fn func(reason string)) { p.onLiveWake = fn }

// RequestSleep starts the sleep-entry transition. Its latency and draw are
// booked like any other state. A reason arriving during entry turns the rest of
// the transition into recovery.
func (p *Processor) RequestSleep() {
	if p.state != ProcActive {
		return
	}
	p.meter.Commit(p.tl.Now())
	p.state = ProcEnteringSleep
	p.fireStateChange(p.tl.Now())
	p.tl.ScheduleAfter(p.timing.SleepEntry, "proc-sleep-entry", func(now time.Duration) {
		p.meter.Commit(now)
		p.state = ProcAsleep
		p.fireStateChange(now)
		if p.onAsleep != nil {
			p.onAsleep(now)
		}
	})
}

// RequestWake records a reason and starts one recovery if the processor is not
// active. Reasons arriving during entry, sleep or recovery never start a second
// recovery and are never dropped.
func (p *Processor) RequestWake(reason string) {
	if p.state == ProcActive {
		if p.onLiveWake != nil {
			p.onLiveWake(reason)
		}
		return
	}
	p.pendingWake = append(p.pendingWake, reason)
	if p.state == ProcWaking {
		return
	}
	p.meter.Commit(p.tl.Now())
	p.state = ProcWaking
	p.fireStateChange(p.tl.Now())
	p.tl.ScheduleAfter(p.timing.Wakeup, "proc-wakeup", func(now time.Duration) {
		p.meter.Commit(now)
		p.state = ProcActive
		p.fireStateChange(now)
		reasons := p.pendingWake
		p.pendingWake = nil
		if p.onActive != nil {
			p.onActive(now, reasons)
		}
	})
}

func (p *Processor) fireStateChange(now time.Duration) {
	if p.onStateChange != nil {
		p.onStateChange(now, p.state)
	}
}
