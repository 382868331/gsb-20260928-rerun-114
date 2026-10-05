package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// WakeStrategy 决定休眠中如何为带截止时间的工作安排唤醒。
type WakeStrategy string

const (
	// WakeEager 按唤醒延迟提前唤醒，争取赶上截止时间。
	WakeEager WakeStrategy = "eager"
	// WakeLazy 事件到达才唤醒，不为截止时间提前支付唤醒过程。
	WakeLazy WakeStrategy = "lazy"
)

// Plan 是一套可比较的节能方案：同一份到达任务记录可以分别用不同的
// 入睡等待门槛与唤醒策略运行，形成相互独立的研究结果。
type Plan struct {
	Name string `json:"name"`
	// SleepWaitThreshold 是入睡等待门槛：空闲间隔达到它才进入休眠，
	// 否则保持活跃空转，避免为短间隔密集任务反复支付过渡能耗。
	SleepWaitThreshold time.Duration `json:"sleepWaitThreshold"`
	WakeStrategy       WakeStrategy  `json:"wakeStrategy"`
	Profile            PowerProfile  `json:"profile"`
	// PeripheralPower 按名称注册外设功耗（nJ/ms）。
	PeripheralPower map[string]float64 `json:"peripheralPower,omitempty"`
	// PowerOff 是入睡时要关闭的外设或电源域。
	PowerOff []string `json:"powerOff,omitempty"`
}

// PlanResult 是一份独立研究结果：只统计实际发生的执行与过渡开销，
// 不用计划节能量替代已经发生的能耗。Record 与 Plan 保留了运行时的
// 任务与策略依据，之后注入紧急任务不会改写已保存的旧结果。
type PlanResult struct {
	Plan   Plan    `json:"plan"`
	Record []Event `json:"record"`

	Now             time.Duration `json:"now"`
	EnergyNanos     float64       `json:"energyNanos"`
	TransitionNanos float64       `json:"transitionNanos"`
	ActiveDuration  time.Duration `json:"activeDuration"`
	SleepDuration   time.Duration `json:"sleepDuration"`
	WakeDuration    time.Duration `json:"wakeDuration"`
	EntryDuration   time.Duration `json:"entryDuration"`
	WaitDuration    time.Duration `json:"waitDuration"`
	RecoveryCount   int           `json:"recoveryCount"`

	CompletedEvents map[string]int `json:"completedEvents"`
	CompletedOnTime []string       `json:"completedOnTime,omitempty"`
	MissedDeadlines []string       `json:"missedDeadlines,omitempty"`
	Unfinished      []Event        `json:"unfinished,omitempty"`
	Records         []Record       `json:"records,omitempty"`
}

// runner 按方案逐步驱动一条时间线；注入的紧急任务在到达时才进入
// 调度器，因此只影响尚未发生的决策。
type runner struct {
	timeline *Timeline
	plan     Plan
	horizon  time.Duration
	pending  []Event
	used     []Event
}

const runnerStep = time.Millisecond

func sortedEvents(events []Event) []Event {
	out := append([]Event(nil), events...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].At < out[j].At })
	return out
}

func newRunner(record []Event, plan Plan, horizon time.Duration, inject ...Event) (*runner, error) {
	timeline := NewTimeline(plan.Profile)
	names := make([]string, 0, len(plan.PeripheralPower))
	for name := range plan.PeripheralPower {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		timeline.AddPeripheral(name, plan.PeripheralPower[name])
	}
	if plan.WakeStrategy == WakeLazy {
		lead := time.Duration(0)
		timeline.DeadlineWakeLead = &lead
	}
	used := sortedEvents(record)
	for _, event := range used {
		if err := timeline.Schedule(event); err != nil {
			return nil, err
		}
	}
	return &runner{
		timeline: timeline,
		plan:     plan,
		horizon:  horizon,
		pending:  sortedEvents(inject),
		used:     used,
	}, nil
}

// Run 推进到研究终点。
func (r *runner) Run() error {
	return r.RunUntil(r.horizon)
}

// RunUntil 推进到指定时刻，便于在过渡途中保存快照。
func (r *runner) RunUntil(at time.Duration) error {
	if at > r.horizon {
		at = r.horizon
	}
	for r.timeline.Now < at {
		if err := r.step(); err != nil {
			return err
		}
	}
	return nil
}

func (r *runner) step() error {
	timeline := r.timeline
	// 注入到达的紧急任务：此前已经做出的入睡/唤醒决策保持不变，
	// 只有尚未发生的决策能看到新任务。
	for len(r.pending) > 0 && r.pending[0].At <= timeline.Now+runnerStep {
		event := r.pending[0]
		r.pending = r.pending[1:]
		if event.At < timeline.Now {
			event.At = timeline.Now
		}
		if err := timeline.Schedule(event); err != nil {
			return err
		}
		r.used = append(r.used, event)
	}
	if timeline.CPUState == CPUActive && timeline.firstDueEvent() < 0 {
		target := r.horizon
		if next, has := timeline.nextEventAt(); has {
			target = next
		}
		if gap := target - timeline.Now; gap > 0 && gap >= r.plan.SleepWaitThreshold {
			wakeAt := target
			if r.plan.WakeStrategy == WakeEager {
				wakeAt = target - timeline.Profile.WakeDuration
				if wakeAt < timeline.Now {
					wakeAt = timeline.Now
				}
			}
			if err := timeline.SleepUntil(wakeAt, r.plan.PowerOff...); err != nil {
				return err
			}
		}
	}
	next := timeline.Now + runnerStep
	if next > r.horizon {
		next = r.horizon
	}
	return timeline.RunUntil(next)
}

// runnerSnapshot 保存推进途中的完整状态：时间线、尚未注入的紧急任务
// 与已经使用的任务记录，恢复后继续推进与连续运行账目一致。
type runnerSnapshot struct {
	Timeline json.RawMessage `json:"timeline"`
	Pending  []Event         `json:"pending,omitempty"`
	Used     []Event         `json:"used"`
}

func (r *runner) Snapshot() ([]byte, error) {
	data, err := r.timeline.SaveSnapshot()
	if err != nil {
		return nil, err
	}
	return json.Marshal(runnerSnapshot{Timeline: data, Pending: r.pending, Used: r.used})
}

func restoreRunner(data []byte, plan Plan, horizon time.Duration) (*runner, error) {
	var snapshot runnerSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return nil, err
	}
	timeline, err := LoadSnapshot(snapshot.Timeline)
	if err != nil {
		return nil, err
	}
	return &runner{
		timeline: timeline,
		plan:     plan,
		horizon:  horizon,
		pending:  snapshot.Pending,
		used:     snapshot.Used,
	}, nil
}

// Result 汇总实际发生的账目：能耗、过渡能耗、完成/错过/未完成任务。
func (r *runner) Result() *PlanResult {
	timeline := r.timeline
	result := &PlanResult{
		Plan:            r.plan,
		Record:          append([]Event(nil), r.used...),
		Now:             timeline.Now,
		EnergyNanos:     timeline.EnergyNanos,
		ActiveDuration:  timeline.ActiveDuration,
		SleepDuration:   timeline.SleepDuration,
		WakeDuration:    timeline.WakeDuration,
		EntryDuration:   timeline.SleepEntryDuration,
		WaitDuration:    timeline.WaitDuration,
		RecoveryCount:   timeline.RecoveryCount,
		CompletedEvents: make(map[string]int, len(timeline.CompletedEvents)),
		CompletedOnTime: append([]string(nil), timeline.CompletedOnTime...),
		MissedDeadlines: append([]string(nil), timeline.MissedDeadlines...),
		Records:         append([]Record(nil), timeline.Records...),
	}
	for kind, count := range timeline.CompletedEvents {
		result.CompletedEvents[kind] = count
	}
	// 过渡能耗只累计已经发生的唤醒与休眠进入开销（含被打断的进入）。
	for _, record := range timeline.Records {
		switch record.Kind {
		case "wake", "sleep-entry", "sleep-entry-aborted":
			result.TransitionNanos += record.EnergyNanos
		}
	}
	result.Unfinished = append(result.Unfinished, timeline.Events...)
	return result
}

// Study 对同一份到达任务记录运行多份方案，保存各自独立的研究结果。
type Study struct {
	Name    string
	Record  []Event
	Results []*PlanResult
}

func NewStudy(name string, record []Event) *Study {
	return &Study{Name: name, Record: sortedEvents(record)}
}

// studyProfile 是演示与核对场景共用的功耗参数。
func studyProfile() PowerProfile {
	return PowerProfile{
		ActiveEnergyPerMillisecond: 2_000,
		SleepEnergyPerMillisecond:  100,
		WakeDuration:               2 * time.Millisecond,
		WakeEnergyNanos:            12_000,
		SleepEntryDuration:         2 * time.Millisecond,
		SleepEntryEnergyNanos:      6_000,
	}
}

func studyPlan(name string, threshold time.Duration, wake WakeStrategy) Plan {
	return Plan{
		Name:               name,
		SleepWaitThreshold: threshold,
		WakeStrategy:       wake,
		Profile:            studyProfile(),
		PeripheralPower:    map[string]float64{"sensor": 1_500},
		PowerOff:           []string{"sensor"},
	}
}

// denseRecord 是短间隔密集任务记录：每 6ms 一个 1ms 的采样，空闲间隔 5ms。
func denseRecord() []Event {
	var events []Event
	for i := 0; i < 6; i++ {
		events = append(events, Event{
			At:         time.Duration(5+i*6) * time.Millisecond,
			Type:       EventSample,
			Reason:     "dense-sample",
			Peripheral: "sensor",
			CPUWork:    time.Millisecond,
			PowerOff:   true,
		})
	}
	return events
}

// idleRecord 是长空闲任务记录：5ms 一次采样后长期空闲。
func idleRecord() []Event {
	return []Event{
		{At: 5 * time.Millisecond, Type: EventSample, Reason: "periodic-sample", Peripheral: "sensor", CPUWork: time.Millisecond, PowerOff: true},
	}
}

// urgentBurst 是长空闲后中途新增的紧急任务：30ms 到达，截止 32ms。
func urgentBurst() Event {
	return Event{At: 30 * time.Millisecond, Type: EventBurst, Reason: "urgent-burst", Peripheral: "sensor", CPUWork: time.Millisecond, Urgent: true, Deadline: 32 * time.Millisecond}
}

// Run 用一份方案运行研究；inject 是中途新增的紧急任务，只在到达后
// 影响尚未发生的决策，已保存的旧结果保留原任务与策略依据。
func (s *Study) Run(plan Plan, horizon time.Duration, inject ...Event) (*PlanResult, error) {
	runner, err := newRunner(s.Record, plan, horizon, inject...)
	if err != nil {
		return nil, err
	}
	if err := runner.Run(); err != nil {
		return nil, err
	}
	result := runner.Result()
	s.Results = append(s.Results, result)
	return result, nil
}

// Report 输出各方案实际账目的对比表。
func (s *Study) Report() string {
	var out strings.Builder
	fmt.Fprintf(&out, "研究 %q：%d 个方案，任务记录 %d 条\n", s.Name, len(s.Results), len(s.Record))
	fmt.Fprintf(&out, "%-14s %9s %9s %7s %7s %5s %4s %4s %4s\n",
		"方案", "能耗uJ", "过渡uJ", "活跃ms", "休眠ms", "恢复", "按时", "错过", "未完")
	for _, result := range s.Results {
		fmt.Fprintf(&out, "%-14s %9.3f %9.3f %7.1f %7.1f %5d %4d %4d %4d\n",
			result.Plan.Name,
			result.EnergyNanos/1000,
			result.TransitionNanos/1000,
			float64(result.ActiveDuration)/float64(time.Millisecond),
			float64(result.SleepDuration)/float64(time.Millisecond),
			result.RecoveryCount,
			len(result.CompletedOnTime),
			len(result.MissedDeadlines),
			len(result.Unfinished),
		)
	}
	return out.String()
}
