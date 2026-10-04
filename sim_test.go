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

func sleepEntryTimeline() *Timeline {
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
		timeline := sleepEntryTimeline()
		if err := timeline.Schedule(Event{At: 25 * time.Millisecond, Type: EventSample, Reason: "periodic-sample", Peripheral: "sensor", CPUWork: 2 * time.Millisecond, TransferDuration: 3 * time.Millisecond, PowerOff: true}); err != nil {
			t.Fatal(err)
		}
		if err := timeline.SleepUntil(30*time.Millisecond, "sensor"); err != nil {
			t.Fatal(err)
		}
		// 1ms 时正处于 2ms 的休眠进入途中。
		if err := timeline.RunUntil(1 * time.Millisecond); err != nil {
			t.Fatal(err)
		}
		if restore {
			if timeline.CPUState != CPUSleeping {
				t.Fatalf("state = %s, want sleeping", timeline.CPUState)
			}
			data, err := timeline.SaveSnapshot()
			if err != nil {
				t.Fatal(err)
			}
			timeline, err = LoadSnapshot(data)
			if err != nil {
				t.Fatal(err)
			}
			if timeline.CPUState != CPUSleeping || timeline.SleepRemaining != time.Millisecond {
				t.Fatalf("restored state=%s remaining=%s", timeline.CPUState, timeline.SleepRemaining)
			}
		}
		if err := timeline.RunUntil(40 * time.Millisecond); err != nil {
			t.Fatal(err)
		}
		return timeline
	}
	direct := run(false)
	restored := run(true)
	if restored.Now != direct.Now || restored.EnergyNanos != direct.EnergyNanos {
		t.Fatalf("restored=%s/%.1f direct=%s/%.1f", restored.Now, restored.EnergyNanos, direct.Now, direct.EnergyNanos)
	}
	if restored.SleepEntryDuration != 2*time.Millisecond {
		t.Fatalf("sleep entry = %s", restored.SleepEntryDuration)
	}
	// 待处理事件在重开后继续：25ms 的采样在恢复后照常完成。
	if restored.CompletedEvents[string(EventSample)] != 1 || restored.CPUWork != 2*time.Millisecond {
		t.Fatalf("completed=%v cpu-work=%s", restored.CompletedEvents, restored.CPUWork)
	}
	if restored.Peripherals["sensor"].On {
		t.Fatal("sensor should be off after the sleep entry completes")
	}
}

func TestUrgentEventInterruptsSleepEntry(t *testing.T) {
	timeline := sleepEntryTimeline()
	timeline.AddDomain("analog", 200)
	timeline.AddDomain("rf", 300)
	timeline.AddDomain("io", 100)
	if err := timeline.AddPeripheralToDomain("adc", "analog", 1_500); err != nil {
		t.Fatal(err)
	}
	if err := timeline.AddPeripheralToDomain("radio", "rf", 4_000, "adc"); err != nil {
		t.Fatal(err)
	}
	// 先完成一次休眠进入，让 io 域真正关闭。
	if err := timeline.SleepUntil(6*time.Millisecond, "io"); err != nil {
		t.Fatal(err)
	}
	if err := timeline.RunUntil(8 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if timeline.Domains["io"].On {
		t.Fatal("io domain should be off after the first sleep entry")
	}
	// 第二次进入休眠，途中 9ms 到来紧急工作。
	if err := timeline.Schedule(Event{At: 9 * time.Millisecond, Type: EventBurst, Reason: "urgent-burst", Peripheral: "radio", CPUWork: time.Millisecond, TransferDuration: 2 * time.Millisecond, Urgent: true}); err != nil {
		t.Fatal(err)
	}
	if err := timeline.SleepUntil(30*time.Millisecond, "analog", "rf", "io"); err != nil {
		t.Fatal(err)
	}
	if err := timeline.RunUntil(20 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	aborted := false
	for _, record := range timeline.Records {
		if record.Kind == "sleep-entry-aborted" {
			aborted = true
		}
	}
	if !aborted {
		t.Fatal("urgent event did not interrupt the sleep entry")
	}
	if timeline.CompletedEvents[string(EventBurst)] != 1 {
		t.Fatalf("urgent event not processed: %v", timeline.CompletedEvents)
	}
	// 已经消耗的能量不退回：进入过程占了 1ms 活跃能耗。
	if timeline.SleepEntryDuration != 3*time.Millisecond {
		t.Fatalf("sleep entry total = %s", timeline.SleepEntryDuration)
	}
	// 独立域不应被无条件唤醒：io 与本次工作无关，保持关闭。
	if timeline.Domains["io"].On {
		t.Fatal("unrelated io domain must stay off")
	}
	if !timeline.Domains["analog"].On || !timeline.Domains["rf"].On {
		t.Fatal("urgent work must power its own dependency chain")
	}
}

func TestOverlappingDeadlinesAccounting(t *testing.T) {
	timeline := testTimeline()
	events := []Event{
		{At: 10 * time.Millisecond, Type: EventSample, Reason: "loose", Peripheral: "sensor", CPUWork: 3 * time.Millisecond, Deadline: 20 * time.Millisecond},
		{At: 10 * time.Millisecond, Type: EventBurst, Reason: "tight", Peripheral: "radio", CPUWork: 3 * time.Millisecond, Deadline: 12 * time.Millisecond},
	}
	for _, event := range events {
		if err := timeline.Schedule(event); err != nil {
			t.Fatal(err)
		}
	}
	if err := timeline.RunUntil(30 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	// 两个请求合并处理，6ms 后到 16ms 完成：loose 赶上 20ms，tight 错过 12ms。
	if len(timeline.CompletedOnTime) != 1 || timeline.CompletedOnTime[0] != "loose" {
		t.Fatalf("on-time = %v", timeline.CompletedOnTime)
	}
	if len(timeline.MissedDeadlines) != 1 || timeline.MissedDeadlines[0] != "tight" {
		t.Fatalf("missed = %v", timeline.MissedDeadlines)
	}
}

func TestEarlyWakeMeetsDeadline(t *testing.T) {
	timeline := testTimeline()
	if err := timeline.Schedule(Event{At: 10 * time.Millisecond, Type: EventSample, Reason: "deadline-work", Peripheral: "sensor", CPUWork: 2 * time.Millisecond, Deadline: 12 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	if err := timeline.SleepUntil(100 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := timeline.RunUntil(20 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	// 提前 2ms 唤醒，10ms 事件到达时已活跃，12ms 正好赶上截止时间。
	if len(timeline.MissedDeadlines) != 0 {
		t.Fatalf("missed = %v", timeline.MissedDeadlines)
	}
	if len(timeline.CompletedOnTime) != 1 || timeline.CompletedOnTime[0] != "deadline-work" {
		t.Fatalf("on-time = %v", timeline.CompletedOnTime)
	}
	if timeline.WakeReasons[0] != "deadline-work" {
		t.Fatalf("wake reasons = %v", timeline.WakeReasons)
	}
}

func TestSupplyRecoveryTimeline(t *testing.T) {
	timeline := NewTimeline(PowerProfile{
		ActiveEnergyPerMillisecond: 2_000,
		SleepEnergyPerMillisecond:  100,
		SupplyEnergyPerMillisecond: 1_000,
		SupplyRecoveryAt:           10 * time.Millisecond,
	})
	// 恢复前：5ms 活跃工作被 1000nJ/ms 的供给拉长到 10ms。
	if err := timeline.RunUntil(5 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if timeline.Now != 10*time.Millisecond || timeline.WaitDuration != 5*time.Millisecond {
		t.Fatalf("time=%s wait=%s", timeline.Now, timeline.WaitDuration)
	}
	// 恢复后供给不限：2ms 工作不再拉长。
	if err := timeline.RunUntil(12 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if timeline.Now != 12*time.Millisecond || timeline.WaitDuration != 5*time.Millisecond {
		t.Fatalf("time=%s wait=%s", timeline.Now, timeline.WaitDuration)
	}
	// 能量账目：1000nJ/ms x 10ms + 2000nJ/ms x 2ms。
	if math.Abs(timeline.EnergyNanos-14_000) > 0.001 {
		t.Fatalf("energy = %.3f nJ, want 14000 nJ", timeline.EnergyNanos)
	}
}

func TestContinuousAndResumedRunsAgree(t *testing.T) {
	build := func() *Timeline {
		timeline := sleepEntryTimeline()
		events := []Event{
			{At: 12 * time.Millisecond, Type: EventSample, Reason: "on-time", Peripheral: "sensor", CPUWork: 2 * time.Millisecond, Deadline: 18 * time.Millisecond},
			{At: 12 * time.Millisecond, Type: EventBurst, Reason: "late", Peripheral: "sensor", CPUWork: 3 * time.Millisecond, Deadline: 13 * time.Millisecond},
		}
		for _, event := range events {
			if err := timeline.Schedule(event); err != nil {
				t.Fatal(err)
			}
		}
		if err := timeline.SleepUntil(50*time.Millisecond, "sensor"); err != nil {
			t.Fatal(err)
		}
		return timeline
	}
	direct := build()
	if err := direct.RunUntil(30 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	// 在休眠进入途中保存，恢复后继续推进。
	resumed := build()
	if err := resumed.RunUntil(1 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	data, err := resumed.SaveSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	resumed, err = LoadSnapshot(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := resumed.RunUntil(30 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if resumed.Now != direct.Now || resumed.EnergyNanos != direct.EnergyNanos {
		t.Fatalf("resumed=%s/%.1f direct=%s/%.1f", resumed.Now, resumed.EnergyNanos, direct.Now, direct.EnergyNanos)
	}
	if len(resumed.CompletedOnTime) != len(direct.CompletedOnTime) || len(resumed.MissedDeadlines) != len(direct.MissedDeadlines) {
		t.Fatalf("resumed on-time=%v missed=%v; direct on-time=%v missed=%v",
			resumed.CompletedOnTime, resumed.MissedDeadlines, direct.CompletedOnTime, direct.MissedDeadlines)
	}
	if len(direct.CompletedOnTime) != 1 || direct.CompletedOnTime[0] != "on-time" ||
		len(direct.MissedDeadlines) != 1 || direct.MissedDeadlines[0] != "late" {
		t.Fatalf("on-time=%v missed=%v", direct.CompletedOnTime, direct.MissedDeadlines)
	}
}
