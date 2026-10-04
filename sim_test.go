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

func entryTimeline() *Timeline {
	timeline := NewTimeline(PowerProfile{
		ActiveEnergyPerMillisecond: 2_000,
		SleepEnergyPerMillisecond:  100,
		WakeDuration:               2 * time.Millisecond,
		WakeEnergyNanos:            12_000,
		SleepEntryDuration:         2 * time.Millisecond,
		SleepEntryEnergyNanos:      6_000,
	})
	timeline.AddPeripheral("sensor", 1_500)
	return timeline
}

func TestSnapshotDuringSleepEntryResumes(t *testing.T) {
	run := func(restore bool) *Timeline {
		timeline := entryTimeline()
		if err := timeline.SleepUntil(20*time.Millisecond, "sensor"); err != nil {
			t.Fatal(err)
		}
		if timeline.CPUState != CPUFallingAsleep {
			t.Fatalf("state = %s, want falling-asleep", timeline.CPUState)
		}
		if timeline.SleepRemaining != 2*time.Millisecond {
			t.Fatalf("sleep remaining = %s", timeline.SleepRemaining)
		}
		// 进入休眠 2ms，1ms 时正处于过渡途中。
		if err := timeline.RunUntil(1 * time.Millisecond); err != nil {
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
			if timeline.CPUState != CPUFallingAsleep || timeline.SleepRemaining != time.Millisecond {
				t.Fatalf("restored state=%s remaining=%s", timeline.CPUState, timeline.SleepRemaining)
			}
		}
		if err := timeline.RunUntil(25 * time.Millisecond); err != nil {
			t.Fatal(err)
		}
		return timeline
	}
	restored := run(true)
	direct := run(false)
	if restored.CPUState != CPUActive || restored.Peripherals["sensor"].On {
		t.Fatalf("state=%s sensor.on=%v", restored.CPUState, restored.Peripherals["sensor"].On)
	}
	if restored.FallAsleepDuration != 2*time.Millisecond {
		t.Fatalf("sleep entry = %s", restored.FallAsleepDuration)
	}
	if restored.Now != direct.Now || restored.EnergyNanos != direct.EnergyNanos ||
		restored.SleepDuration != direct.SleepDuration || restored.WakeDuration != direct.WakeDuration {
		t.Fatalf("restored=%s/%.1f direct=%s/%.1f",
			restored.Now, restored.EnergyNanos, direct.Now, direct.EnergyNanos)
	}
}

func TestUrgentWorkInterruptsSleepEntry(t *testing.T) {
	timeline := NewTimeline(PowerProfile{
		ActiveEnergyPerMillisecond: 2_000,
		SleepEnergyPerMillisecond:  100,
		WakeDuration:               2 * time.Millisecond,
		WakeEnergyNanos:            12_000,
		SleepEntryDuration:         4 * time.Millisecond,
		SleepEntryEnergyNanos:      8_000,
	})
	timeline.AddPeripheral("sensor", 1_500)
	timeline.AddDomain("rf", 300)
	if err := timeline.AddPeripheralToDomain("radio", "rf", 4_000); err != nil {
		t.Fatal(err)
	}
	if err := timeline.Schedule(Event{At: time.Millisecond, Type: EventBurst, Reason: "urgent-burst", Peripheral: "sensor", CPUWork: 2 * time.Millisecond, Urgent: true}); err != nil {
		t.Fatal(err)
	}
	if err := timeline.SleepUntil(30*time.Millisecond, "sensor", "rf"); err != nil {
		t.Fatal(err)
	}
	if err := timeline.RunUntil(3 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if timeline.CPUState != CPUActive {
		t.Fatalf("urgent work must abort the sleep entry, state = %s", timeline.CPUState)
	}
	if timeline.FallAsleepDuration != time.Millisecond {
		t.Fatalf("entry progressed %s before abort", timeline.FallAsleepDuration)
	}
	// 进入休眠途中已消耗的能量不退回：1ms 过渡能耗 2000nJ。
	if timeline.EnergyNanos < 2_000 {
		t.Fatalf("entry energy was refunded: %.3f nJ", timeline.EnergyNanos)
	}
	// 中止后挂起的关断被丢弃，设备保持供电。
	if !timeline.Peripherals["sensor"].On || !timeline.Domains["rf"].On {
		t.Fatal("aborted entry must not power anything off")
	}
	aborted := false
	for _, record := range timeline.Records {
		if record.Kind == "sleep-entry-aborted" && record.Detail == "urgent-burst" {
			aborted = true
		}
	}
	if !aborted {
		t.Fatal("missing sleep-entry-aborted record")
	}
}

func TestDeadlineRequestsDecideCompletion(t *testing.T) {
	timeline := domainTimeline(t)
	events := []Event{
		// 50ms 到达，唤醒 2ms + 工作 5ms = 57ms，截止 60ms：提前唤醒并完成。
		{At: 50 * time.Millisecond, Type: EventSample, Reason: "feasible-sample", Peripheral: "adc", CPUWork: 5 * time.Millisecond, Deadline: 60 * time.Millisecond},
		// 70ms 到达，工作 3ms 要到 73ms，截止 72ms：错过时限，不唤醒。
		{At: 70 * time.Millisecond, Type: EventBurst, Reason: "late-burst", Peripheral: "radio", CPUWork: 3 * time.Millisecond, Deadline: 72 * time.Millisecond},
	}
	for _, event := range events {
		if err := timeline.Schedule(event); err != nil {
			t.Fatal(err)
		}
	}
	if err := timeline.SleepUntil(100*time.Millisecond, "analog", "rf", "io"); err != nil {
		t.Fatal(err)
	}
	if err := timeline.RunUntil(120 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if timeline.CompletedEvents[string(EventSample)] != 1 {
		t.Fatalf("completed = %v", timeline.CompletedEvents)
	}
	if timeline.MissedEvents[string(EventBurst)] != 1 {
		t.Fatalf("missed = %v", timeline.MissedEvents)
	}
	if timeline.RecoveryCount != 1 {
		t.Fatalf("recoveries = %d, only the feasible request may wake the device", timeline.RecoveryCount)
	}
	// 唤醒只拉起 adc 所在的 analog 域，无关域保持关闭。
	if !timeline.Domains["analog"].On {
		t.Fatal("analog domain should be on for the adc request")
	}
	if timeline.Domains["rf"].On || timeline.Domains["io"].On {
		t.Fatal("unrelated domains must not be woken unconditionally")
	}
}

func TestDeadlinesRespectSupplyRecovery(t *testing.T) {
	timeline := NewTimeline(PowerProfile{
		ActiveEnergyPerMillisecond: 2_000,
		SleepEnergyPerMillisecond:  100,
		WakeDuration:               2 * time.Millisecond,
		WakeEnergyNanos:            12_000,
		SupplyEnergyPerMillisecond: 1_000,
	})
	events := []Event{
		// 供能不足：4ms 工作拉长到 8ms，10+2+8=20ms 恰好赶上截止。
		{At: 10 * time.Millisecond, Type: EventSample, Reason: "stretched-sample", CPUWork: 4 * time.Millisecond, Deadline: 20 * time.Millisecond},
		// 同样的工作在供能恢复后来得及：40+2+4=46ms 截止 46ms。
		{At: 40 * time.Millisecond, Type: EventSample, Reason: "recovered-sample", CPUWork: 4 * time.Millisecond, Deadline: 46 * time.Millisecond},
	}
	for _, event := range events {
		if err := timeline.Schedule(event); err != nil {
			t.Fatal(err)
		}
	}
	if err := timeline.SleepUntil(10 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := timeline.RunUntil(20 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := timeline.SleepUntil(50 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	// 供能仍然不足时，30ms 到达、截止 34ms 的请求只能错过。
	if err := timeline.Schedule(Event{At: 30 * time.Millisecond, Type: EventBurst, Reason: "starved-burst", CPUWork: 4 * time.Millisecond, Deadline: 34 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	if err := timeline.RunUntil(34 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if timeline.CPUState != CPUSleep {
		t.Fatalf("missed request must not wake the device, state = %s", timeline.CPUState)
	}
	// 供能恢复。
	timeline.Profile.SupplyEnergyPerMillisecond = 0
	if err := timeline.RunUntil(50 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if timeline.CompletedEvents[string(EventSample)] != 2 || timeline.MissedEvents[string(EventBurst)] != 1 {
		t.Fatalf("completed=%v missed=%v", timeline.CompletedEvents, timeline.MissedEvents)
	}
	if timeline.RecoveryCount != 2 {
		t.Fatalf("recoveries = %d", timeline.RecoveryCount)
	}
	if timeline.WaitDuration != 4*time.Millisecond {
		t.Fatalf("wait = %s", timeline.WaitDuration)
	}
	// 能量账目：休眠 100nJ/ms，唤醒 12000nJ，欠压段 1000nJ/ms，活跃 2000nJ/ms。
	want := 10*100 + 12_000 + 8*1_000 + 14*100 + 6*100 + 12_000 + 4*2_000 + 4*2_000
	if math.Abs(timeline.EnergyNanos-float64(want)) > 0.001 {
		t.Fatalf("energy = %.3f nJ, want %d nJ", timeline.EnergyNanos, want)
	}
}

func TestContinuousAndRestoredRunsMatch(t *testing.T) {
	run := func(restore bool) *Timeline {
		timeline := entryTimeline()
		events := []Event{
			{At: time.Millisecond, Type: EventBurst, Reason: "urgent-burst", Peripheral: "sensor", CPUWork: 2 * time.Millisecond, Urgent: true},
			{At: 20 * time.Millisecond, Type: EventSample, Reason: "feasible-sample", Peripheral: "sensor", CPUWork: 3 * time.Millisecond, Deadline: 30 * time.Millisecond},
			{At: 40 * time.Millisecond, Type: EventSample, Reason: "late-sample", Peripheral: "sensor", CPUWork: 3 * time.Millisecond, Deadline: 42 * time.Millisecond},
		}
		for _, event := range events {
			if err := timeline.Schedule(event); err != nil {
				t.Fatal(err)
			}
		}
		steps := []func() error{
			func() error { return timeline.SleepUntil(50*time.Millisecond, "sensor") },
			func() error { return timeline.RunUntil(3 * time.Millisecond) },
			func() error { return timeline.SleepUntil(50*time.Millisecond, "sensor") },
			// 第二次进入休眠 3ms~5ms，4ms 时处于过渡途中。
			func() error { return timeline.RunUntil(4 * time.Millisecond) },
			func() error { return timeline.RunUntil(25 * time.Millisecond) },
			func() error { return timeline.SleepUntil(60*time.Millisecond, "sensor") },
			func() error { return timeline.RunUntil(65 * time.Millisecond) },
		}
		for i, step := range steps {
			if err := step(); err != nil {
				t.Fatal(err)
			}
			if restore && i == 3 {
				data, err := timeline.SaveSnapshot()
				if err != nil {
					t.Fatal(err)
				}
				timeline, err = LoadSnapshot(data)
				if err != nil {
					t.Fatal(err)
				}
				if timeline.CPUState != CPUFallingAsleep {
					t.Fatalf("restored state = %s", timeline.CPUState)
				}
			}
		}
		return timeline
	}
	direct := run(false)
	restored := run(true)
	if restored.Now != direct.Now || restored.EnergyNanos != direct.EnergyNanos {
		t.Fatalf("restored=%s/%.3f direct=%s/%.3f", restored.Now, restored.EnergyNanos, direct.Now, direct.EnergyNanos)
	}
	if restored.RecoveryCount != direct.RecoveryCount || restored.FallAsleepDuration != direct.FallAsleepDuration {
		t.Fatalf("restored recoveries=%d entry=%s", restored.RecoveryCount, restored.FallAsleepDuration)
	}
	if restored.CompletedEvents[string(EventBurst)] != 1 || restored.CompletedEvents[string(EventSample)] != 1 {
		t.Fatalf("completed = %v", restored.CompletedEvents)
	}
	if restored.MissedEvents[string(EventSample)] != 1 {
		t.Fatalf("missed = %v", restored.MissedEvents)
	}
	if len(restored.WakeReasons) != len(direct.WakeReasons) {
		t.Fatalf("wake reasons = %v vs %v", restored.WakeReasons, direct.WakeReasons)
	}
}
