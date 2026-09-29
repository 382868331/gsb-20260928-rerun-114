package sim

import "time"

type PeriphState int

const (
	PeriphOff PeriphState = iota
	PeriphOn
)

func (s PeriphState) String() string {
	if s == PeriphOn {
		return "on"
	}
	return "off"
}

type PeriphPower struct {
	Off int64
	On  int64
}

// Peripheral models a clocked peripheral such as a sensor or radio. It can be
// powered down only when no transfer is active; RequestOff during a transfer is
// remembered and applied once the transfer completes.
type Peripheral struct {
	name  string
	meter *Meter
	power PeriphPower

	state       PeriphState
	transfers   int
	offPending  bool
	onOffChange func(now time.Duration, state PeriphState)
}

func NewPeripheral(name string, meter *Meter, power PeriphPower) *Peripheral {
	ph := &Peripheral{name: name, meter: meter, power: power, state: PeriphOff}
	meter.sources = append(meter.sources, ph)
	return ph
}

func (ph *Peripheral) Name() string         { return ph.name }
func (ph *Peripheral) State() PeriphState   { return ph.state }
func (ph *Peripheral) IsOn() bool           { return ph.state == PeriphOn }
func (ph *Peripheral) ActiveTransfers() int { return ph.transfers }
func (ph *Peripheral) OffPending() bool     { return ph.offPending }

func (ph *Peripheral) SetOnOffChange(fn func(now time.Duration, state PeriphState)) {
	ph.onOffChange = fn
}

func (ph *Peripheral) PowerMilliWatts() int64 {
	if ph.state == PeriphOn {
		return ph.power.On
	}
	return ph.power.Off
}

func (ph *Peripheral) On(now time.Duration) {
	if ph.state == PeriphOn {
		ph.offPending = false
		return
	}
	ph.meter.Commit(now)
	ph.state = PeriphOn
	ph.offPending = false
}

// RequestOff powers down immediately when idle. During transfers it only marks
// the intent; the peripheral stays on and FinishTransfer performs the shutdown.
func (ph *Peripheral) RequestOff(now time.Duration) {
	if ph.state == PeriphOff {
		ph.offPending = false
		return
	}
	if ph.transfers == 0 {
		ph.shutdown(now)
		return
	}
	ph.offPending = true
}

// StartTransfer marks work in flight. The caller is responsible for keeping the
// peripheral powered while using it.
func (ph *Peripheral) StartTransfer() {
	if ph.state != PeriphOn {
		panic("sim: cannot transfer on powered-off peripheral " + ph.name)
	}
	ph.transfers++
	ph.offPending = false
}

// FinishTransfer closes one transfer and shuts down if shutdown was requested
// while it was in flight.
func (ph *Peripheral) FinishTransfer(now time.Duration) {
	if ph.transfers <= 0 {
		panic("sim: peripheral transfer underflow on " + ph.name)
	}
	ph.transfers--
	if ph.transfers == 0 && ph.offPending {
		ph.shutdown(now)
	}
}

func (ph *Peripheral) shutdown(now time.Duration) {
	ph.meter.Commit(now)
	ph.state = PeriphOff
	ph.offPending = false
	if ph.onOffChange != nil {
		ph.onOffChange(now, ph.state)
	}
}
