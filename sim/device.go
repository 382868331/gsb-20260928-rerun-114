package sim

import "time"

// Device is a reusable low-power node built on the generic timeline. New
// peripherals or wake sources plug in without changing timeline or meter.
type Device struct {
	TL      *Timeline
	Meter   *Meter
	Proc    *Processor
	Sensor  *Peripheral
	Radio   *Peripheral
	Arbiter *WakeArbiter

	// jobs is outstanding work in the current wake session, including transfers.
	jobs int

	// sessionReasons keeps every reason served by the current session, in order.
	sessionReasons []string

	workload   int
	recoveries int

	cfg DeviceConfig
}

type DeviceConfig struct {
	ProcPower  ProcPower
	ProcTiming ProcTimings
	SensorPow  PeriphPower
	RadioPow   PeriphPower
}

func DefaultConfig() DeviceConfig {
	return DeviceConfig{
		ProcPower:  ProcPower{Active: 20, EnteringSleep: 10, Asleep: 1, Waking: 15},
		ProcTiming: ProcTimings{SleepEntry: 200 * time.Microsecond, Wakeup: 500 * time.Microsecond},
		SensorPow:  PeriphPower{Off: 0, On: 3},
		RadioPow:   PeriphPower{Off: 0, On: 30},
	}
}

func NewDevice(cfg DeviceConfig) *Device {
	tl := &Timeline{}
	meter := NewMeter(0)
	proc := NewProcessor(tl, meter, cfg.ProcPower, cfg.ProcTiming)
	sensor := NewPeripheral("sensor", meter, cfg.SensorPow)
	radio := NewPeripheral("radio", meter, cfg.RadioPow)
	arb := NewWakeArbiter(proc)

	d := &Device{
		TL: tl, Meter: meter, Proc: proc, cfg: cfg,
		Sensor: sensor, Radio: radio, Arbiter: arb,
	}
	proc.SetHandlers(d.beginSession, nil)
	proc.SetLiveWake(d.addLiveJob)
	// The device powers up and immediately rests until the first wake source.
	proc.RequestSleep()
	return d
}

func (d *Device) Workload() int            { return d.workload }
func (d *Device) Recoveries() int          { return d.recoveries }
func (d *Device) Segments() []PowerSegment { return d.Meter.Segments() }
func (d *Device) SessionReasons() []string { return append([]string(nil), d.sessionReasons...) }
func (d *Device) Config() DeviceConfig     { return d.cfg }

// Flush closes the open energy segment at the end of a run.
func (d *Device) Flush(now time.Duration) { d.Meter.Commit(now) }

// beginSession serves every reason delivered by a single recovery. Multiple
// reasons therefore share one wake-up but each becomes a unit of work.
func (d *Device) beginSession(now time.Duration, reasons []string) {
	d.recoveries++
	d.sessionReasons = append([]string(nil), reasons...)
	d.Sensor.On(now)
	d.Radio.On(now)
	for _, reason := range reasons {
		d.dispatch(now, reason)
	}
}

// addLiveJob serves a reason arriving while active: no second recovery, but the
// reason is retained and still produces a unit of work.
func (d *Device) addLiveJob(reason string) {
	d.sessionReasons = append(d.sessionReasons, reason)
	now := d.TL.Now()
	d.Sensor.On(now)
	d.Radio.On(now)
	d.dispatch(now, reason)
}

// dispatch maps one reason to its transfer chain. Each chain ends in
// completeJob, so jobs outlive their last transfer by construction.
func (d *Device) dispatch(at time.Duration, reason string) {
	if reason == ReasonTimer {
		d.sensorJob(at)
		return
	}
	d.burstJob(at)
}

const (
	convertDuration  = 2 * time.Millisecond
	transmitDuration = 1 * time.Millisecond
	burstDuration    = 500 * time.Microsecond
	ReasonTimer      = "timer.sample"
)

func (d *Device) sensorJob(at time.Duration) {
	d.jobs++
	d.Sensor.StartTransfer()
	d.TL.ScheduleAt(at+convertDuration, "sample-convert", func(t time.Duration) {
		d.Sensor.FinishTransfer(t)
		d.Radio.StartTransfer()
		d.TL.ScheduleAfter(transmitDuration, "sample-tx", func(t2 time.Duration) {
			d.Radio.FinishTransfer(t2)
			d.completeJob(t2)
		})
	})
}

func (d *Device) burstJob(at time.Duration) {
	d.jobs++
	d.Radio.StartTransfer()
	d.TL.ScheduleAt(at+burstDuration, "burst-tx", func(t time.Duration) {
		d.Radio.FinishTransfer(t)
		d.completeJob(t)
	})
}

// completeJob finishes one unit of work. Peripherals are released only after
// the final transfer has closed; RequestOff during a transfer is deferred by
// the peripheral itself.
func (d *Device) completeJob(now time.Duration) {
	d.workload++
	d.jobs--
	if d.jobs > 0 {
		return
	}
	d.Radio.RequestOff(now)
	d.Sensor.RequestOff(now)
	if len(d.Proc.PendingReasons()) == 0 && d.Proc.IsAvailable() {
		d.Proc.RequestSleep()
	}
}
