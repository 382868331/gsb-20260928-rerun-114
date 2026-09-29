package sim

import "time"

// PowerSource is anything that draws power in its current state.
type PowerSource interface {
	PowerMilliWatts() int64
}

// Meter integrates power over time. Power is expressed in milliwatts and time
// in nanoseconds, so the accumulated unit is the picojoule (1 mW * 1 ns = 1 pJ).
// Integer arithmetic keeps the energy account exact and reproducible.
type Meter struct {
	sources  []PowerSource
	last     time.Duration
	totalPJ  int64
	segments []PowerSegment
}

// PowerSegment is one interval over which total draw stayed constant.
type PowerSegment struct {
	Start time.Duration
	End   time.Duration
	Power int64 // milliwatts
}

func NewMeter(start time.Duration, sources ...PowerSource) *Meter {
	return &Meter{sources: sources, last: start}
}

func (m *Meter) Sources() []PowerSource { return m.sources }

// Commit closes the current power segment at now and books its energy before a
// state change alters the draw of any source.
func (m *Meter) Commit(now time.Duration) {
	dt := now - m.last
	if dt < 0 {
		panic("sim: meter cannot rewind time")
	}
	if dt > 0 {
		var power int64
		for _, s := range m.sources {
			power += s.PowerMilliWatts()
		}
		m.totalPJ += power * int64(dt)
		m.segments = append(m.segments, PowerSegment{Start: m.last, End: now, Power: power})
	}
	m.last = now
}

func (m *Meter) TotalPicoJoules() int64 { return m.totalPJ }

func (m *Meter) TotalMicroJoules() int64 { return m.totalPJ / 1000 }

func (m *Meter) TotalJoules() float64 { return float64(m.totalPJ) / 1e12 }

func (m *Meter) Segments() []PowerSegment { return m.segments }
