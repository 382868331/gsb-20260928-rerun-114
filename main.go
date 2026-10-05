package main

import (
	"fmt"
	"time"
)

func main() {
	timeline := buildScenario()

	fmt.Printf("time=%s cpu-work=%s energy=%.3fuJ\n",
		timeline.Now,
		timeline.CPUWork,
		timeline.EnergyNanos/1000,
	)
	fmt.Printf("recoveries=%d reasons=%v\n", timeline.RecoveryCount, timeline.WakeReasons)

	domains := buildDomainScenario()
	fmt.Printf("[domains] time=%s cpu-work=%s wait=%s energy=%.3fuJ\n",
		domains.Now,
		domains.CPUWork,
		domains.WaitDuration,
		domains.EnergyNanos/1000,
	)
	fmt.Printf("[domains] recoveries=%d reasons=%v\n", domains.RecoveryCount, domains.WakeReasons)

	deadlines := buildDeadlineScenario()
	fmt.Printf("[deadlines] time=%s on-time=%v missed=%v energy=%.3fuJ\n",
		deadlines.Now,
		deadlines.CompletedOnTime,
		deadlines.MissedDeadlines,
		deadlines.EnergyNanos/1000,
	)

	fmt.Print(buildDenseStudy().Report())
	fmt.Print(buildIdleUrgentStudy().Report())
}

// buildDenseStudy 用短间隔密集任务比较入睡等待门槛：
// 每次空闲即睡要反复支付过渡能耗，等待 6ms 门槛的方案保持活跃反而更省。
func buildDenseStudy() *Study {
	study := NewStudy("短间隔密集任务", denseRecord())
	if _, err := study.Run(studyPlan("每次空闲即睡", 0, WakeEager), 40*time.Millisecond); err != nil {
		panic(err)
	}
	if _, err := study.Run(studyPlan("等待6ms再睡", 6*time.Millisecond, WakeEager), 40*time.Millisecond); err != nil {
		panic(err)
	}
	return study
}

// buildIdleUrgentStudy 用长空闲后紧急任务比较唤醒策略：
// 提前唤醒赶上 32ms 截止时间，到达才唤醒则错过；基线结果保留原任务依据。
func buildIdleUrgentStudy() *Study {
	study := NewStudy("长空闲后紧急任务", idleRecord())
	horizon := 60 * time.Millisecond
	if _, err := study.Run(studyPlan("提前唤醒-基线", 0, WakeEager), horizon); err != nil {
		panic(err)
	}
	if _, err := study.Run(studyPlan("提前唤醒", 0, WakeEager), horizon, urgentBurst()); err != nil {
		panic(err)
	}
	if _, err := study.Run(studyPlan("到达才唤醒", 0, WakeLazy), horizon, urgentBurst()); err != nil {
		panic(err)
	}
	return study
}

// buildDeadlineScenario 演示休眠进入过程与带截止时间的工作请求：
// 进入休眠耗时 2ms，途中 9ms 到来的紧急工作打断进入（已耗能不退回），
// 两个截止时间重叠的请求合并处理，loose 赶上、 tight 错过。
func buildDeadlineScenario() *Timeline {
	timeline := NewTimeline(PowerProfile{
		ActiveEnergyPerMillisecond: 2_000,
		SleepEnergyPerMillisecond:  100,
		WakeDuration:               2 * time.Millisecond,
		WakeEnergyNanos:            12_000,
		SleepEntryDuration:         2 * time.Millisecond,
		SleepEntryEnergyNanos:      6_000,
	})
	timeline.AddPeripheral("sensor", 1_500)
	timeline.AddPeripheral("radio", 4_000)

	events := []Event{
		{At: 9 * time.Millisecond, Type: EventBurst, Reason: "urgent-burst", Peripheral: "radio", CPUWork: time.Millisecond, TransferDuration: 2 * time.Millisecond, Urgent: true, PowerOff: true},
		{At: 20 * time.Millisecond, Type: EventSample, Reason: "loose", Peripheral: "sensor", CPUWork: 3 * time.Millisecond, Deadline: 30 * time.Millisecond, PowerOff: true},
		{At: 20 * time.Millisecond, Type: EventBurst, Reason: "tight", Peripheral: "radio", CPUWork: 3 * time.Millisecond, Deadline: 22 * time.Millisecond, PowerOff: true},
	}
	for _, event := range events {
		if err := timeline.Schedule(event); err != nil {
			panic(err)
		}
	}

	if err := timeline.SleepUntil(50*time.Millisecond, "sensor", "radio"); err != nil {
		panic(err)
	}
	if err := timeline.RunUntil(40 * time.Millisecond); err != nil {
		panic(err)
	}
	return timeline
}

func buildScenario() *Timeline {
	timeline := NewTimeline(PowerProfile{
		ActiveEnergyPerMillisecond: 2_000,
		SleepEnergyPerMillisecond:  100,
		WakeDuration:               2 * time.Millisecond,
		WakeEnergyNanos:            12_000,
	})
	timeline.AddPeripheral("sensor", 1_500)
	timeline.AddPeripheral("radio", 4_000)

	events := []Event{
		{At: 40 * time.Millisecond, Type: EventSample, Reason: "periodic-sample", Peripheral: "sensor", CPUWork: 3 * time.Millisecond, TransferDuration: 5 * time.Millisecond, PowerOff: true},
		{At: 40 * time.Millisecond, Type: EventBurst, Reason: "external-burst", Peripheral: "radio", CPUWork: 2 * time.Millisecond, TransferDuration: 4 * time.Millisecond, PowerOff: true},
		{At: 70 * time.Millisecond, Type: EventSample, Reason: "periodic-sample", Peripheral: "sensor", CPUWork: 3 * time.Millisecond, TransferDuration: 5 * time.Millisecond, PowerOff: true},
	}
	for _, event := range events {
		if err := timeline.Schedule(event); err != nil {
			panic(err)
		}
	}

	if err := timeline.SleepUntil(40*time.Millisecond, "sensor", "radio"); err != nil {
		panic(err)
	}
	if err := timeline.RunUntil(47 * time.Millisecond); err != nil {
		panic(err)
	}
	if err := timeline.SleepUntil(70*time.Millisecond, "sensor", "radio"); err != nil {
		panic(err)
	}
	if err := timeline.RunUntil(77 * time.Millisecond); err != nil {
		panic(err)
	}
	return timeline
}

// buildDomainScenario 演示多电源域、外设唤醒依赖与供能不足：
// radio 依赖 adc（位于 analog 域），radio 传输未结束时关闭 analog 域会被阻断，
// 但独立的 io 域可以照常关闭；供能上限 3000nJ/ms 低于活跃功耗，时间被拉长并计入 wait。
func buildDomainScenario() *Timeline {
	timeline := NewTimeline(PowerProfile{
		ActiveEnergyPerMillisecond: 2_000,
		SleepEnergyPerMillisecond:  100,
		WakeDuration:               2 * time.Millisecond,
		WakeEnergyNanos:            12_000,
		SupplyEnergyPerMillisecond: 3_000,
	})
	timeline.AddDomain("analog", 200)
	timeline.AddDomain("rf", 300)
	timeline.AddDomain("io", 100)
	if err := timeline.AddPeripheralToDomain("adc", "analog", 1_500); err != nil {
		panic(err)
	}
	if err := timeline.AddPeripheralToDomain("radio", "rf", 4_000, "adc"); err != nil {
		panic(err)
	}
	if err := timeline.AddPeripheralToDomain("display", "io", 800); err != nil {
		panic(err)
	}

	events := []Event{
		{At: 30 * time.Millisecond, Type: EventBurst, Reason: "external-burst", Peripheral: "radio", CPUWork: 2 * time.Millisecond, TransferDuration: 6 * time.Millisecond},
		{At: 60 * time.Millisecond, Type: EventSample, Reason: "periodic-sample", Peripheral: "adc", CPUWork: 3 * time.Millisecond, TransferDuration: 5 * time.Millisecond, PowerOff: true},
	}
	for _, event := range events {
		if err := timeline.Schedule(event); err != nil {
			panic(err)
		}
	}

	if err := timeline.SleepUntil(30*time.Millisecond, "analog", "rf", "io"); err != nil {
		panic(err)
	}
	if err := timeline.RunUntil(45 * time.Millisecond); err != nil {
		panic(err)
	}
	// radio 开始一段长传输，持续到 55ms。
	if err := timeline.BeginTransfer("radio", 10*time.Millisecond); err != nil {
		panic(err)
	}
	// radio 的传输依赖 adc：关闭 analog 域必须受阻。
	if err := timeline.SleepUntil(60*time.Millisecond, "analog"); err != nil {
		fmt.Printf("[domains] blocked: %v\n", err)
	}
	// 独立的 io 域不受影响，可以单独关闭。
	if err := timeline.SleepUntil(60*time.Millisecond, "io"); err != nil {
		panic(err)
	}
	if err := timeline.RunUntil(80 * time.Millisecond); err != nil {
		panic(err)
	}
	return timeline
}
