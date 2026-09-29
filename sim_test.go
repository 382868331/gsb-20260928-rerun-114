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
