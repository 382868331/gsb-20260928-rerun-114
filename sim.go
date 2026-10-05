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
	// CPUSleeping is the sleep-entry transition: the processor is still
	// powered while peripherals finish draining, and a snapshot taken here
	// resumes the entry instead of jumping straight to sleep.
	CPUSleeping CPUState = "sleeping"
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
	// SupplyRecoveryAt is when a constrained supply recovers; before it the
	// SupplyEnergyPerMillisecond cap applies, at and after it the
	// RecoveredSupplyEnergyPerMillisecond cap applies (0 means unlimited).
	SupplyRecoveryAt                    time.Duration `json:"supplyRecoveryAt,omitempty"`
	RecoveredSupplyEnergyPerMillisecond float64       `json:"recoveredSupplyEnergyPerMillisecond,omitempty"`
	// SleepEntryDuration models a non-instantaneous transition into sleep.
	// SleepEntryEnergyNanos is the total transition cost, accumulated
	// proportionally to progress: an aborted entry keeps the share already
	// spent (no refund, no double charge on resume), and a completed entry
	// has charged exactly the full amount.
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
	// Deadline, when > 0, is the latest completion time for the request.
	Deadline time.Duration `json:"deadline,omitempty"`
	// Urgent work interrupts an in-progress sleep entry.
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
	Now                 time.Duration           `json:"now"`
	EnergyNanos         float64                 `json:"energyNanos"`
	CPUState            CPUState                `json:"cpuState"`
	WakeAt              time.Duration           `json:"wakeAt"`
	Profile             PowerProfile            `json:"profile"`
	Peripherals         map[string]*Peripheral  `json:"peripherals"`
	Events              []Event                 `json:"events"`
	Records             []Record                `json:"records"`
	ActiveDuration      time.Duration           `json:"activeDuration"`
	SleepDuration       time.Duration           `json:"sleepDuration"`
	WakeDuration        time.Duration           `json:"wakeDurationTotal"`
	CPUWork             time.Duration           `json:"cpuWork"`
	TransferDuration    time.Duration           `json:"transferDuration"`
	WaitDuration        time.Duration           `json:"waitDuration"`
	CompletedEvents     map[string]int          `json:"completedEvents"`
	WakeReasons         []string                `json:"wakeReasons"`
	RecoveryCount       int                     `json:"recoveryCount"`
	Domains             map[string]*PowerDomain `json:"domains,omitempty"`
	WakeRemaining       time.Duration           `json:"wakeRemaining,omitempty"`
	WakeStartAt         time.Duration           `json:"wakeStartAt,omitempty"`
	WakePendingReasons  []string                `json:"wakePendingReasons,omitempty"`
	WakePendingEnergy   float64                 `json:"wakePendingEnergy,omitempty"`
	SleepRemaining      time.Duration           `json:"sleepRemaining,omitempty"`
	SleepStartAt        time.Duration           `json:"sleepStartAt,omitempty"`
	SleepPendingWakeAt  time.Duration           `json:"sleepPendingWakeAt,omitempty"`
	SleepPendingOff     []string                `json:"sleepPendingOff,omitempty"`
	SleepPendingDomains []string                `json:"sleepPendingDomains,omitempty"`
	SleepEntryDuration  time.Duration           `json:"sleepEntryDurationTotal,omitempty"`
	CompletedOnTime     []string                `json:"completedOnTime,omitempty"`
	MissedDeadlines     []string                `json:"missedDeadlines,omitempty"`
	// LazyWake 为 true 时休眠只在事件到达时唤醒，不为截止时间提前唤醒。
	LazyWake bool `json:"lazyWake,omitempty"`
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
		// Non-instantaneous entry: keep everything powered, remember the
		// pending shutdown, and let RunUntil advance the transition.
		names := make([]string, 0, len(shutdown))
		for name := range shutdown {
			names = append(names, name)
		}
		sort.Strings(names)
		domains := make([]string, 0, len(domainsOff))
		for name := range domainsOff {
			domains = append(domains, name)
		}
		sort.Strings(domains)
		t.CPUState = CPUSleeping
		t.SleepRemaining = t.Profile.SleepEntryDuration
		t.SleepStartAt = t.Now
		t.SleepPendingWakeAt = wakeAt
		t.SleepPendingOff = names
		t.SleepPendingDomains = domains
		return nil
	}
	t.powerOffAll(shutdown, domainsOff)
	t.enterSleep(wakeAt)
	return nil
}

func (t *Timeline) powerOffAll(shutdown map[string]bool, domainsOff map[string]bool) {
	for name := range shutdown {
		peripheral := t.Peripherals[name]
		peripheral.On = false
		peripheral.TransferEnd = 0
	}
	for name := range domainsOff {
		t.Domains[name].On = false
	}
}

func (t *Timeline) enterSleep(wakeAt time.Duration) {
	t.CPUState = CPUSleep
	t.WakeAt = wakeAt
	t.Records = append(t.Records, Record{At: t.Now, Kind: "sleep", Duration: wakeAt - t.Now})
}

// finishSleepEntry applies the pending shutdown and charges the entry cost.
func (t *Timeline) finishSleepEntry() {
	shutdown := map[string]bool{}
	for _, name := range t.SleepPendingOff {
		shutdown[name] = true
	}
	domainsOff := map[string]bool{}
	for _, name := range t.SleepPendingDomains {
		domainsOff[name] = true
	}
	t.powerOffAll(shutdown, domainsOff)
	t.Records = append(t.Records, Record{
		At:          t.SleepStartAt,
		Kind:        "sleep-entry",
		Duration:    t.Profile.SleepEntryDuration,
		EnergyNanos: t.Profile.SleepEntryEnergyNanos,
	})
	t.SleepPendingOff = nil
	t.SleepPendingDomains = nil
	t.enterSleep(t.SleepPendingWakeAt)
}

// abortSleepEntry cancels an in-progress entry for urgent work. Energy
// already spent is not refunded and the pending shutdown is discarded;
// domains that were already off stay off. It returns the entry progress
// as a fraction in [0,1] so the wake back only pays for how far the
// processor actually sank toward sleep.
func (t *Timeline) abortSleepEntry() float64 {
	progress := 1.0
	if total := t.Profile.SleepEntryDuration; total > 0 {
		progress = float64(total-t.SleepRemaining) / float64(total)
	}
	t.Records = append(t.Records, Record{
		At:          t.Now,
		Kind:        "sleep-entry-aborted",
		Duration:    t.Now - t.SleepStartAt,
		EnergyNanos: progress * t.Profile.SleepEntryEnergyNanos,
		Detail:      "urgent work interrupted the sleep entry",
	})
	t.SleepRemaining = 0
	t.SleepPendingOff = nil
	t.SleepPendingDomains = nil
	t.CPUState = CPUActive
	return progress
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
		case CPUSleeping:
			if t.hasDueUrgentEvent() {
				progress := t.abortSleepEntry()
				if err := t.beginWake(); err != nil {
					return err
				}
				// 尚未真正睡着：唤醒只按进入进度比例支付，
				// 不无条件收取完整唤醒过程。
				t.WakeRemaining = time.Duration(progress * float64(t.Profile.WakeDuration))
				t.WakePendingEnergy = progress * t.Profile.WakeEnergyNanos
				continue
			}
			step := t.SleepRemaining
			if remaining := horizon - t.Now; remaining < step {
				step = remaining
			}
			// Stop at the next event so urgent work can interrupt the entry.
			if nextEventAt, hasEvent := t.nextEventAt(); hasEvent && nextEventAt > t.Now && nextEventAt-t.Now < step {
				step = nextEventAt - t.Now
			}
			t.advance(step)
			t.SleepRemaining -= step
			if t.SleepRemaining == 0 {
				t.finishSleepEntry()
			}
			continue
		case CPUSleep:
			target := t.sleepWakeTarget()
			if target > horizon {
				target = horizon
			}
			t.advance(target - t.Now)
			if t.CPUState == CPUSleep && t.Now >= t.sleepWakeTarget() {
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

// sleepWakeTarget is when the processor should start waking: the scheduled
// wake time, or the next event — moved earlier by the wake latency for
// deadline work so processing can start when the event arrives.
func (t *Timeline) sleepWakeTarget() time.Duration {
	target := t.WakeAt
	if len(t.Events) > 0 {
		event := t.Events[0]
		wakeFor := event.At
		if event.Deadline > 0 && !t.LazyWake {
			wakeFor = event.At - t.Profile.WakeDuration
		}
		if wakeFor < target {
			target = wakeFor
		}
	}
	if target < t.Now {
		target = t.Now
	}
	return target
}

func (t *Timeline) hasDueUrgentEvent() bool {
	for _, event := range t.Events {
		if event.At <= t.Now && event.Urgent {
			return true
		}
	}
	return false
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
		// An early wake for deadline work names the event it wakes for.
		if len(t.Events) > 0 && t.Events[0].At <= t.Now+t.Profile.WakeDuration {
			groupTime := t.Events[0].At
			for _, event := range t.Events {
				if event.At == groupTime {
					reasons = append(reasons, event.Reason)
				}
			}
		}
	}
	if len(reasons) == 0 {
		reasons = append(reasons, "scheduled-wakeup")
	}

	t.CPUState = CPUWaking
	t.WakeRemaining = t.Profile.WakeDuration
	t.WakeStartAt = t.Now
	t.WakePendingReasons = reasons
	t.WakePendingEnergy = t.Profile.WakeEnergyNanos
	t.RecoveryCount++
	t.WakeReasons = append(t.WakeReasons, reasons...)
	return nil
}

func (t *Timeline) finishWake() {
	wakeDuration := t.Now - t.WakeStartAt
	t.EnergyNanos += t.WakePendingEnergy
	t.Records = append(t.Records, Record{
		At:          t.WakeStartAt,
		Kind:        "wake",
		Reasons:     t.WakePendingReasons,
		Duration:    wakeDuration,
		EnergyNanos: t.WakePendingEnergy,
	})
	t.WakePendingReasons = nil
	t.WakePendingEnergy = 0
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
	completion := t.Now
	for _, event := range group {
		if event.Deadline <= 0 {
			continue
		}
		if completion <= event.Deadline {
			t.CompletedOnTime = append(t.CompletedOnTime, event.Reason)
			continue
		}
		t.MissedDeadlines = append(t.MissedDeadlines, event.Reason)
		t.Records = append(t.Records, Record{
			At:     completion,
			Kind:   "deadline-missed",
			Detail: event.Reason,
		})
	}
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
	case CPUSleeping:
		// Still fully powered while the entry drains.
		cpuEnergyPerMillisecond = t.Profile.ActiveEnergyPerMillisecond
		t.SleepEntryDuration += duration
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
	if t.CPUState == CPUSleeping && t.Profile.SleepEntryDuration > 0 {
		// 过渡耗能按实际推进量累积：中途打断已消耗部分不退回，
		// 快照重开后只继续支付剩余部分，不重复计算。
		energy += t.Profile.SleepEntryEnergyNanos * float64(duration) / float64(t.Profile.SleepEntryDuration)
	}
	if supply := t.supplyCap(); supply > 0 {
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

// supplyCap returns the current supply limit, accounting for recovery.
func (t *Timeline) supplyCap() float64 {
	if t.Profile.SupplyRecoveryAt > 0 && t.Now >= t.Profile.SupplyRecoveryAt {
		return t.Profile.RecoveredSupplyEnergyPerMillisecond
	}
	return t.Profile.SupplyEnergyPerMillisecond
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
	// Snapshots written before wake energy was tracked per-transition
	// resume with the full profile charge.
	if timeline.CPUState == CPUWaking && timeline.WakePendingEnergy == 0 {
		timeline.WakePendingEnergy = timeline.Profile.WakeEnergyNanos
	}
	return timeline, nil
}
