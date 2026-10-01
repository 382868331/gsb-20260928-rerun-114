package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"
)

type CPUState string

const (
	CPUActive        CPUState = "active"
	CPUSleep         CPUState = "sleep"
	CPUWaking        CPUState = "waking"
	CPUEnteringSleep CPUState = "entering-sleep"
	CPUWaiting       CPUState = "waiting-for-energy"
)

type EventType string

const (
	EventSample EventType = "sample"
	EventBurst  EventType = "burst"
)

type PowerProfile struct {
	ActiveEnergyPerMillisecond float64       `json:"activeEnergyPerMillisecond"`
	SleepEnergyPerMillisecond  float64       `json:"sleepEnergyPerMillisecond"`
	SleepEntryDuration         time.Duration `json:"sleepEntryDuration,omitempty"`
	SleepEntryEnergyNanos      float64       `json:"sleepEntryEnergyNanos,omitempty"`
	WakeDuration               time.Duration `json:"wakeDuration"`
	WakeEnergyNanos            float64       `json:"wakeEnergyNanos"`
}

type PowerDomain struct {
	Name                 string  `json:"name"`
	EnergyPerMillisecond float64 `json:"energyPerMillisecond"`
	On                   bool    `json:"on"`
}

type Peripheral struct {
	Name                 string   `json:"name"`
	Domain               string   `json:"domain"`
	DependsOn            []string `json:"dependsOn,omitempty"`
	EnergyPerMillisecond float64  `json:"energyPerMillisecond"`
	On                   bool     `json:"on"`
	TransferEnd          int64    `json:"transferEnd"`
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
	Now                         time.Duration           `json:"now"`
	EnergyNanos                 float64                 `json:"energyNanos"`
	CPUState                    CPUState                `json:"cpuState"`
	WakeAt                      time.Duration           `json:"wakeAt"`
	TransitionEnd               time.Duration           `json:"transitionEnd,omitempty"`
	TransitionStart             time.Duration           `json:"transitionStart,omitempty"`
	WaitResume                  string                  `json:"waitResume,omitempty"`
	WaitEnd                     time.Duration           `json:"waitEnd,omitempty"`
	WaitCostNanos               float64                 `json:"waitCostNanos,omitempty"`
	PendingPowerOff             []string                `json:"pendingPowerOff,omitempty"`
	PendingWakeReasons          []string                `json:"pendingWakeReasons,omitempty"`
	Profile                     PowerProfile            `json:"profile"`
	Domains                     map[string]*PowerDomain `json:"domains"`
	Peripherals                 map[string]*Peripheral  `json:"peripherals"`
	Events                      []Event                 `json:"events"`
	Records                     []Record                `json:"records"`
	ActiveDuration              time.Duration           `json:"activeDuration"`
	SleepDuration               time.Duration           `json:"sleepDuration"`
	WakeDuration                time.Duration           `json:"wakeDurationTotal"`
	SleepEntryDuration          time.Duration           `json:"sleepEntryDurationTotal,omitempty"`
	WaitDuration                time.Duration           `json:"waitDuration,omitempty"`
	EnergyBudgetNanos           float64                 `json:"energyBudgetNanos,omitempty"`
	RechargeNanosPerMillisecond float64                 `json:"rechargeNanosPerMillisecond,omitempty"`
	BudgetLimited               bool                    `json:"budgetLimited,omitempty"`
	CPUWork                     time.Duration           `json:"cpuWork"`
	TransferDuration            time.Duration           `json:"transferDuration"`
	CompletedEvents             map[string]int          `json:"completedEvents"`
	WakeReasons                 []string                `json:"wakeReasons"`
	RecoveryCount               int                     `json:"recoveryCount"`
}

const CoreDomain = "core"

func NewTimeline(profile PowerProfile) *Timeline {
	return &Timeline{
		CPUState:        CPUActive,
		Profile:         profile,
		Domains:         map[string]*PowerDomain{CoreDomain: {Name: CoreDomain}},
		Peripherals:     make(map[string]*Peripheral),
		CompletedEvents: make(map[string]int),
	}
}

func NewTimelineWithBudget(profile PowerProfile, budgetNanos, rechargeNanosPerMillisecond float64) *Timeline {
	t := NewTimeline(profile)
	t.EnergyBudgetNanos = budgetNanos
	t.RechargeNanosPerMillisecond = rechargeNanosPerMillisecond
	t.BudgetLimited = true
	return t
}

func (t *Timeline) AddPowerDomain(name string, energyPerMillisecond float64) {
	t.Domains[name] = &PowerDomain{Name: name, EnergyPerMillisecond: energyPerMillisecond}
}

func (t *Timeline) AddPeripheral(name string, energyPerMillisecond float64) {
	t.AddDomainPeripheral(name, CoreDomain, energyPerMillisecond)
}

func (t *Timeline) AddDomainPeripheral(name, domain string, energyPerMillisecond float64, dependsOn ...string) {
	if t.Domains[domain] == nil {
		t.Domains[domain] = &PowerDomain{Name: domain}
	}
	t.Peripherals[name] = &Peripheral{
		Name:                 name,
		Domain:               domain,
		DependsOn:            dependsOn,
		EnergyPerMillisecond: energyPerMillisecond,
	}
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
	targets, err := t.resolveDomains(peripherals)
	if err != nil {
		return err
	}
	if err := t.checkPowerOffBlocked(targets); err != nil {
		return err
	}

	t.WakeAt = wakeAt
	t.PendingPowerOff = targets
	if t.Profile.SleepEntryDuration > 0 {
		t.CPUState = CPUEnteringSleep
		t.TransitionStart = t.Now
		t.TransitionEnd = t.Now + t.Profile.SleepEntryDuration
		t.Records = append(t.Records, Record{
			At:       t.Now,
			Kind:     "sleep-entry",
			Duration: t.Profile.SleepEntryDuration,
		})
	} else {
		t.beginSleep()
	}
	return nil
}

func (t *Timeline) resolveDomains(names []string) ([]string, error) {
	seen := map[string]bool{}
	targets := []string{}
	add := func(domain string) {
		if !seen[domain] {
			seen[domain] = true
			targets = append(targets, domain)
		}
	}
	for _, name := range names {
		if t.Domains[name] != nil {
			add(name)
			continue
		}
		peripheral := t.Peripherals[name]
		if peripheral == nil {
			return nil, fmt.Errorf("unknown peripheral or domain %q", name)
		}
		add(peripheral.Domain)
	}
	return targets, nil
}

func (t *Timeline) checkPowerOffBlocked(targets []string) error {
	targeted := map[string]bool{}
	for _, domain := range targets {
		targeted[domain] = true
	}
	for _, peripheral := range t.Peripherals {
		if !peripheral.transferring(t.Now) {
			continue
		}
		if targeted[peripheral.Domain] {
			return fmt.Errorf("cannot power off domain %q: %q transfer finishes at %s",
				peripheral.Domain, peripheral.Name, time.Duration(peripheral.TransferEnd))
		}
		for _, dependency := range peripheral.DependsOn {
			dep := t.Peripherals[dependency]
			if dep != nil && targeted[dep.Domain] {
				return fmt.Errorf("cannot power off domain %q: %q needs it until transfer finishes at %s",
					dep.Domain, peripheral.Name, time.Duration(peripheral.TransferEnd))
			}
		}
	}
	return nil
}

func (t *Timeline) beginSleep() {
	for _, domain := range t.PendingPowerOff {
		t.powerOffDomain(domain)
	}
	t.PendingPowerOff = nil
	t.CPUState = CPUSleep
	t.Records = append(t.Records, Record{At: t.Now, Kind: "sleep", Duration: t.WakeAt - t.Now})
}

func (t *Timeline) powerOffDomain(name string) {
	domain := t.Domains[name]
	if domain == nil {
		return
	}
	domain.On = false
	for _, peripheral := range t.Peripherals {
		if peripheral.Domain == name {
			peripheral.On = false
			peripheral.TransferEnd = 0
		}
	}
}

func (t *Timeline) powerOnDomain(name string) {
	if domain := t.Domains[name]; domain != nil {
		domain.On = true
	}
}

func (t *Timeline) RunUntil(horizon time.Duration) error {
	sort.SliceStable(t.Events, func(i, j int) bool { return t.Events[i].At < t.Events[j].At })

	for t.Now < horizon {
		switch t.CPUState {
		case CPUEnteringSleep:
			target := minDuration(t.TransitionEnd, horizon)
			t.advance(target - t.Now)
			if t.Now >= t.TransitionEnd {
				t.finishSleepEntry()
			}
		case CPUSleep:
			target := t.WakeAt
			if nextEventAt, hasEvent := t.nextEventAt(); hasEvent && nextEventAt < target {
				target = nextEventAt
			}
			t.advance(minDuration(target, horizon) - t.Now)
			if t.Now >= horizon {
				return nil
			}
			if t.Now >= t.WakeAt || t.firstDueEvent() >= 0 {
				t.beginWake()
			}
		case CPUWaking:
			t.advance(minDuration(t.TransitionEnd, horizon) - t.Now)
			if t.Now >= t.TransitionEnd {
				if err := t.finishWake(); err != nil {
					return err
				}
			}
		case CPUWaiting:
			t.advance(minDuration(t.WaitEnd, horizon) - t.Now)
			if t.Now >= t.WaitEnd {
				t.finishWait()
			}
		case CPUActive:
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
		default:
			return fmt.Errorf("processor is %s at %s", t.CPUState, t.Now)
		}
	}
	return nil
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
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

func (t *Timeline) finishSleepEntry() {
	t.SleepEntryDuration += t.TransitionEnd - t.TransitionStart
	t.EnergyNanos += t.Profile.SleepEntryEnergyNanos
	t.beginSleep()
}

func (t *Timeline) beginWake() {
	due := t.dueEvents()
	reasons := make([]string, 0, len(due))
	for _, event := range due {
		reasons = append(reasons, event.Reason)
	}
	if len(reasons) == 0 {
		reasons = append(reasons, "scheduled-wakeup")
	}
	t.PendingWakeReasons = reasons
	t.CPUState = CPUWaking
	t.TransitionStart = t.Now
	t.TransitionEnd = t.Now + t.Profile.WakeDuration
	t.Records = append(t.Records, Record{
		At:       t.Now,
		Kind:     "wake",
		Reasons:  reasons,
		Duration: t.Profile.WakeDuration,
	})
}

func (t *Timeline) finishWake() error {
	t.RecoveryCount++
	t.WakeDuration += t.TransitionEnd - t.TransitionStart
	t.WakeReasons = append(t.WakeReasons, t.PendingWakeReasons...)
	t.PendingWakeReasons = nil
	t.EnergyNanos += t.Profile.WakeEnergyNanos
	t.CPUState = CPUActive
	return nil
}

func (t *Timeline) beginWait(cost float64) {
	t.CPUState = CPUWaiting
	t.WaitCostNanos = cost
	shortfall := cost - t.EnergyBudgetNanos
	wait := time.Duration(math.Ceil(shortfall/t.RechargeNanosPerMillisecond) * float64(time.Millisecond))
	t.WaitEnd = t.Now + wait
	t.WaitResume = "events"
	t.Records = append(t.Records, Record{
		At:          t.Now,
		Kind:        "energy-wait",
		Duration:    wait,
		EnergyNanos: shortfall,
		Detail:      "energy budget depleted, waiting for recharge",
	})
}

func (t *Timeline) finishWait() {
	t.CPUState = CPUActive
	t.WaitResume = ""
	t.WaitEnd = 0
	t.WaitCostNanos = 0
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
	var cpuWork time.Duration
	var maxTransfer time.Duration
	powerOffPeripheral := map[string]bool{}
	requiredDomains := map[string]bool{}
	activePeripherals := map[string]bool{}
	for _, event := range group {
		if event.Peripheral != "" {
			peripheral := t.Peripherals[event.Peripheral]
			if peripheral == nil {
				return fmt.Errorf("event %q uses unknown peripheral %q", event.Reason, event.Peripheral)
			}
			requiredDomains[peripheral.Domain] = true
			for _, dependency := range peripheral.DependsOn {
				dep := t.Peripherals[dependency]
				if dep == nil {
					return fmt.Errorf("event %q: %q depends on unknown peripheral %q", event.Reason, event.Peripheral, dependency)
				}
				requiredDomains[dep.Domain] = true
			}
			activePeripherals[event.Peripheral] = true
			if event.TransferDuration > maxTransfer {
				maxTransfer = event.TransferDuration
			}
			if previous, exists := powerOffPeripheral[event.Peripheral]; !exists {
				powerOffPeripheral[event.Peripheral] = event.PowerOff
			} else if previous && !event.PowerOff {
				powerOffPeripheral[event.Peripheral] = false
			}
		}
		cpuWork += event.CPUWork
	}

	duration := cpuWork
	if maxTransfer > duration {
		duration = maxTransfer
	}

	cost := t.energyFor(duration, CPUActive)
	for domain := range requiredDomains {
		if d := t.Domains[domain]; d != nil {
			cost += d.EnergyPerMillisecond * float64(duration) / float64(time.Millisecond)
		}
	}
	for name := range activePeripherals {
		cost += t.Peripherals[name].EnergyPerMillisecond * float64(duration) / float64(time.Millisecond)
	}
	if t.BudgetLimited && cost > t.EnergyBudgetNanos {
		t.beginWait(cost)
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
	reasons := make([]string, 0, len(group))
	for _, event := range group {
		t.CPUWork += event.CPUWork
		t.TransferDuration += event.TransferDuration
		t.CompletedEvents[string(event.Type)]++
		reasons = append(reasons, event.Reason)
		if event.Peripheral == "" {
			continue
		}
		peripheral := t.Peripherals[event.Peripheral]
		peripheral.On = true
		if event.TransferDuration > 0 {
			peripheral.TransferEnd = int64(start + event.TransferDuration)
		}
	}

	for domain := range requiredDomains {
		t.powerOnDomain(domain)
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
	for name, shouldPowerOff := range powerOffPeripheral {
		if shouldPowerOff {
			peripheral := t.Peripherals[name]
			if t.domainStillNeeded(peripheral.Domain) {
				continue
			}
			t.powerOffDomain(peripheral.Domain)
		}
	}
	return nil
}

func (t *Timeline) domainStillNeeded(domain string) bool {
	for _, peripheral := range t.Peripherals {
		if peripheral.Domain == domain && peripheral.transferring(t.Now) {
			return true
		}
		for _, dependency := range peripheral.DependsOn {
			dep := t.Peripherals[dependency]
			if dep != nil && dep.Domain == domain && peripheral.transferring(t.Now) {
				return true
			}
		}
	}
	return false
}

func (t *Timeline) advance(duration time.Duration) {
	if duration <= 0 {
		return
	}

	spend := t.energyFor(duration, t.CPUState)
	recharge := 0.0
	if t.BudgetLimited {
		recharge = t.RechargeNanosPerMillisecond * float64(duration) / float64(time.Millisecond)
	}

	switch t.CPUState {
	case CPUActive, CPUWaiting:
		t.ActiveDuration += duration
	case CPUSleep:
		t.SleepDuration += duration
	}

	if t.CPUState == CPUWaiting {
		t.WaitDuration += duration
	}

	domainEnergy := 0.0
	onDomains := map[string]bool{}
	for _, peripheral := range t.Peripherals {
		if peripheral.On && peripheral.Domain != "" {
			onDomains[peripheral.Domain] = true
		}
	}
	for domain := range onDomains {
		if d := t.Domains[domain]; d != nil && d.On {
			domainEnergy += d.EnergyPerMillisecond * float64(duration) / float64(time.Millisecond)
		}
	}

	peripheralEnergy := 0.0
	for _, peripheral := range t.Peripherals {
		if !peripheral.On {
			continue
		}
		peripheralEnergy += peripheral.EnergyPerMillisecond * float64(duration) / float64(time.Millisecond)
	}

	t.Now += duration
	t.EnergyNanos += spend + domainEnergy + peripheralEnergy
	if t.BudgetLimited {
		if t.CPUState == CPUActive {
			t.EnergyBudgetNanos -= spend + domainEnergy + peripheralEnergy
			if t.EnergyBudgetNanos < 0 {
				t.EnergyBudgetNanos = 0
			}
		} else {
			t.EnergyBudgetNanos += recharge
		}
	}
}

func (t *Timeline) energyFor(duration time.Duration, state CPUState) float64 {
	cpuEnergyPerMillisecond := 0.0
	switch state {
	case CPUActive, CPUWaiting:
		cpuEnergyPerMillisecond = t.Profile.ActiveEnergyPerMillisecond
	case CPUSleep:
		cpuEnergyPerMillisecond = t.Profile.SleepEnergyPerMillisecond
	}
	return cpuEnergyPerMillisecond * float64(duration) / float64(time.Millisecond)
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
	if timeline.Domains == nil {
		timeline.Domains = make(map[string]*PowerDomain)
	}
	return timeline, nil
}
