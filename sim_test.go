package main

import (
	"math"
	"testing"
	"time"
)

func testTimeline() *Timeline {
	timeline := NewTimeline(PowerProfile{
		ActiveEnergyPerMillisecond: 2_000,
		SleepEnergyPerMillisecond:  100,
		WakeDuration:               2 * time.Millisecond,
		WakeEnergyNanos:            12_000,
	})
	timeline.AddPeripheral("sensor", 1_500)
	timeline.AddPeripheral("radio", 4_000)
	return timeline
}

func TestSnapshotContinuesScenario(t *testing.T) {
	timeline := testTimeline()
	scheduleScenario(t, timeline)
	if err := timeline.SleepUntil(40*time.Millisecond, "sensor", "radio"); err != nil {
		t.Fatal(err)
	}
	if err := timeline.RunUntil(47 * time.Millisecond); err != nil {
		t.Fatal(err)
	}

	snapshot, err := timeline.SaveSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := LoadSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := restored.SleepUntil(70*time.Millisecond, "sensor", "radio"); err != nil {
		t.Fatal(err)
	}
	if err := restored.RunUntil(77 * time.Millisecond); err != nil {
		t.Fatal(err)
	}

	if restored.Now != 77*time.Millisecond {
		t.Fatalf("time = %s", restored.Now)
	}
	if restored.CPUWork != 8*time.Millisecond {
		t.Fatalf("CPU work = %s", restored.CPUWork)
	}
	if restored.TransferDuration != 14*time.Millisecond {
		t.Fatalf("transfer work = %s", restored.TransferDuration)
	}
	if restored.RecoveryCount != 2 {
		t.Fatalf("recoveries = %d", restored.RecoveryCount)
	}
	wantReasons := []string{"periodic-sample", "external-burst", "periodic-sample"}
	if len(restored.WakeReasons) != len(wantReasons) {
		t.Fatalf("wake reasons = %v", restored.WakeReasons)
	}
	for i := range wantReasons {
		if restored.WakeReasons[i] != wantReasons[i] {
			t.Fatalf("wake reasons = %v", restored.WakeReasons)
		}
	}
	if math.Abs(restored.EnergyNanos-85_300) > 0.001 {
		t.Fatalf("energy = %.3f nJ, want 85300 nJ", restored.EnergyNanos)
	}
	if restored.ActiveDuration != 10*time.Millisecond || restored.SleepDuration != 63*time.Millisecond || restored.WakeDuration != 4*time.Millisecond {
		t.Fatalf("active=%s sleep=%s wake=%s", restored.ActiveDuration, restored.SleepDuration, restored.WakeDuration)
	}
}

func TestRejectsPowerOffDuringTransfer(t *testing.T) {
	timeline := testTimeline()
	if err := timeline.BeginTransfer("sensor", 5*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	err := timeline.SleepUntil(10*time.Millisecond, "sensor")
	if err == nil {
		t.Fatal("expected unfinished transfer to block power off")
	}
	if timeline.CPUState != CPUActive || !timeline.Peripherals["sensor"].On {
		t.Fatal("rejected sleep changed processor or peripheral state")
	}
}

func TestWakeRecoveryHasDurationAndEnergy(t *testing.T) {
	timeline := testTimeline()
	if err := timeline.SleepUntil(1 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := timeline.RunUntil(3 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if timeline.RecoveryCount != 1 || timeline.WakeDuration != 2*time.Millisecond {
		t.Fatalf("recovery count=%d duration=%s", timeline.RecoveryCount, timeline.WakeDuration)
	}
	if timeline.EnergyNanos <= 12_000 {
		t.Fatalf("wake energy was not charged: %.3f nJ", timeline.EnergyNanos)
	}
}

func scheduleScenario(t *testing.T, timeline *Timeline) {
	t.Helper()
	events := []Event{
		{At: 40 * time.Millisecond, Type: EventSample, Reason: "periodic-sample", Peripheral: "sensor", CPUWork: 3 * time.Millisecond, TransferDuration: 5 * time.Millisecond, PowerOff: true},
		{At: 40 * time.Millisecond, Type: EventBurst, Reason: "external-burst", Peripheral: "radio", CPUWork: 2 * time.Millisecond, TransferDuration: 4 * time.Millisecond, PowerOff: true},
		{At: 70 * time.Millisecond, Type: EventSample, Reason: "periodic-sample", Peripheral: "sensor", CPUWork: 3 * time.Millisecond, TransferDuration: 5 * time.Millisecond, PowerOff: true},
	}
	for _, event := range events {
		if err := timeline.Schedule(event); err != nil {
			t.Fatal(err)
		}
	}
}

func domainTimeline(t *testing.T) *Timeline {
	t.Helper()
	timeline := testTimeline()
	timeline.AddDomain("analog", 200)
	timeline.AddDomain("rf", 300)
	timeline.AddDomain("io", 100)
	if err := timeline.AddPeripheralToDomain("adc", "analog", 1_500); err != nil {
		t.Fatal(err)
	}
	if err := timeline.AddPeripheralToDomain("radio", "rf", 4_000, "adc"); err != nil {
		t.Fatal(err)
	}
	if err := timeline.AddPeripheralToDomain("display", "io", 800); err != nil {
		t.Fatal(err)
	}
	return timeline
}

func TestDependentTransferBlocksDomainPowerOff(t *testing.T) {
	timeline := domainTimeline(t)
	if err := timeline.BeginTransfer("radio", 10*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	// radio 依赖 analog 域中的 adc，传输未结束时关闭 analog 必须受阻。
	if err := timeline.SleepUntil(20*time.Millisecond, "analog"); err == nil {
		t.Fatal("expected dependent transfer to block analog domain power off")
	}
	if timeline.Domains["analog"].On == false {
		t.Fatal("blocked sleep turned the analog domain off")
	}
	// 独立的 io 域不受影响。
	if err := timeline.SleepUntil(20*time.Millisecond, "io"); err != nil {
		t.Fatalf("independent domain should power off: %v", err)
	}
	if timeline.Domains["io"].On {
		t.Fatal("io domain should be off")
	}
	if !timeline.Domains["analog"].On || !timeline.Domains["rf"].On {
		t.Fatal("unrelated domains must stay on")
	}
}

func TestEventWakesDependencyChain(t *testing.T) {
	timeline := domainTimeline(t)
	if err := timeline.Schedule(Event{At: 5 * time.Millisecond, Type: EventBurst, Reason: "burst", Peripheral: "radio", CPUWork: time.Millisecond, TransferDuration: 2 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	if err := timeline.SleepUntil(5*time.Millisecond, "analog", "rf", "io"); err != nil {
		t.Fatal(err)
	}
	if err := timeline.RunUntil(10 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if !timeline.Peripherals["radio"].On || !timeline.Peripherals["adc"].On {
		t.Fatal("event must power on the peripheral and its dependencies")
	}
	if !timeline.Domains["analog"].On || !timeline.Domains["rf"].On {
		t.Fatal("event must power on the domains of the dependency chain")
	}
	if timeline.Domains["io"].On {
		t.Fatal("unrelated io domain must stay off")
	}
}

func TestSnapshotDuringWakeResumes(t *testing.T) {
	run := func(snapshotAt time.Duration, restore bool) *Timeline {
		timeline := testTimeline()
		if err := timeline.SleepUntil(10 * time.Millisecond); err != nil {
			t.Fatal(err)
		}
		if err := timeline.RunUntil(snapshotAt); err != nil {
			t.Fatal(err)
		}
		if restore {
			data, err := timeline.SaveSnapshot()
			if err != nil {
				t.Fatal(err)
			}
			timeline, err = LoadSnapshot(data)
			if err != nil {
				t.Fatal(err)
			}
			if timeline.CPUState != CPUWaking {
				t.Fatalf("restored state jumped to %s instead of resuming the wake", timeline.CPUState)
			}
		}
		if err := timeline.RunUntil(20 * time.Millisecond); err != nil {
			t.Fatal(err)
		}
		return timeline
	}
	// 唤醒 2ms，11ms 时正处于 waking 途中。
	restored := run(11*time.Millisecond, true)
	direct := run(20*time.Millisecond, false)
	if restored.CPUState != CPUActive || restored.RecoveryCount != 1 {
		t.Fatalf("state=%s recoveries=%d", restored.CPUState, restored.RecoveryCount)
	}
	if restored.WakeDuration != direct.WakeDuration || restored.EnergyNanos != direct.EnergyNanos || restored.Now != direct.Now {
		t.Fatalf("restored=%s/%.1f/%s direct=%s/%.1f/%s",
			restored.WakeDuration, restored.EnergyNanos, restored.Now,
			direct.WakeDuration, direct.EnergyNanos, direct.Now)
	}
}

func TestInsufficientSupplyStretchesTime(t *testing.T) {
	timeline := NewTimeline(PowerProfile{
		ActiveEnergyPerMillisecond: 2_000,
		SleepEnergyPerMillisecond:  100,
		WakeDuration:               2 * time.Millisecond,
		WakeEnergyNanos:            12_000,
		SupplyEnergyPerMillisecond: 1_000,
	})
	if err := timeline.RunUntil(4 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	// 活跃功耗 2000nJ/ms 超过供给 1000nJ/ms：4ms 的工作拉长到 8ms。
	if timeline.Now != 8*time.Millisecond {
		t.Fatalf("time = %s", timeline.Now)
	}
	if timeline.WaitDuration != 4*time.Millisecond {
		t.Fatalf("wait = %s", timeline.WaitDuration)
	}
	// 能量守恒：供给速率 x 拉长时间 = 需求速率 x 工作时间。
	if math.Abs(timeline.EnergyNanos-8_000) > 0.001 {
		t.Fatalf("energy = %.3f nJ, want 8000 nJ", timeline.EnergyNanos)
	}
	if timeline.ActiveDuration != 4*time.Millisecond {
		t.Fatalf("active work = %s", timeline.ActiveDuration)
	}
}
