package sim

import "time"

// BurstEvent is an asynchronous wake source at an absolute simulation time.
type BurstEvent struct {
	At     time.Duration
	Reason string
}

// ScenarioConfig describes a periodic sampling run with burst events.
type ScenarioConfig struct {
	Period      time.Duration
	Horizon     time.Duration
	Bursts      []BurstEvent
	FirstSample time.Duration
}

// Snapshot is a portable device state taken while the processor is asleep and
// no transfer is pending. Replaying from a snapshot must reproduce the
// uninterrupted run exactly.
type Snapshot struct {
	At            time.Duration
	EnergyPJ      int64
	Workload      int
	Recoveries    int
	SamplesSeen   int
	BurstsPending []BurstEvent
	Cfg           ScenarioConfig
	DevCfg        DeviceConfig
}

// Scenario drives a Device: periodic timer wake-ups plus burst events.
type Scenario struct {
	dev  *Device
	cfg  ScenarioConfig
	sent int // number of periodic samples scheduled
	bi   int // next burst index
}

func NewScenario(dev *Device, cfg ScenarioConfig) *Scenario {
	return &Scenario{dev: dev, cfg: cfg}
}

// schedule enqueues the next timer tick and all bursts at or before the next
// tick, then advances to upto. Repeated short steps keep event scheduling
// deterministic and make snapshot replay trivial.
func (s *Scenario) schedule(upto time.Duration) {
	for {
		nextSample := s.cfg.FirstSample + time.Duration(s.sent)*s.cfg.Period
		var nextBurst time.Duration
		haveBurst := false
		if s.bi < len(s.cfg.Bursts) {
			nextBurst = s.cfg.Bursts[s.bi].At
			haveBurst = true
		}
		if nextSample > upto && (!haveBurst || nextBurst > upto) {
			return
		}
		if haveBurst && nextBurst <= nextSample && nextBurst <= upto {
			b := s.cfg.Bursts[s.bi]
			s.bi++
			reason := b.Reason
			s.dev.TL.ScheduleAt(b.At, "burst", func(_ time.Duration) {
				s.dev.Arbiter.Wake(b.At, reason)
			})
			continue
		}
		if nextSample <= upto {
			n := s.sent
			s.sent++
			s.dev.TL.ScheduleAt(nextSample, "timer", func(_ time.Duration) {
				_ = n
				s.dev.Arbiter.Wake(nextSample, ReasonTimer)
			})
		}
	}
}

// Run advances the simulation to t.
func (s *Scenario) Run(t time.Duration) {
	s.schedule(t)
	s.dev.TL.Run(t)
}

// Finish runs through the horizon and closes the energy account.
func (s *Scenario) Finish() {
	s.Run(s.cfg.Horizon)
	s.dev.Flush(s.cfg.Horizon)
}

// Savepoint is valid only while asleep with no transfer in flight, which is the
// stable point between a completed burst and the next scheduled wake.
type Savepoint struct {
	At       time.Duration
	Snapshot Snapshot
}

// TakeSnapshot records resumable state. It must be called at a stable time.
func (s *Scenario) TakeSnapshot(at time.Duration) Snapshot {
	s.Run(at)
	s.dev.Meter.Commit(at)
	if s.dev.Proc.State() != ProcAsleep {
		panic("sim: snapshot requires the processor to be asleep")
	}
	if s.dev.Sensor.ActiveTransfers() != 0 || s.dev.Radio.ActiveTransfers() != 0 {
		panic("sim: snapshot requires no active transfers")
	}
	pending := append([]BurstEvent(nil), s.cfg.Bursts[s.bi:]...)
	return Snapshot{
		At:            at,
		EnergyPJ:      s.dev.Meter.TotalPicoJoules(),
		Workload:      s.dev.Workload(),
		Recoveries:    s.dev.Recoveries(),
		SamplesSeen:   s.sent,
		BurstsPending: pending,
		Cfg:           s.cfg,
		DevCfg:        s.dev.Config(),
	}
}

// Restore builds a fresh timeline/device at the snapshot and returns a scenario
// ready to continue. Energy, work and recovery counters carry over.
func Restore(snap Snapshot) (*Scenario, *Device) {
	dev := NewDevice(snap.DevCfg)
	// Drop the freshly-constructed device's own power-up sleep entry; the
	// snapshot already finds the processor asleep at snap.At.
	dev.TL.items = nil
	dev.TL.now = snap.At
	dev.Meter.last = snap.At
	dev.Meter.totalPJ = snap.EnergyPJ
	dev.workload = snap.Workload
	dev.recoveries = snap.Recoveries
	dev.Proc.state = ProcAsleep
	dev.Meter.Commit(snap.At)

	cfg := snap.Cfg
	cfg.Bursts = snap.BurstsPending
	s := NewScenario(dev, cfg)
	s.sent = snap.SamplesSeen
	s.bi = 0
	return s, dev
}
