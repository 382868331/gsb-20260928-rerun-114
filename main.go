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
	fmt.Printf("active=%s sleep=%s entry=%s wake=%s wait=%s\n",
		timeline.ActiveDuration,
		timeline.SleepDuration,
		timeline.SleepEntryDuration,
		timeline.WakeDuration,
		timeline.WaitDuration,
	)
	fmt.Printf("recoveries=%d reasons=%v\n", timeline.RecoveryCount, timeline.WakeReasons)
}

func buildScenario() *Timeline {
	timeline := NewTimelineWithBudget(PowerProfile{
		ActiveEnergyPerMillisecond: 2_000,
		SleepEnergyPerMillisecond:  100,
		SleepEntryDuration:         1 * time.Millisecond,
		SleepEntryEnergyNanos:      6_000,
		WakeDuration:               2 * time.Millisecond,
		WakeEnergyNanos:            12_000,
	}, 56_500, 500)
	timeline.AddPowerDomain("sensor-rail", 800)
	timeline.AddPowerDomain("radio-rail", 3_000)
	timeline.AddDomainPeripheral("sensor", "sensor-rail", 1_500)
	timeline.AddDomainPeripheral("radio", "radio-rail", 4_000, "sensor")

	events := []Event{
		{At: 20 * time.Millisecond, Type: EventSample, Reason: "periodic-sample", Peripheral: "sensor", CPUWork: 3 * time.Millisecond, TransferDuration: 5 * time.Millisecond, PowerOff: true},
		{At: 20 * time.Millisecond, Type: EventBurst, Reason: "external-burst", Peripheral: "radio", CPUWork: 2 * time.Millisecond, TransferDuration: 4 * time.Millisecond, PowerOff: true},
		{At: 50 * time.Millisecond, Type: EventSample, Reason: "periodic-sample", Peripheral: "sensor", CPUWork: 3 * time.Millisecond, TransferDuration: 5 * time.Millisecond, PowerOff: true},
	}
	for _, event := range events {
		if err := timeline.Schedule(event); err != nil {
			panic(err)
		}
	}

	// Sleeping the radio rail while the independent sensor rail is idle keeps
	// only the radio rail off; the sensor rail is unaffected.
	if err := timeline.SleepUntil(20*time.Millisecond, "radio-rail"); err != nil {
		panic(err)
	}
	if err := timeline.RunUntil(45 * time.Millisecond); err != nil {
		panic(err)
	}
	if err := timeline.SleepUntil(50*time.Millisecond, "sensor-rail", "radio-rail"); err != nil {
		panic(err)
	}
	if err := timeline.RunUntil(85 * time.Millisecond); err != nil {
		panic(err)
	}
	return timeline
}
