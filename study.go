package main

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// WakeStrategy 决定休眠中的处理器何时开始唤醒。
type WakeStrategy string

const (
	// WakeAnticipatory 按截止时间提前唤醒（默认行为）。
	WakeAnticipatory WakeStrategy = "anticipatory"
	// WakeOnArrival 等事件到达才开始唤醒，可能错过截止时间。
	WakeOnArrival WakeStrategy = "on-arrival"
)

// Policy 是一份可比较的节能方案：入睡等待门槛 + 唤醒策略。
type Policy struct {
	Name string `json:"name"`
	// SleepThreshold 是入睡等待门槛：空闲间隔不足该门槛时保持活跃，
	// 不支付入睡/唤醒过渡开销。
	SleepThreshold time.Duration `json:"sleepThreshold"`
	Wake           WakeStrategy  `json:"wake"`
}

// Injection 是运行中途新增的任务（例如紧急任务）：只有当仿真时间
// 到达 At 时才进入调度，因此只影响尚未发生的决策。
type Injection struct {
	At    time.Duration `json:"at"`
	Event Event         `json:"event"`
}

// Study 描述一次方案比较：同一份到达任务记录、同一个硬件模型，
// 分别套用不同 Policy 独立运行。
type Study struct {
	// NewTimeline 为每个方案构造一份全新的时间线（外设、电源域一致）。
	NewTimeline func() *Timeline
	Events      []Event
	Injections  []Injection
	Horizon     time.Duration
}

// SchemeRun 是一个方案的独立运行实例，可中途保存快照并恢复。
type SchemeRun struct {
	study      *Study
	Policy     Policy
	T          *Timeline
	injections []Injection
}

// Start 用同一份任务记录为 policy 开启一份独立运行。
func (s *Study) Start(policy Policy) (*SchemeRun, error) {
	timeline := s.NewTimeline()
	timeline.LazyWake = policy.Wake == WakeOnArrival
	run := &SchemeRun{study: s, Policy: policy, T: timeline}
	for _, event := range s.Events {
		if err := timeline.Schedule(event); err != nil {
			return nil, err
		}
	}
	run.injections = append(run.injections, s.Injections...)
	sort.SliceStable(run.injections, func(i, j int) bool {
		return run.injections[i].At < run.injections[j].At
	})
	return run, nil
}

// Resume 从快照恢复一个方案运行，继续按原策略推进；尚未到达的
// 注入仍然生效，已经发生的决策不受影响。
func (s *Study) Resume(policy Policy, snapshot []byte) (*SchemeRun, error) {
	timeline, err := LoadSnapshot(snapshot)
	if err != nil {
		return nil, err
	}
	timeline.LazyWake = policy.Wake == WakeOnArrival
	run := &SchemeRun{study: s, Policy: policy, T: timeline}
	for _, injection := range s.Injections {
		if injection.At > timeline.Now {
			run.injections = append(run.injections, injection)
		}
	}
	sort.SliceStable(run.injections, func(i, j int) bool {
		return run.injections[i].At < run.injections[j].At
	})
	return run, nil
}

func (r *SchemeRun) peripheralNames() []string {
	names := make([]string, 0, len(r.T.Peripherals))
	for name := range r.T.Peripherals {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Step 按策略推进到 stopAt（不超过研究终点）。空闲间隔达到门槛时
// 入睡，否则保持活跃等待；过渡途中（waking/sleeping）可以在任意
// stopAt 暂停并保存快照。
func (r *SchemeRun) Step(stopAt time.Duration) error {
	if stopAt > r.study.Horizon {
		stopAt = r.study.Horizon
	}
	for r.T.Now < stopAt {
		t := r.T
		// 到达注入时刻的紧急任务进入调度，只影响之后的决策。
		for len(r.injections) > 0 && r.injections[0].At <= t.Now {
			if err := t.Schedule(r.injections[0].Event); err != nil {
				return err
			}
			r.injections = r.injections[1:]
		}
		next, hasEvent := t.nextEventAt()
		target := stopAt
		if hasEvent && next > t.Now && next < target {
			target = next
		}
		if len(r.injections) > 0 && r.injections[0].At < target {
			target = r.injections[0].At
		}
		if t.CPUState == CPUActive {
			if t.firstDueEvent() >= 0 {
				if err := t.processDueEvents(); err != nil {
					return err
				}
				continue
			}
			if hasEvent && next-t.Now >= r.Policy.SleepThreshold {
				if err := t.SleepUntil(next, r.peripheralNames()...); err != nil {
					return err
				}
				continue
			}
			if target <= t.Now {
				return nil
			}
			if err := t.RunUntil(target); err != nil {
				return err
			}
			continue
		}
		// 休眠、唤醒或入睡途中：最多推进到本次过渡结束，把随后的
		// 决策（处理事件、是否再次入睡）交还给策略层。
		var stateTarget time.Duration
		switch t.CPUState {
		case CPUWaking:
			stateTarget = t.Now + t.WakeRemaining
		case CPUSleeping:
			stateTarget = t.Now + t.SleepRemaining
		default:
			stateTarget = t.sleepWakeTarget()
		}
		if stateTarget > t.Now && stateTarget < target {
			target = stateTarget
		}
		if target <= t.Now {
			target = t.Now + time.Nanosecond
		}
		if err := t.RunUntil(target); err != nil {
			return err
		}
	}
	return nil
}

// Snapshot 保存当前方案运行状态（可处于过渡途中）。
func (r *SchemeRun) Snapshot() ([]byte, error) {
	return r.T.SaveSnapshot()
}

// SchemeResult 是一份方案的独立研究结果：只统计实际发生的执行与
// 开销，不用计划节能量替代。
type SchemeResult struct {
	Policy Policy `json:"policy"`
	// Events 是该方案实际依据的任务记录（含运行中已注入的任务）。
	Events                []Event        `json:"events"`
	EnergyNanos           float64        `json:"energyNanos"`
	TransitionEnergyNanos float64        `json:"transitionEnergyNanos"`
	ActiveDuration        time.Duration  `json:"activeDuration"`
	SleepDuration         time.Duration  `json:"sleepDuration"`
	WakeDuration          time.Duration  `json:"wakeDuration"`
	SleepEntryDuration    time.Duration  `json:"sleepEntryDuration"`
	WaitDuration          time.Duration  `json:"waitDuration"`
	RecoveryCount         int            `json:"recoveryCount"`
	CompletedEvents       map[string]int `json:"completedEvents"`
	CompletedOnTime       []string       `json:"completedOnTime,omitempty"`
	MissedDeadlines       []string       `json:"missedDeadlines,omitempty"`
	Unfinished            []string       `json:"unfinished,omitempty"`
	FinalState            CPUState       `json:"finalState"`
	Now                   time.Duration  `json:"now"`
	Snapshot              []byte         `json:"snapshot"`
}

// Finish 推进到研究终点并汇总实际发生的账目。
func (r *SchemeRun) Finish() (*SchemeResult, error) {
	if err := r.Step(r.study.Horizon); err != nil {
		return nil, err
	}
	t := r.T
	result := &SchemeResult{
		Policy:             r.Policy,
		EnergyNanos:        t.EnergyNanos,
		ActiveDuration:     t.ActiveDuration,
		SleepDuration:      t.SleepDuration,
		WakeDuration:       t.WakeDuration,
		SleepEntryDuration: t.SleepEntryDuration,
		WaitDuration:       t.WaitDuration,
		RecoveryCount:      t.RecoveryCount,
		CompletedEvents:    t.CompletedEvents,
		CompletedOnTime:    t.CompletedOnTime,
		MissedDeadlines:    t.MissedDeadlines,
		FinalState:         t.CPUState,
		Now:                t.Now,
	}
	// 过渡能耗只累计已经发生的记录：完成的唤醒、完成/被打断的入睡。
	for _, record := range t.Records {
		switch record.Kind {
		case "wake", "sleep-entry", "sleep-entry-aborted":
			result.TransitionEnergyNanos += record.EnergyNanos
		}
	}
	// 方案依据的任务记录：原始任务 + 已注入任务。
	result.Events = append(result.Events, r.study.Events...)
	for _, injection := range r.study.Injections {
		if injection.At <= t.Now {
			result.Events = append(result.Events, injection.Event)
		}
	}
	// 研究终点仍未完成的任务单独列出。
	for _, event := range t.Events {
		result.Unfinished = append(result.Unfinished, event.Reason)
	}
	snapshot, err := t.SaveSnapshot()
	if err != nil {
		return nil, err
	}
	result.Snapshot = snapshot
	return result, nil
}

// CompareSchemes 对同一份任务记录分别运行各方案，返回独立结果。
func (s *Study) CompareSchemes(policies ...Policy) ([]*SchemeResult, error) {
	results := make([]*SchemeResult, 0, len(policies))
	for _, policy := range policies {
		run, err := s.Start(policy)
		if err != nil {
			return nil, err
		}
		result, err := run.Finish()
		if err != nil {
			return nil, err
		}
		results = append(results, result)
	}
	return results, nil
}

// FormatComparison 生成各方案实际账目的对比文本。
func FormatComparison(results ...*SchemeResult) string {
	var b strings.Builder
	for _, result := range results {
		fmt.Fprintf(&b, "  %-12s energy=%.3fuJ transition=%.3fuJ recoveries=%d on-time=%v missed=%v unfinished=%v\n",
			result.Policy.Name,
			result.EnergyNanos/1000,
			result.TransitionEnergyNanos/1000,
			result.RecoveryCount,
			result.CompletedOnTime,
			result.MissedDeadlines,
			result.Unfinished,
		)
	}
	return b.String()
}

// denseStudy 是短间隔密集任务场景：每 4ms 一次采样，工作 2ms。
func denseStudy() *Study {
	return &Study{
		NewTimeline: func() *Timeline {
			timeline := NewTimeline(PowerProfile{
				ActiveEnergyPerMillisecond: 2_000,
				SleepEnergyPerMillisecond:  100,
				WakeDuration:               2 * time.Millisecond,
				WakeEnergyNanos:            12_000,
				SleepEntryDuration:         time.Millisecond,
				SleepEntryEnergyNanos:      3_000,
			})
			timeline.AddPeripheral("sensor", 1_500)
			return timeline
		},
		Events: []Event{
			{At: 10 * time.Millisecond, Type: EventSample, Reason: "sample-1", Peripheral: "sensor", CPUWork: time.Millisecond, TransferDuration: 2 * time.Millisecond, PowerOff: true},
			{At: 14 * time.Millisecond, Type: EventSample, Reason: "sample-2", Peripheral: "sensor", CPUWork: time.Millisecond, TransferDuration: 2 * time.Millisecond, PowerOff: true},
			{At: 18 * time.Millisecond, Type: EventSample, Reason: "sample-3", Peripheral: "sensor", CPUWork: time.Millisecond, TransferDuration: 2 * time.Millisecond, PowerOff: true},
			{At: 22 * time.Millisecond, Type: EventSample, Reason: "sample-4", Peripheral: "sensor", CPUWork: time.Millisecond, TransferDuration: 2 * time.Millisecond, PowerOff: true},
		},
		Horizon: 30 * time.Millisecond,
	}
}

// idleUrgentStudy 是长空闲后紧急任务场景：5ms 一次常规采样，
// 之后长时间空闲，80ms 注入一个带截止时间的紧急任务。
func idleUrgentStudy() *Study {
	study := denseStudy()
	study.Events = []Event{
		{At: 5 * time.Millisecond, Type: EventSample, Reason: "routine", Peripheral: "sensor", CPUWork: time.Millisecond, TransferDuration: 2 * time.Millisecond, PowerOff: true},
	}
	study.Injections = []Injection{
		{At: 60 * time.Millisecond, Event: Event{At: 80 * time.Millisecond, Type: EventBurst, Reason: "urgent", Peripheral: "sensor", CPUWork: 2 * time.Millisecond, Urgent: true, Deadline: 83 * time.Millisecond}},
	}
	study.Horizon = 100 * time.Millisecond
	return study
}
