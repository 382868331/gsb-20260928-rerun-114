package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"
)

type CPUState string

const (
	CPUActive CPUState = "active"
	CPUSleep  CPUState = "sleep"
	CPUWaking CPUState = "waking"
)

type EventType string

const (
	EventSample EventType = "sample"
	EventBurst  EventType = "burst"
)

type PowerProfile struct {
	ActiveEnergyPerMillisecond float64       `json:"activeEnergyPerMillisecond"`
	SleepEnergyPerMillisecond  float64       `json:"sleepEnergyPerMillisecond"`
	WakeDuration               time.Duration `json:"wakeDuration"`
	WakeEnergyNanos            float64       `json:"wakeEnergyNanos"`
}

type Peripheral struct {
	Name                 string  `json:"name"`
	EnergyPerMillisecond float64 `json:"energyPerMillisecond"`
	On                   bool    `json:"on"`
	TransferEnd          int64   `json:"transferEnd"`
}

func (p *Peripheral) transferring(now time.Duration) bool {
	return p.On && p.TransferEnd > int64(now)
}

type Event struct {
	At               time.Duration `json:"at"`
	Type             EventType     `json:"type"`
	Reason           string        `json:"reason"`
	Peripheral       string        `json:"peripheral,omitempty"`
	CPUWork          time.Duration `json:"cpuWork"`
	TransferDuration time.Duration `json:"transferDuration,omitempty"`
	PowerOff         bool          `json:"powerOff,omitempty"`
}

type Record struct {
	At          time.Duration `json:"at"`
	Kind        string        `json:"kind"`
	Reasons     []string      `json:"reasons,omitempty"`
	Duration    time.Duration `json:"duration,omitempty"`
	EnergyNanos float64       `json:"energyNanos,omitempty"`
	Detail      string        `json:"detail,omitempty"`
}

type Timeline struct {
	Now              time.Duration          `json:"now"`
	EnergyNanos      float64                `json:"energyNanos"`
	CPUState         CPUState               `json:"cpuState"`
	WakeAt           time.Duration          `json:"wakeAt"`
	Profile          PowerProfile           `json:"profile"`
	Peripherals      map[string]*Peripheral `json:"peripherals"`
	Events           []Event                `json:"events"`
	Records          []Record               `json:"records"`
	ActiveDuration   time.Duration          `json:"activeDuration"`
	SleepDuration    time.Duration          `json:"sleepDuration"`
	WakeDuration     time.Duration          `json:"wakeDurationTotal"`
	CPUWork          time.Duration          `json:"cpuWork"`
	TransferDuration time.Duration          `json:"transferDuration"`
	CompletedEvents  map[string]int         `json:"completedEvents"`
	WakeReasons      []string               `json:"wakeReasons"`
	RecoveryCount    int                    `json:"recoveryCount"`
}

func NewTimeline(profile PowerProfile) *Timeline {
	return &Timeline{
		CPUState:        CPUActive,
		Profile:         profile,
		Peripherals:     make(map[string]*Peripheral),
		CompletedEvents: make(map[string]int),
	}
}

func (t *Timeline) AddPeripheral(name string, energyPerMillisecond float64) {
	t.Peripherals[name] = &Peripheral{Name: name, EnergyPerMillisecond: energyPerMillisecond}
}

func (t *Timeline) BeginTransfer(name string, duration time.Duration) error {
	if t.CPUState != CPUActive {
		return fmt.Errorf("processor is %s, not active", t.CPUState)
	}
	peripheral := t.Peripherals[name]
	if peripheral == nil {
		return fmt.Errorf("unknown peripheral %q", name)
	}
	peripheral.On = true
	peripheral.TransferEnd = int64(t.Now + duration)
	t.TransferDuration += duration
	return nil
}

func (t *Timeline) Schedule(event Event) error {
	if event.At < t.Now {
		return fmt.Errorf("event %q is before current time %s", event.Reason, t.Now)
	}
	if event.Reason == "" {
		return errors.New("wake event requires a reason")
	}
	t.Events = append(t.Events, event)
	return nil
}

func (t *Timeline) SleepUntil(wakeAt time.Duration, peripherals ...string) error {
	if t.CPUState != CPUActive {
		return fmt.Errorf("processor is %s, not active", t.CPUState)
	}
	if wakeAt < t.Now {
		return fmt.Errorf("wake time %s is before current time %s", wakeAt, t.Now)
	}
	for _, name := range peripherals {
		peripheral := t.Peripherals[name]
		if peripheral == nil {
			return fmt.Errorf("unknown peripheral %q", name)
		}
		if peripheral.transferring(t.Now) {
			return fmt.Errorf("cannot power off %q: transfer finishes at %s", name, time.Duration(peripheral.TransferEnd))
		}
	}
	for _, name := range peripherals {
		peripheral := t.Peripherals[name]
		peripheral.On = false
		peripheral.TransferEnd = 0
	}
	t.CPUState = CPUSleep
	t.WakeAt = wakeAt
	t.Records = append(t.Records, Record{At: t.Now, Kind: "sleep", Duration: wakeAt - t.Now})
	return nil
}

func (t *Timeline) RunUntil(horizon time.Duration) error {
	sort.SliceStable(t.Events, func(i, j int) bool { return t.Events[i].At < t.Events[j].At })

	for t.Now < horizon {
		if t.CPUState == CPUSleep {
			nextEventAt, hasEvent := t.nextEventAt()
			target := t.WakeAt
			if hasEvent && nextEventAt < target {
				target = nextEventAt
			}
			if target > horizon {
				target = horizon
			}
			t.advance(target - t.Now)
			if t.CPUState == CPUSleep && (t.Now >= t.WakeAt || t.firstDueEvent() >= 0) {
				if err := t.recover(); err != nil {
					return err
				}
			}
			if t.Now >= horizon {
				return nil
			}
		}

		if t.CPUState != CPUActive {
			return fmt.Errorf("processor is %s at %s", t.CPUState, t.Now)
		}

		if t.firstDueEvent() >= 0 {
			if err := t.processDueEvents(); err != nil {
				return err
			}
			continue
		}

		nextEventAt, hasEvent := t.nextEventAt()
		if !hasEvent || nextEventAt > horizon {
			t.advance(horizon - t.Now)
			return nil
		}
		t.advance(nextEventAt - t.Now)
		if err := t.processDueEvents(); err != nil {
			return err
		}
	}
	return nil
}

func (t *Timeline) nextEventAt() (time.Duration, bool) {
	if len(t.Events) == 0 {
		return 0, false
	}
	return t.Events[0].At, true
}

func (t *Timeline) firstDueEvent() int {
	for i := range t.Events {
		if t.Events[i].At <= t.Now {
			return i
		}
	}
	return -1
}

func (t *Timeline) recover() error {
	due := t.dueEvents()
	reasons := make([]string, 0, len(due))
	for _, event := range due {
		reasons = append(reasons, event.Reason)
	}
	if len(reasons) == 0 {
		reasons = append(reasons, "scheduled-wakeup")
	}

	t.CPUState = CPUWaking
	wakeDuration := t.Profile.WakeDuration
	before := t.EnergyNanos
	t.advance(wakeDuration)
	t.EnergyNanos += t.Profile.WakeEnergyNanos
	t.WakeDuration += wakeDuration
	t.RecoveryCount++
	t.WakeReasons = append(t.WakeReasons, reasons...)
	t.Records = append(t.Records, Record{
		At:          t.Now - wakeDuration,
		Kind:        "wake",
		Reasons:     reasons,
		Duration:    wakeDuration,
		EnergyNanos: t.EnergyNanos - before,
	})
	t.CPUState = CPUActive
	return nil
}

func (t *Timeline) dueEvents() []Event {
	index := t.firstDueEvent()
	if index < 0 {
		return nil
	}
	groupTime := t.Events[index].At
	group := []Event{}
	for _, event := range t.Events {
		if event.At == groupTime {
			group = append(group, event)
		}
	}
	return group
}

func (t *Timeline) processDueEvents() error {
	group := t.dueEvents()
	if len(group) == 0 {
		return nil
	}
	groupTime := group[0].At
	remaining := t.Events[:0]
	for _, event := range t.Events {
		if event.At != groupTime {
			remaining = append(remaining, event)
		}
	}
	t.Events = remaining

	start := t.Now
	var cpuWork time.Duration
	var maxTransfer time.Duration
	powerOff := map[string]bool{}
	reasons := make([]string, 0, len(group))
	for _, event := range group {
		if event.Peripheral != "" {
			peripheral := t.Peripherals[event.Peripheral]
			if peripheral == nil {
				return fmt.Errorf("event %q uses unknown peripheral %q", event.Reason, event.Peripheral)
			}
			peripheral.On = true
			if event.TransferDuration > 0 {
				peripheral.TransferEnd = int64(start + event.TransferDuration)
			}
			if event.TransferDuration > maxTransfer {
				maxTransfer = event.TransferDuration
			}
			if previous, exists := powerOff[event.Peripheral]; !exists {
				powerOff[event.Peripheral] = event.PowerOff
			} else if previous && !event.PowerOff {
				powerOff[event.Peripheral] = false
			}
		}
		cpuWork += event.CPUWork
		t.CPUWork += event.CPUWork
		t.TransferDuration += event.TransferDuration
		t.CompletedEvents[string(event.Type)]++
		reasons = append(reasons, event.Reason)
	}

	duration := cpuWork
	if maxTransfer > duration {
		duration = maxTransfer
	}
	before := t.EnergyNanos
	t.advance(duration)
	t.Records = append(t.Records, Record{
		At:          start,
		Kind:        "events",
		Reasons:     reasons,
		Duration:    duration,
		EnergyNanos: t.EnergyNanos - before,
	})
	for name, shouldPowerOff := range powerOff {
		if shouldPowerOff {
			peripheral := t.Peripherals[name]
			peripheral.On = false
			peripheral.TransferEnd = 0
		}
	}
	return nil
}

func (t *Timeline) advance(duration time.Duration) {
	if duration <= 0 {
		return
	}

	cpuEnergyPerMillisecond := 0.0
	switch t.CPUState {
	case CPUActive:
		cpuEnergyPerMillisecond = t.Profile.ActiveEnergyPerMillisecond
		t.ActiveDuration += duration
	case CPUSleep:
		cpuEnergyPerMillisecond = t.Profile.SleepEnergyPerMillisecond
		t.SleepDuration += duration
	}

	peripheralEnergy := 0.0
	for _, peripheral := range t.Peripherals {
		if !peripheral.On {
			continue
		}
		peripheralEnergy += peripheral.EnergyPerMillisecond * float64(duration) / float64(time.Millisecond)
	}

	t.Now += duration
	t.EnergyNanos += cpuEnergyPerMillisecond*float64(duration)/float64(time.Millisecond) + peripheralEnergy
}

func (t *Timeline) SaveSnapshot() ([]byte, error) {
	return json.MarshalIndent(t, "", "  ")
}

func LoadSnapshot(data []byte) (*Timeline, error) {
	timeline := NewTimeline(PowerProfile{})
	if err := json.Unmarshal(data, timeline); err != nil {
		return nil, err
	}
	if timeline.Peripherals == nil {
		timeline.Peripherals = make(map[string]*Peripheral)
	}
	if timeline.CompletedEvents == nil {
		timeline.CompletedEvents = make(map[string]int)
	}
	return timeline, nil
}
