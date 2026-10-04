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
	// CPUFallingAsleep is the transition into sleep: it takes time and energy,
	// can be snapshot mid-way, and can be aborted by urgent work.
	CPUFallingAsleep CPUState = "falling-asleep"
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
	// SupplyEnergyPerMillisecond caps the total power draw; 0 means unlimited.
	// When the instantaneous draw exceeds the cap, time stretches (brownout)
	// so that energy is conserved and the extra elapsed time is accounted
	// as WaitDuration.
	SupplyEnergyPerMillisecond float64 `json:"supplyEnergyPerMillisecond,omitempty"`
	// SleepEntryDuration is how long powering down into sleep takes; 0 means
	// the processor enters sleep instantly. SleepEntryEnergyNanos is the total
	// energy charged across the entry, consumed progressively so that an
	// aborted entry keeps the energy already spent.
	SleepEntryDuration    time.Duration `json:"sleepEntryDuration,omitempty"`
	SleepEntryEnergyNanos float64       `json:"sleepEntryEnergyNanos,omitempty"`
}

type PowerDomain struct {
	Name                     string  `json:"name"`
	On                       bool    `json:"on"`
	IdleEnergyPerMillisecond float64 `json:"idleEnergyPerMillisecond"`
}

type Peripheral struct {
	Name                 string   `json:"name"`
	EnergyPerMillisecond float64  `json:"energyPerMillisecond"`
	On                   bool     `json:"on"`
	TransferEnd          int64    `json:"transferEnd"`
	Domain               string   `json:"domain,omitempty"`
	DependsOn            []string `json:"dependsOn,omitempty"`
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
	// Deadline is the absolute time by which the work must finish; 0 means
	// no deadline. Work that cannot finish in time is recorded as missed
	// and does not wake the device on its own.
	Deadline time.Duration `json:"deadline,omitempty"`
	// Urgent work interrupts an ongoing sleep entry; the energy already
	// spent on the entry is not refunded.
	Urgent bool `json:"urgent,omitempty"`
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
	Now                time.Duration           `json:"now"`
	EnergyNanos        float64                 `json:"energyNanos"`
	CPUState           CPUState                `json:"cpuState"`
	WakeAt             time.Duration           `json:"wakeAt"`
	Profile            PowerProfile            `json:"profile"`
	Peripherals        map[string]*Peripheral  `json:"peripherals"`
	Events             []Event                 `json:"events"`
	Records            []Record                `json:"records"`
	ActiveDuration     time.Duration           `json:"activeDuration"`
	SleepDuration      time.Duration           `json:"sleepDuration"`
	WakeDuration       time.Duration           `json:"wakeDurationTotal"`
	CPUWork            time.Duration           `json:"cpuWork"`
	TransferDuration   time.Duration           `json:"transferDuration"`
	WaitDuration       time.Duration           `json:"waitDuration"`
	CompletedEvents    map[string]int          `json:"completedEvents"`
	WakeReasons        []string                `json:"wakeReasons"`
	RecoveryCount      int                     `json:"recoveryCount"`
	MissedEvents       map[string]int          `json:"missedEvents,omitempty"`
	Domains            map[string]*PowerDomain `json:"domains,omitempty"`
	WakeRemaining      time.Duration           `json:"wakeRemaining,omitempty"`
	WakeStartAt        time.Duration           `json:"wakeStartAt,omitempty"`
	WakePendingReasons []string                `json:"wakePendingReasons,omitempty"`
	// Sleep-entry progress, kept in snapshots so an interrupted entry resumes.
	SleepRemaining          time.Duration `json:"sleepRemaining,omitempty"`
	SleepEntryStartAt       time.Duration `json:"sleepEntryStartAt,omitempty"`
	SleepPendingWakeAt      time.Duration `json:"sleepPendingWakeAt,omitempty"`
	SleepPendingPeripherals []string      `json:"sleepPendingPeripherals,omitempty"`
	SleepPendingDomains     []string      `json:"sleepPendingDomains,omitempty"`
	FallAsleepDuration      time.Duration `json:"fallAsleepDuration,omitempty"`
}

func NewTimeline(profile PowerProfile) *Timeline {
	return &Timeline{
		CPUState:        CPUActive,
		Profile:         profile,
		Peripherals:     make(map[string]*Peripheral),
		Domains:         make(map[string]*PowerDomain),
		CompletedEvents: make(map[string]int),
	}
}

func (t *Timeline) AddDomain(name string, idleEnergyPerMillisecond float64) {
	t.Domains[name] = &PowerDomain{Name: name, On: true, IdleEnergyPerMillisecond: idleEnergyPerMillisecond}
}

func (t *Timeline) AddPeripheral(name string, energyPerMillisecond float64) {
	t.Peripherals[name] = &Peripheral{Name: name, EnergyPerMillisecond: energyPerMillisecond}
}

func (t *Timeline) AddPeripheralToDomain(name, domain string, energyPerMillisecond float64, dependsOn ...string) error {
	if domain != "" {
		if t.Domains[domain] == nil {
			return fmt.Errorf("unknown power domain %q", domain)
		}
	}
	for _, dep := range dependsOn {
		if t.Peripherals[dep] == nil {
			return fmt.Errorf("unknown dependency peripheral %q", dep)
		}
	}
	t.Peripherals[name] = &Peripheral{
		Name:                 name,
		EnergyPerMillisecond: energyPerMillisecond,
		Domain:               domain,
		DependsOn:            dependsOn,
	}
	return nil
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
	shutdown := map[string]bool{}
	domainsOff := map[string]bool{}
	for _, name := range peripherals {
		if domain, ok := t.Domains[name]; ok {
			domainsOff[domain.Name] = true
			for _, peripheral := range t.Peripherals {
				if peripheral.Domain == name {
					shutdown[peripheral.Name] = true
				}
			}
			continue
		}
		peripheral := t.Peripherals[name]
		if peripheral == nil {
			return fmt.Errorf("unknown peripheral or domain %q", name)
		}
		shutdown[name] = true
	}
	for _, peripheral := range t.Peripherals {
		if !peripheral.transferring(t.Now) {
			continue
		}
		if shutdown[peripheral.Name] {
			return fmt.Errorf("cannot power off %q: transfer finishes at %s", peripheral.Name, time.Duration(peripheral.TransferEnd))
		}
		for _, dep := range peripheral.DependsOn {
			if shutdown[dep] {
				return fmt.Errorf("cannot power off %q: %q depends on it and transfers until %s",
					dep, peripheral.Name, time.Duration(peripheral.TransferEnd))
			}
		}
	}
	if t.Profile.SleepEntryDuration > 0 {
		t.CPUState = CPUFallingAsleep
		t.SleepRemaining = t.Profile.SleepEntryDuration
		t.SleepEntryStartAt = t.Now
		t.SleepPendingWakeAt = wakeAt
		t.SleepPendingPeripherals = t.SleepPendingPeripherals[:0]
		for name := range shutdown {
			t.SleepPendingPeripherals = append(t.SleepPendingPeripherals, name)
		}
		sort.Strings(t.SleepPendingPeripherals)
		t.SleepPendingDomains = t.SleepPendingDomains[:0]
		for name := range domainsOff {
			t.SleepPendingDomains = append(t.SleepPendingDomains, name)
		}
		sort.Strings(t.SleepPendingDomains)
		return nil
	}
	t.applyShutdown(shutdown, domainsOff)
	t.CPUState = CPUSleep
	t.WakeAt = wakeAt
	t.Records = append(t.Records, Record{At: t.Now, Kind: "sleep", Duration: wakeAt - t.Now})
	return nil
}

func (t *Timeline) applyShutdown(shutdown, domainsOff map[string]bool) {
	for name := range shutdown {
		peripheral := t.Peripherals[name]
		peripheral.On = false
		peripheral.TransferEnd = 0
	}
	for name := range domainsOff {
		t.Domains[name].On = false
	}
}

func (t *Timeline) finishSleepEntry() {
	shutdown := map[string]bool{}
	for _, name := range t.SleepPendingPeripherals {
		shutdown[name] = true
	}
	domainsOff := map[string]bool{}
	for _, name := range t.SleepPendingDomains {
		domainsOff[name] = true
	}
	t.applyShutdown(shutdown, domainsOff)
	t.SleepPendingPeripherals = nil
	t.SleepPendingDomains = nil
	t.Records = append(t.Records, Record{
		At:          t.SleepEntryStartAt,
		Kind:        "sleep-entry",
		Duration:    t.Profile.SleepEntryDuration,
		EnergyNanos: t.Profile.SleepEntryEnergyNanos,
	})
	t.CPUState = CPUSleep
	t.WakeAt = t.SleepPendingWakeAt
	t.Records = append(t.Records, Record{At: t.Now, Kind: "sleep", Duration: t.WakeAt - t.Now})
}

// abortSleepEntry cancels an ongoing sleep entry (e.g. urgent work arrived).
// The energy already spent on the entry is kept; the pending shutdown is
// discarded so the device stays powered.
func (t *Timeline) abortSleepEntry(reason string) {
	t.Records = append(t.Records, Record{
		At:       t.SleepEntryStartAt,
		Kind:     "sleep-entry-aborted",
		Duration: t.Now - t.SleepEntryStartAt,
		Detail:   reason,
	})
	t.CPUState = CPUActive
	t.SleepRemaining = 0
	t.SleepPendingPeripherals = nil
	t.SleepPendingDomains = nil
}

func (t *Timeline) RunUntil(horizon time.Duration) error {
	sort.SliceStable(t.Events, func(i, j int) bool { return t.Events[i].At < t.Events[j].At })

	for t.Now < horizon {
		switch t.CPUState {
		case CPUWaking:
			step := t.WakeRemaining
			if remaining := horizon - t.Now; remaining < step {
				step = remaining
			}
			t.advance(step)
			t.WakeRemaining -= step
			if t.WakeRemaining == 0 {
				t.finishWake()
			}
			continue
		case CPUFallingAsleep:
			step := t.SleepRemaining
			if remaining := horizon - t.Now; remaining < step {
				step = remaining
			}
			// Urgent work arriving mid-entry aborts the power-down; the
			// energy already spent on the entry is not refunded.
			if len(t.Events) > 0 && t.Events[0].Urgent && t.Events[0].At <= t.Now+step {
				step = t.Events[0].At - t.Now
				t.advance(step)
				t.SleepRemaining -= step
				t.abortSleepEntry(t.Events[0].Reason)
				continue
			}
			t.advance(step)
			t.SleepRemaining -= step
			if t.SleepRemaining == 0 {
				t.finishSleepEntry()
			}
			continue
		case CPUSleep:
			nextEventAt, hasEvent := t.nextEventAt()
			target := t.WakeAt
			if hasEvent && nextEventAt < target {
				target = nextEventAt
			}
			if target > horizon {
				target = horizon
			}
			t.advance(target - t.Now)
			if t.CPUState != CPUSleep {
				continue
			}
			if t.firstDueEvent() >= 0 {
				// Work whose deadline cannot be met even with an early
				// wake is dropped as missed and does not wake the device.
				if t.dropMissedEvents() {
					if err := t.beginWake(); err != nil {
						return err
					}
				} else if t.Now >= t.WakeAt {
					if err := t.beginWake(); err != nil {
						return err
					}
				}
				continue
			}
			if t.Now >= t.WakeAt {
				if err := t.beginWake(); err != nil {
					return err
				}
			}
			continue
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

func (t *Timeline) beginWake() error {
	due := t.dueEvents()
	reasons := make([]string, 0, len(due))
	for _, event := range due {
		reasons = append(reasons, event.Reason)
	}
	if len(reasons) == 0 {
		reasons = append(reasons, "scheduled-wakeup")
	}

	t.CPUState = CPUWaking
	t.WakeRemaining = t.Profile.WakeDuration
	t.WakeStartAt = t.Now
	t.WakePendingReasons = reasons
	t.RecoveryCount++
	t.WakeReasons = append(t.WakeReasons, reasons...)
	return nil
}

func (t *Timeline) finishWake() {
	wakeDuration := t.Profile.WakeDuration
	t.EnergyNanos += t.Profile.WakeEnergyNanos
	t.Records = append(t.Records, Record{
		At:          t.WakeStartAt,
		Kind:        "wake",
		Reasons:     t.WakePendingReasons,
		Duration:    wakeDuration,
		EnergyNanos: t.Profile.WakeEnergyNanos,
	})
	t.WakePendingReasons = nil
	t.CPUState = CPUActive
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

	feasible := group[:0]
	for _, event := range group {
		if event.Deadline > 0 && t.estimateFinish(event) > event.Deadline {
			t.recordMissed(event)
			continue
		}
		feasible = append(feasible, event)
	}
	group = feasible
	if len(group) == 0 {
		return nil
	}

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
			t.powerOnWithDependencies(event.Peripheral)
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

func (t *Timeline) powerOnWithDependencies(name string) {
	peripheral := t.Peripherals[name]
	if peripheral == nil {
		return
	}
	if peripheral.Domain != "" {
		if domain := t.Domains[peripheral.Domain]; domain != nil {
			domain.On = true
		}
	}
	peripheral.On = true
	for _, dep := range peripheral.DependsOn {
		t.powerOnWithDependencies(dep)
	}
}

// estimateFinish predicts when the event's work would complete from the
// current state, including the remaining wake/entry transition and any
// brownout stretch caused by the supply cap.
func (t *Timeline) estimateFinish(event Event) time.Duration {
	start := t.Now
	switch t.CPUState {
	case CPUWaking:
		start += t.WakeRemaining
	case CPUSleep:
		start += t.Profile.WakeDuration
	}
	work := event.CPUWork
	if event.TransferDuration > work {
		work = event.TransferDuration
	}
	return start + t.stretchedWork(event, work)
}

func (t *Timeline) stretchedWork(event Event, work time.Duration) time.Duration {
	supply := t.Profile.SupplyEnergyPerMillisecond
	if supply <= 0 || work <= 0 {
		return work
	}
	rate := t.Profile.ActiveEnergyPerMillisecond
	if peripheral := t.Peripherals[event.Peripheral]; event.Peripheral != "" && peripheral != nil {
		rate += peripheral.EnergyPerMillisecond
		if domain := t.Domains[peripheral.Domain]; domain != nil {
			rate += domain.IdleEnergyPerMillisecond
		}
	}
	if rate > supply {
		return time.Duration(float64(work) * rate / supply)
	}
	return work
}

// dropMissedEvents removes due events whose deadline can no longer be met
// and reports whether any due work remains worth waking for.
func (t *Timeline) dropMissedEvents() bool {
	index := t.firstDueEvent()
	if index < 0 {
		return false
	}
	groupTime := t.Events[index].At
	remaining := t.Events[:0]
	for _, event := range t.Events {
		if event.At == groupTime && event.Deadline > 0 && t.estimateFinish(event) > event.Deadline {
			t.recordMissed(event)
			continue
		}
		remaining = append(remaining, event)
	}
	t.Events = remaining
	return t.firstDueEvent() >= 0
}

func (t *Timeline) recordMissed(event Event) {
	if t.MissedEvents == nil {
		t.MissedEvents = make(map[string]int)
	}
	t.MissedEvents[string(event.Type)]++
	t.Records = append(t.Records, Record{
		At:      t.Now,
		Kind:    "missed",
		Reasons: []string{event.Reason},
		Detail:  "deadline " + event.Deadline.String(),
	})
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
	case CPUWaking:
		t.WakeDuration += duration
	case CPUFallingAsleep:
		if t.Profile.SleepEntryDuration > 0 {
			cpuEnergyPerMillisecond = t.Profile.SleepEntryEnergyNanos * float64(time.Millisecond) / float64(t.Profile.SleepEntryDuration)
		}
		t.FallAsleepDuration += duration
	}

	peripheralEnergy := 0.0
	for _, peripheral := range t.Peripherals {
		if !peripheral.On {
			continue
		}
		peripheralEnergy += peripheral.EnergyPerMillisecond * float64(duration) / float64(time.Millisecond)
	}
	for _, domain := range t.Domains {
		if !domain.On {
			continue
		}
		peripheralEnergy += domain.IdleEnergyPerMillisecond * float64(duration) / float64(time.Millisecond)
	}

	energy := cpuEnergyPerMillisecond*float64(duration)/float64(time.Millisecond) + peripheralEnergy
	if supply := t.Profile.SupplyEnergyPerMillisecond; supply > 0 {
		rate := energy * float64(time.Millisecond) / float64(duration)
		if rate > supply {
			stretched := time.Duration(float64(duration) * rate / supply)
			t.WaitDuration += stretched - duration
			energy = supply * float64(stretched) / float64(time.Millisecond)
			duration = stretched
		}
	}

	t.Now += duration
	t.EnergyNanos += energy
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
	if timeline.Domains == nil {
		timeline.Domains = make(map[string]*PowerDomain)
	}
	if timeline.CompletedEvents == nil {
		timeline.CompletedEvents = make(map[string]int)
	}
	if timeline.MissedEvents == nil {
		timeline.MissedEvents = make(map[string]int)
	}
	return timeline, nil
}
