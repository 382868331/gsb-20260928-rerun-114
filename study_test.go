package main

import (
	"math"
	"testing"
	"time"
)

// 短间隔密集任务：门槛低的方案每次空闲都入睡，过渡能耗反而超过
// 保持活跃的方案；比较必须使用实际发生的过渡能耗。
func TestDenseTasksThresholdComparison(t *testing.T) {
	study := denseStudy()
	results, err := study.CompareSchemes(
		Policy{Name: "eager-sleep", SleepThreshold: 2 * time.Millisecond, Wake: WakeAnticipatory},
		Policy{Name: "stay-active", SleepThreshold: 10 * time.Millisecond, Wake: WakeAnticipatory},
	)
	if err != nil {
		t.Fatal(err)
	}
	eager, calm := results[0], results[1]
	if eager.RecoveryCount <= calm.RecoveryCount {
		t.Fatalf("recoveries eager=%d calm=%d", eager.RecoveryCount, calm.RecoveryCount)
	}
	if eager.TransitionEnergyNanos <= calm.TransitionEnergyNanos {
		t.Fatalf("transition energy eager=%.1f calm=%.1f", eager.TransitionEnergyNanos, calm.TransitionEnergyNanos)
	}
	// 密集任务下频繁入睡的过渡开销超过保持活跃的代价。
	if eager.EnergyNanos <= calm.EnergyNanos {
		t.Fatalf("eager=%.1f nJ should exceed calm=%.1f nJ", eager.EnergyNanos, calm.EnergyNanos)
	}
	// 过渡能耗来自实际记录：每次入睡+唤醒 3000+12000 nJ。
	want := float64(eager.RecoveryCount) * (12_000 + 3_000)
	if math.Abs(eager.TransitionEnergyNanos-want) > 0.001 {
		t.Fatalf("transition=%.1f want %.1f", eager.TransitionEnergyNanos, want)
	}
	// 两个方案都完成了全部任务，没有未完成项。
	for _, result := range results {
		if result.CompletedEvents[string(EventSample)] != 4 || len(result.Unfinished) != 0 {
			t.Fatalf("%s completed=%v unfinished=%v", result.Policy.Name, result.CompletedEvents, result.Unfinished)
		}
	}
}

// 长空闲后紧急任务：提前唤醒策略赶上截止时间，到达才唤醒的策略错过。
func TestIdleThenUrgentWakeStrategies(t *testing.T) {
	study := idleUrgentStudy()
	results, err := study.CompareSchemes(
		Policy{Name: "anticipatory", SleepThreshold: 5 * time.Millisecond, Wake: WakeAnticipatory},
		Policy{Name: "on-arrival", SleepThreshold: 5 * time.Millisecond, Wake: WakeOnArrival},
	)
	if err != nil {
		t.Fatal(err)
	}
	anticipatory, onArrival := results[0], results[1]
	if len(anticipatory.CompletedOnTime) != 1 || anticipatory.CompletedOnTime[0] != "urgent" {
		t.Fatalf("anticipatory on-time=%v missed=%v", anticipatory.CompletedOnTime, anticipatory.MissedDeadlines)
	}
	if len(onArrival.MissedDeadlines) != 1 || onArrival.MissedDeadlines[0] != "urgent" {
		t.Fatalf("on-arrival on-time=%v missed=%v", onArrival.CompletedOnTime, onArrival.MissedDeadlines)
	}
	// 两个方案独立运行：都依据同一份任务记录（含注入的紧急任务）。
	if len(anticipatory.Events) != 2 || len(onArrival.Events) != 2 {
		t.Fatalf("events=%v vs %v", anticipatory.Events, onArrival.Events)
	}
}

// 中途新增紧急任务只影响尚未发生的决策：注入前已保存的旧方案结果
// 保留原任务与策略依据，注入后的运行才包含新任务。
func TestMidRunInjectionKeepsSavedResults(t *testing.T) {
	study := idleUrgentStudy()
	policy := Policy{Name: "anticipatory", SleepThreshold: 5 * time.Millisecond, Wake: WakeAnticipatory}

	// 跑到 50ms（注入之前）保存一份旧方案结果。
	early, err := study.Start(policy)
	if err != nil {
		t.Fatal(err)
	}
	if err := early.Step(50 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	snapshot, err := early.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	saved, err := early.Finish()
	if err != nil {
		t.Fatal(err)
	}

	// 从 50ms 的快照恢复：注入的紧急任务只影响之后的决策。
	resumed, err := study.Resume(policy, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	continued, err := resumed.Finish()
	if err != nil {
		t.Fatal(err)
	}

	// 旧结果与恢复结果账目一致（同一策略、同一任务记录）。
	if saved.EnergyNanos != continued.EnergyNanos || saved.RecoveryCount != continued.RecoveryCount {
		t.Fatalf("saved=%.1f/%d continued=%.1f/%d",
			saved.EnergyNanos, saved.RecoveryCount, continued.EnergyNanos, continued.RecoveryCount)
	}
	if len(saved.CompletedOnTime) != len(continued.CompletedOnTime) ||
		len(saved.MissedDeadlines) != len(continued.MissedDeadlines) {
		t.Fatalf("saved on-time=%v missed=%v; continued on-time=%v missed=%v",
			saved.CompletedOnTime, saved.MissedDeadlines, continued.CompletedOnTime, continued.MissedDeadlines)
	}
	// 保存的旧方案结果保留策略依据与任务记录（含后来注入的紧急任务）。
	if saved.Policy != policy || len(saved.Events) != 2 {
		t.Fatalf("saved policy=%+v events=%v", saved.Policy, saved.Events)
	}
}

// 过渡进行到一半时保存重开：连续运行与恢复运行的任务完成和能量
// 账目必须一致（能量守恒，不重复也不遗漏过渡开销）。
func TestSchemeSnapshotMidTransitionConservation(t *testing.T) {
	study := denseStudy()
	policy := Policy{Name: "eager-sleep", SleepThreshold: 2 * time.Millisecond, Wake: WakeAnticipatory}

	direct, err := study.CompareSchemes(policy)
	if err != nil {
		t.Fatal(err)
	}

	// 16ms 处理完密集事件后进入 1ms 的入睡过渡：16.5ms 时正
	// 处于过渡途中，保存快照再恢复。
	paused, err := study.Start(policy)
	if err != nil {
		t.Fatal(err)
	}
	if err := paused.Step(16*time.Millisecond + 500*time.Microsecond); err != nil {
		t.Fatal(err)
	}
	if paused.T.CPUState != CPUSleeping {
		t.Fatalf("state = %s, want sleeping mid-transition", paused.T.CPUState)
	}
	snapshot, err := paused.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := study.Resume(policy, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := resumed.Finish()
	if err != nil {
		t.Fatal(err)
	}

	want := direct[0]
	if restored.EnergyNanos != want.EnergyNanos || restored.Now != want.Now {
		t.Fatalf("restored=%.1f/%s direct=%.1f/%s",
			restored.EnergyNanos, restored.Now, want.EnergyNanos, want.Now)
	}
	if restored.TransitionEnergyNanos != want.TransitionEnergyNanos {
		t.Fatalf("transition restored=%.1f direct=%.1f",
			restored.TransitionEnergyNanos, want.TransitionEnergyNanos)
	}
	if restored.CompletedEvents[string(EventSample)] != want.CompletedEvents[string(EventSample)] {
		t.Fatalf("completed restored=%v direct=%v", restored.CompletedEvents, want.CompletedEvents)
	}
	if len(restored.Unfinished) != 0 || len(restored.MissedDeadlines) != 0 {
		t.Fatalf("unfinished=%v missed=%v", restored.Unfinished, restored.MissedDeadlines)
	}
}
