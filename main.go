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
	fmt.Printf("[deadlines] time=%s wait=%s energy=%.3fuJ\n",
		deadlines.Now,
		deadlines.WaitDuration,
		deadlines.EnergyNanos/1000,
	)
	fmt.Printf("[deadlines] completed=%v missed=%v recoveries=%d\n",
		deadlines.CompletedEvents,
		deadlines.MissedEvents,
		deadlines.RecoveryCount,
	)
}

// buildDeadlineScenario 演示带截止时间的工作请求：进入休眠有 2ms 过渡，
// 供能上限 1000nJ/ms 会把活跃工作拉长；赶不上截止时间的请求记为错过，
// 不会唤醒设备；供能恢复后同样的请求可以完成。
func buildDeadlineScenario() *Timeline {
	timeline := NewTimeline(PowerProfile{
		ActiveEnergyPerMillisecond: 2_000,
		SleepEnergyPerMillisecond:  100,
		WakeDuration:               2 * time.Millisecond,
		WakeEnergyNanos:            12_000,
		SleepEntryDuration:         2 * time.Millisecond,
		SleepEntryEnergyNanos:      6_000,
		SupplyEnergyPerMillisecond: 1_000,
	})
	events := []Event{
		{At: 10 * time.Millisecond, Type: EventSample, Reason: "stretched-sample", CPUWork: 4 * time.Millisecond, Deadline: 20 * time.Millisecond},
		{At: 30 * time.Millisecond, Type: EventBurst, Reason: "starved-burst", CPUWork: 4 * time.Millisecond, Deadline: 34 * time.Millisecond},
		{At: 40 * time.Millisecond, Type: EventSample, Reason: "recovered-sample", CPUWork: 4 * time.Millisecond, Deadline: 46 * time.Millisecond},
	}
	for _, event := range events {
		if err := timeline.Schedule(event); err != nil {
			panic(err)
		}
	}
	if err := timeline.SleepUntil(10 * time.Millisecond); err != nil {
		panic(err)
	}
	if err := timeline.RunUntil(22 * time.Millisecond); err != nil {
		panic(err)
	}
	if err := timeline.SleepUntil(50 * time.Millisecond); err != nil {
		panic(err)
	}
	if err := timeline.RunUntil(34 * time.Millisecond); err != nil {
		panic(err)
	}
	// 供能恢复，后续请求不再被拉长。
	timeline.Profile.SupplyEnergyPerMillisecond = 0
	if err := timeline.RunUntil(50 * time.Millisecond); err != nil {
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
