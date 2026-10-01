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

func domainTimeline() *Timeline {
	timeline := NewTimeline(PowerProfile{
		ActiveEnergyPerMillisecond: 2_000,
		SleepEnergyPerMillisecond:  100,
		SleepEntryDuration:         1 * time.Millisecond,
		SleepEntryEnergyNanos:      6_000,
		WakeDuration:               2 * time.Millisecond,
		WakeEnergyNanos:            12_000,
	})
	timeline.AddPowerDomain("sensor-rail", 800)
	timeline.AddPowerDomain("radio-rail", 3_000)
	timeline.AddDomainPeripheral("sensor", "sensor-rail", 1_500)
	timeline.AddDomainPeripheral("radio", "radio-rail", 4_000, "sensor")
	return timeline
}

func TestPowerDomainIsolation(t *testing.T) {
	timeline := domainTimeline()
	if err := timeline.Schedule(Event{
		At: 10 * time.Millisecond, Type: EventSample, Reason: "long-sample",
		Peripheral: "sensor", CPUWork: 1 * time.Millisecond, TransferDuration: 8 * time.Millisecond,
	}); err != nil {
		t.Fatal(err)
	}
	if err := timeline.RunUntil(12 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := timeline.SleepUntil(30*time.Millisecond, "radio"); err != nil {
		t.Fatalf("independent rail should sleep: %v", err)
	}
	if err := timeline.RunUntil(14 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if timeline.Domains["radio-rail"].On {
		t.Fatal("radio-rail stayed on after sleeping")
	}
	if !timeline.Domains["sensor-rail"].On {
		t.Fatal("sensor-rail was shut down even though its transfer is still running")
	}
}

func TestWakeDependencyBlocksRailPowerOff(t *testing.T) {
	timeline := domainTimeline()
	if err := timeline.BeginTransfer("radio", 6*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	err := timeline.SleepUntil(5*time.Millisecond, "sensor")
	if err == nil {
		t.Fatal("powering off the sensor rail while radio still depends on it must be blocked")
	}
	if timeline.CPUState != CPUActive {
		t.Fatal("rejected sleep changed state")
	}
	if err := timeline.RunUntil(6 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	err = timeline.SleepUntil(20*time.Millisecond, "sensor")
	if err != nil {
		t.Fatalf("after the radio transfer completes the rail can power off: %v", err)
	}
}

func TestMidTransitionSnapshotResumes(t *testing.T) {
	timeline := NewTimeline(PowerProfile{
		ActiveEnergyPerMillisecond: 2_000,
		SleepEnergyPerMillisecond:  100,
		SleepEntryDuration:         2 * time.Millisecond,
		SleepEntryEnergyNanos:      6_000,
		WakeDuration:               2 * time.Millisecond,
		WakeEnergyNanos:            12_000,
	})
	timeline.AddDomainPeripheral("sensor", "sensor-rail", 1_500)
	if err := timeline.Schedule(Event{
		At: 10 * time.Millisecond, Type: EventSample, Reason: "periodic-sample",
		Peripheral: "sensor", CPUWork: 1 * time.Millisecond,
	}); err != nil {
		t.Fatal(err)
	}
	if err := timeline.SleepUntil(10*time.Millisecond, "sensor"); err != nil {
		t.Fatal(err)
	}
	if err := timeline.RunUntil(1 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if timeline.CPUState != CPUEnteringSleep || timeline.Now != 1*time.Millisecond {
		t.Fatalf("expected mid entry at 1ms, got state=%s now=%s", timeline.CPUState, timeline.Now)
	}

	data, err := timeline.SaveSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := LoadSnapshot(data)
	if err != nil {
		t.Fatal(err)
	}
	if restored.CPUState != CPUEnteringSleep {
		t.Fatal("snapshot jumped out of the entry transition")
	}
	if err := restored.RunUntil(15 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if restored.RecoveryCount != 1 || restored.CPUState != CPUActive {
		t.Fatalf("count=%d state=%s", restored.RecoveryCount, restored.CPUState)
	}
	if got := restored.WakeReasons; len(got) != 1 || got[0] != "periodic-sample" {
		t.Fatalf("wake reasons after mid-transition resume = %v", got)
	}
	if restored.SleepEntryDuration != 2*time.Millisecond {
		t.Fatalf("entry duration = %s", restored.SleepEntryDuration)
	}
	if restored.WakeDuration != 2*time.Millisecond {
		t.Fatalf("wake duration = %s", restored.WakeDuration)
	}
}

func TestMidWakeSnapshotDoesNotSkipRecovery(t *testing.T) {
	timeline := domainTimeline()
	if err := timeline.Schedule(Event{
		At: 10 * time.Millisecond, Type: EventSample, Reason: "periodic-sample",
		Peripheral: "sensor", CPUWork: 1 * time.Millisecond,
	}); err != nil {
		t.Fatal(err)
	}
	if err := timeline.SleepUntil(10*time.Millisecond, "sensor"); err != nil {
		t.Fatal(err)
	}
	if err := timeline.RunUntil(11 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if timeline.CPUState != CPUWaking {
		t.Fatalf("state = %s", timeline.CPUState)
	}

	data, err := timeline.SaveSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := LoadSnapshot(data)
	if err != nil {
		t.Fatal(err)
	}
	if restored.CPUState != CPUWaking {
		t.Fatal("snapshot restored as already active")
	}
	if err := restored.RunUntil(15 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if restored.CPUState != CPUActive || restored.RecoveryCount != 1 {
		t.Fatalf("state=%s recoveries=%d", restored.CPUState, restored.RecoveryCount)
	}
	if got := restored.WakeReasons; len(got) != 1 || got[0] != "periodic-sample" {
		t.Fatalf("wake reasons = %v", got)
	}
}

func TestEnergyShortageWaitsAndThenProcesses(t *testing.T) {
	timeline := NewTimelineWithBudget(PowerProfile{
		ActiveEnergyPerMillisecond: 1_000,
		SleepEnergyPerMillisecond:  100,
		WakeDuration:               time.Millisecond,
		WakeEnergyNanos:            1_000,
	}, 500, 1_000)
	timeline.AddPeripheral("sensor", 0)
	if err := timeline.Schedule(Event{
		At: 2 * time.Millisecond, Type: EventSample, Reason: "periodic-sample",
		Peripheral: "sensor", CPUWork: 2 * time.Millisecond,
	}); err != nil {
		t.Fatal(err)
	}
	if err := timeline.RunUntil(1 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := timeline.RunUntil(6 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if timeline.WaitDuration < 2*time.Millisecond {
		t.Fatalf("expected recharge wait, got %s", timeline.WaitDuration)
	}
	if timeline.CPUWork != 2*time.Millisecond {
		t.Fatalf("pending work lost after recharge, cpu work = %s", timeline.CPUWork)
	}
	if timeline.CompletedEvents["sample"] != 1 {
		t.Fatalf("event processed %d times", timeline.CompletedEvents["sample"])
	}
}
