package main

import (
	"math"
	"reflect"
	"testing"
	"time"
)

func TestDenseTasksFavorWaitThreshold(t *testing.T) {
	study := NewStudy("dense", denseRecord())
	eager, err := study.Run(studyPlan("sleep-always", 0, WakeEager), 40*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	patient, err := study.Run(studyPlan("wait-6ms", 6*time.Millisecond, WakeEager), 40*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	// 两份结果相互独立，但任务与策略依据都完整保留。
	if len(eager.Record) != 6 || len(patient.Record) != 6 {
		t.Fatalf("records = %d/%d", len(eager.Record), len(patient.Record))
	}
	// 两个方案都完成了全部 6 个任务，没有未完成项。
	if eager.CompletedEvents[string(EventSample)] != 6 || patient.CompletedEvents[string(EventSample)] != 6 {
		t.Fatalf("completed = %v/%v", eager.CompletedEvents, patient.CompletedEvents)
	}
	if len(eager.Unfinished) != 0 || len(patient.Unfinished) != 0 {
		t.Fatalf("unfinished = %v/%v", eager.Unfinished, patient.Unfinished)
	}
	// 密集任务下每次都睡要反复支付过渡能耗，等待门槛方案更省。
	if eager.TransitionNanos <= patient.TransitionNanos {
		t.Fatalf("transition = %.1f/%.1f", eager.TransitionNanos, patient.TransitionNanos)
	}
	if eager.EnergyNanos <= patient.EnergyNanos {
		t.Fatalf("energy = %.1f/%.1f", eager.EnergyNanos, patient.EnergyNanos)
	}
	if patient.TransitionNanos != 0 {
		t.Fatalf("patient transition = %.1f", patient.TransitionNanos)
	}
}

func TestIdleThenUrgentWakeStrategies(t *testing.T) {
	study := NewStudy("idle-urgent", idleRecord())
	horizon := 60 * time.Millisecond
	// 旧方案结果：没有紧急任务时的基线。
	baseline, err := study.Run(studyPlan("eager", 0, WakeEager), horizon)
	if err != nil {
		t.Fatal(err)
	}
	eager, err := study.Run(studyPlan("eager", 0, WakeEager), horizon, urgentBurst())
	if err != nil {
		t.Fatal(err)
	}
	lazy, err := study.Run(studyPlan("lazy", 0, WakeLazy), horizon, urgentBurst())
	if err != nil {
		t.Fatal(err)
	}
	// 旧结果保留原任务与策略依据，不受中途新增任务影响。
	if len(baseline.Record) != 1 || baseline.Record[0].Reason != "periodic-sample" {
		t.Fatalf("baseline record = %v", baseline.Record)
	}
	if len(eager.Record) != 2 || len(lazy.Record) != 2 {
		t.Fatalf("records = %d/%d", len(eager.Record), len(lazy.Record))
	}
	// 唤醒策略差异：提前唤醒赶上 32ms 截止，到达才唤醒错过。
	if len(eager.CompletedOnTime) != 1 || eager.CompletedOnTime[0] != "urgent-burst" {
		t.Fatalf("eager on-time = %v missed = %v", eager.CompletedOnTime, eager.MissedDeadlines)
	}
	if len(lazy.MissedDeadlines) != 1 || lazy.MissedDeadlines[0] != "urgent-burst" {
		t.Fatalf("lazy on-time = %v missed = %v", lazy.CompletedOnTime, lazy.MissedDeadlines)
	}
	if len(eager.Unfinished) != 0 || len(lazy.Unfinished) != 0 {
		t.Fatalf("unfinished = %v/%v", eager.Unfinished, lazy.Unfinished)
	}
}

// 中途新增紧急任务只影响尚未发生的决策：注入时刻之前的账目与基线一致。
func TestUrgentInjectionOnlyAffectsFutureDecisions(t *testing.T) {
	study := NewStudy("idle-urgent", idleRecord())
	horizon := 60 * time.Millisecond
	baseline, err := study.Run(studyPlan("eager", 0, WakeEager), horizon)
	if err != nil {
		t.Fatal(err)
	}
	injected, err := study.Run(studyPlan("eager", 0, WakeEager), horizon, urgentBurst())
	if err != nil {
		t.Fatal(err)
	}
	// 注入发生在 29ms（提前一个推进步），此前的记录必须逐条一致。
	const injectVisibleAt = 29 * time.Millisecond
	before := func(records []Record) []Record {
		var out []Record
		for _, record := range records {
			if record.At < injectVisibleAt {
				out = append(out, record)
			}
		}
		return out
	}
	baseBefore, injectedBefore := before(baseline.Records), before(injected.Records)
	if len(baseBefore) == 0 || len(baseBefore) != len(injectedBefore) {
		t.Fatalf("before = %v/%v", baseBefore, injectedBefore)
	}
	for i := range baseBefore {
		if !reflect.DeepEqual(baseBefore[i], injectedBefore[i]) {
			t.Fatalf("record %d differs: %v vs %v", i, baseBefore[i], injectedBefore[i])
		}
	}
	// 注入之后账目才分叉：注入方案多了一次唤醒。
	if injected.RecoveryCount <= baseline.RecoveryCount {
		t.Fatalf("recoveries = %d/%d", injected.RecoveryCount, baseline.RecoveryCount)
	}
}

// 过渡进行到一半时保存重开：恢复运行与连续运行的任务完成和能量账目一致。
func TestSnapshotMidTransitionMatchesContinuous(t *testing.T) {
	record := denseRecord()
	plan := studyPlan("sleep-always", 0, WakeEager)
	horizon := 40 * time.Millisecond

	continuous, err := newRunner(record, plan, horizon)
	if err != nil {
		t.Fatal(err)
	}
	if err := continuous.Run(); err != nil {
		t.Fatal(err)
	}
	direct := continuous.Result()

	resumed, err := newRunner(record, plan, horizon)
	if err != nil {
		t.Fatal(err)
	}
	// 7ms 时正处于 6ms 开始的 2ms 休眠进入途中。
	if err := resumed.RunUntil(7 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if resumed.timeline.CPUState != CPUSleeping {
		t.Fatalf("state = %s, want sleeping", resumed.timeline.CPUState)
	}
	data, err := resumed.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	resumed, err = restoreRunner(data, plan, horizon)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.timeline.CPUState != CPUSleeping || resumed.timeline.SleepRemaining != time.Millisecond {
		t.Fatalf("restored state = %s remaining = %s", resumed.timeline.CPUState, resumed.timeline.SleepRemaining)
	}
	if err := resumed.Run(); err != nil {
		t.Fatal(err)
	}
	restored := resumed.Result()

	if restored.Now != direct.Now || restored.EnergyNanos != direct.EnergyNanos {
		t.Fatalf("restored = %s/%.3f direct = %s/%.3f", restored.Now, restored.EnergyNanos, direct.Now, direct.EnergyNanos)
	}
	if restored.TransitionNanos != direct.TransitionNanos {
		t.Fatalf("transition = %.3f/%.3f", restored.TransitionNanos, direct.TransitionNanos)
	}
	if len(restored.CompletedEvents) != len(direct.CompletedEvents) {
		t.Fatalf("completed = %v/%v", restored.CompletedEvents, direct.CompletedEvents)
	}
	for kind, count := range direct.CompletedEvents {
		if restored.CompletedEvents[kind] != count {
			t.Fatalf("completed = %v/%v", restored.CompletedEvents, direct.CompletedEvents)
		}
	}
	if len(restored.CompletedOnTime) != len(direct.CompletedOnTime) ||
		len(restored.MissedDeadlines) != len(direct.MissedDeadlines) ||
		len(restored.Unfinished) != len(direct.Unfinished) {
		t.Fatalf("restored on-time=%v missed=%v unfinished=%v",
			restored.CompletedOnTime, restored.MissedDeadlines, restored.Unfinished)
	}
}

// 能量守恒核对：恢复运行的能耗等于连续运行，且等于各记录能耗之和。
func TestResumedRunConservesEnergy(t *testing.T) {
	plan := studyPlan("sleep-always", 0, WakeEager)
	horizon := 40 * time.Millisecond

	continuous, err := newRunner(denseRecord(), plan, horizon, urgentBurst())
	if err != nil {
		t.Fatal(err)
	}
	if err := continuous.Run(); err != nil {
		t.Fatal(err)
	}

	// 在多个过渡途中分别保存重开，账目都必须与连续运行一致。
	for _, splitAt := range []time.Duration{7 * time.Millisecond, 12 * time.Millisecond, 29 * time.Millisecond} {
		part, err := newRunner(denseRecord(), plan, horizon, urgentBurst())
		if err != nil {
			t.Fatal(err)
		}
		if err := part.RunUntil(splitAt); err != nil {
			t.Fatal(err)
		}
		data, err := part.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		part, err = restoreRunner(data, plan, horizon)
		if err != nil {
			t.Fatal(err)
		}
		if err := part.Run(); err != nil {
			t.Fatal(err)
		}
		result := part.Result()
		direct := continuous.Result()
		if math.Abs(result.EnergyNanos-direct.EnergyNanos) > 0.001 {
			t.Fatalf("split=%s energy = %.3f want %.3f", splitAt, result.EnergyNanos, direct.EnergyNanos)
		}
		if result.Now != direct.Now || result.RecoveryCount != direct.RecoveryCount {
			t.Fatalf("split=%s now=%s recoveries=%d", splitAt, result.Now, result.RecoveryCount)
		}
		if len(result.CompletedOnTime) != len(direct.CompletedOnTime) ||
			len(result.MissedDeadlines) != len(direct.MissedDeadlines) {
			t.Fatalf("split=%s on-time=%v missed=%v", splitAt, result.CompletedOnTime, result.MissedDeadlines)
		}
	}
}
