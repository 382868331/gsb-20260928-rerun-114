package sim

import (
	"testing"
	"time"
)

// A transfer in flight must keep the peripheral powered even when shutdown is
// requested; the deferred shutdown applies only after FinishTransfer.
func TestPeripheralDefersOffDuringTransfer(t *testing.T) {
	meter := NewMeter(0)
	ph := NewPeripheral("p", meter, PeriphPower{Off: 0, On: 5})

	ph.On(0)
	ph.StartTransfer()
	ph.RequestOff(10 * time.Millisecond)
	if !ph.IsOn() {
		t.Fatal("peripheral powered down with a transfer in flight")
	}
	if !ph.OffPending() {
		t.Fatal("shutdown request should be remembered")
	}
	ph.FinishTransfer(20 * time.Millisecond)
	if ph.IsOn() || ph.ActiveTransfers() != 0 || ph.OffPending() {
		t.Fatal("peripheral should power down once the transfer finishes")
	}

	// Energy: 5 mW for the whole 20 ms = 100,000,000 pJ.
	meter.Commit(20 * time.Millisecond)
	if got, want := meter.TotalPicoJoules(), int64(5)*int64(20*time.Millisecond); got != want {
		t.Fatalf("energy = %d, want %d", got, want)
	}
}

func TestStartTransferOnOffPanics(t *testing.T) {
	meter := NewMeter(0)
	ph := NewPeripheral("p", meter, PeriphPower{})
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on transfer while powered off")
		}
	}()
	ph.StartTransfer()
}

// Multiple reasons raised during one recovery merge: a single recovery is
// performed but every reason is delivered and becomes work.
func TestConcurrentWakeReasonsMerge(t *testing.T) {
	tl := &Timeline{}
	meter := NewMeter(0)
	proc := NewProcessor(tl, meter, DefaultConfig().ProcPower, DefaultConfig().ProcTiming)

	delivered := [][]string{}
	proc.SetHandlers(func(_ time.Duration, reasons []string) {
		delivered = append(delivered, append([]string(nil), reasons...))
		proc.RequestSleep()
	}, nil)

	proc.RequestSleep()
	tl.Run(1 * time.Millisecond) // asleep

	// First reason starts recovery; the second arrives 100 us later, while the
	// 500 us recovery is still running.
	proc.RequestWake("gpio.line")
	tl.ScheduleAt(1*time.Millisecond+100*time.Microsecond, "b", func(_ time.Duration) {
		proc.RequestWake("radio.packet")
	})
	tl.Run(2 * time.Millisecond)

	if len(delivered) != 1 {
		t.Fatalf("expected exactly one recovery delivery, got %d", len(delivered))
	}
	if got, want := len(delivered[0]), 2; got != want {
		t.Fatalf("reasons kept = %v, want both", delivered[0])
	}
}

// A live reason during active processing needs no recovery but stays as work.
func TestLiveWakeAddsWorkWithoutRecovery(t *testing.T) {
	dev := NewDevice(DefaultConfig())

	cfg := ScenarioConfig{
		FirstSample: 10 * time.Millisecond,
		Period:      100 * time.Second,
		Horizon:     50 * time.Millisecond,
		Bursts: []BurstEvent{
			// Within the timer session: recovery at 10.5 ms, work until ~13.5 ms.
			{At: 12 * time.Millisecond, Reason: "gpio.line"},
		},
	}
	scn := NewScenario(dev, cfg)
	scn.Finish()

	if dev.Recoveries() != 1 {
		t.Fatalf("recoveries = %d, want 1 (live wake needs no recovery)", dev.Recoveries())
	}
	if dev.Workload() != 2 {
		t.Fatalf("workload = %d, want 2 (both reasons served)", dev.Workload())
	}
	if got := dev.SessionReasons(); len(got) != 2 || got[0] != ReasonTimer || got[1] != "gpio.line" {
		t.Fatalf("reasons = %v", got)
	}
}

// Full scenario: every wake recovery must charge its 500 us latency at waking
// power, and the trace-derived energy must equal the meter exactly.
func TestScenarioTimeWorkloadEnergy(t *testing.T) {
	cfg := ScenarioConfig{
		FirstSample: 10 * time.Millisecond,
		Period:      10 * time.Millisecond,
		Horizon:     100 * time.Millisecond,
		Bursts: []BurstEvent{
			{At: 10*time.Millisecond + 100*time.Microsecond, Reason: "gpio.line"},
			{At: 35*time.Millisecond + 60*time.Microsecond, Reason: "radio.packet"},
			{At: 35*time.Millisecond + 70*time.Microsecond, Reason: "gpio.line"},
		},
	}
	dev := NewDevice(DefaultConfig())
	scn := NewScenario(dev, cfg)
	scn.Finish()

	// 10 timer samples (10ms..100ms inclusive boundary: samples strictly before
	// or at horizon) + 3 bursts, but burst at 35.07ms shares a recovery with the
	// 35.06ms burst.
	t.Run("accounts", func(t *testing.T) {
		if dev.TL.Now() != cfg.Horizon {
			t.Fatalf("time = %v, want %v", dev.TL.Now(), cfg.Horizon)
		}
		// Samples at 10..90ms fully complete (the 100ms wake finishes beyond
		// the horizon); all 3 bursts complete. 9 + 3 = 12.
		if dev.Workload() != 12 {
			t.Fatalf("workload = %d, want 12", dev.Workload())
		}
		// 9 completed timer recoveries + 1 standalone burst recovery at 35.06ms.
		// The 10.1ms burst is live; the 35.07ms burst merges; the 100ms timer
		// recovery does not complete before the horizon.
		if dev.Recoveries() != 10 {
			t.Fatalf("recoveries = %d, want 10", dev.Recoveries())
		}
	})

	t.Run("energy trace matches meter", func(t *testing.T) {
		var trace int64
		var wakingTime time.Duration
		for _, seg := range dev.Segments() {
			trace += seg.Power * int64(seg.End-seg.Start)
			if seg.Power == DefaultConfig().ProcPower.Waking {
				wakingTime += seg.End - seg.Start
			}
		}
		if trace != dev.Meter.TotalPicoJoules() {
			t.Fatalf("trace energy %d != meter %d", trace, dev.Meter.TotalPicoJoules())
		}
		wantWaking := time.Duration(dev.Recoveries()) * DefaultConfig().ProcTiming.Wakeup
		if wakingTime != wantWaking {
			t.Fatalf("waking time = %v, want %v (recovery latency must be billed)", wakingTime, wantWaking)
		}
	})

	t.Run("no active transfers at end", func(t *testing.T) {
		if dev.Sensor.ActiveTransfers() != 0 || dev.Radio.ActiveTransfers() != 0 {
			t.Fatal("transfers leaked at end of run")
		}
	})
}

// Saving state mid-scenario and continuing must reproduce the uninterrupted run.
func TestSnapshotResumeParity(t *testing.T) {
	cfg := ScenarioConfig{
		FirstSample: 10 * time.Millisecond,
		Period:      10 * time.Millisecond,
		Horizon:     100 * time.Millisecond,
		Bursts: []BurstEvent{
			{At: 10*time.Millisecond + 100*time.Microsecond, Reason: "gpio.line"},
			{At: 35*time.Millisecond + 60*time.Microsecond, Reason: "radio.packet"},
		},
	}

	ref := NewDevice(DefaultConfig())
	NewScenario(ref, cfg).Finish()

	fork := NewDevice(DefaultConfig())
	scn := NewScenario(fork, cfg)
	snap := scn.TakeSnapshot(25 * time.Millisecond)
	if snap.At != 25*time.Millisecond || snap.Workload != 3 || snap.Recoveries != 2 {
		t.Fatalf("snapshot state = %+v", snap)
	}
	cont, contDev := Restore(snap)
	cont.Finish()

	if contDev.Meter.TotalPicoJoules() != ref.Meter.TotalPicoJoules() {
		t.Fatalf("energy after resume %d != reference %d",
			contDev.Meter.TotalPicoJoules(), ref.Meter.TotalPicoJoules())
	}
	if contDev.Workload() != ref.Workload() || contDev.Recoveries() != ref.Recoveries() {
		t.Fatalf("counters after resume (w=%d,r=%d) != reference (w=%d,r=%d)",
			contDev.Workload(), contDev.Recoveries(), ref.Workload(), ref.Recoveries())
	}
}

// A snapshot outside a stable sleep point must be rejected.
func TestSnapshotRequiresStableSleep(t *testing.T) {
	cfg := ScenarioConfig{
		FirstSample: 10 * time.Millisecond, Period: 10 * time.Millisecond, Horizon: 50 * time.Millisecond,
	}
	dev := NewDevice(DefaultConfig())
	scn := NewScenario(dev, cfg)
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic when snapshotting during wake activity")
		}
	}()
	_ = scn.TakeSnapshot(10 * time.Millisecond) // timer wake fires exactly now
}
