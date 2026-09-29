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
