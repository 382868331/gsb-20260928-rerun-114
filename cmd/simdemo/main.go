package main

import (
	"fmt"
	"time"

	"lowpower/sim"
)

func main() {
	cfg := sim.ScenarioConfig{
		FirstSample: 10 * time.Millisecond,
		Period:      10 * time.Millisecond,
		Horizon:     100 * time.Millisecond,
		Bursts: []sim.BurstEvent{
			{At: 10*time.Millisecond + 100*time.Microsecond, Reason: "gpio.line"},
			{At: 35*time.Millisecond + 60*time.Microsecond, Reason: "radio.packet"},
			{At: 35*time.Millisecond + 70*time.Microsecond, Reason: "gpio.line"},
		},
	}

	// Uninterrupted reference run.
	ref := sim.NewDevice(sim.DefaultConfig())
	run := sim.NewScenario(ref, cfg)
	run.Finish()

	// Forked run: save after the first sample, then continue.
	fork := sim.NewDevice(sim.DefaultConfig())
	scn := sim.NewScenario(fork, cfg)
	snap := scn.TakeSnapshot(19 * time.Millisecond)
	cont, contDev := sim.Restore(snap)
	cont.Finish()

	var tracePJ int64
	for _, seg := range ref.Segments() {
		tracePJ += seg.Power * int64(seg.End-seg.Start)
	}

	fmt.Println("低功耗设备仿真 — 首轮：休眠进入 / 唤醒 / 能耗累计")
	fmt.Printf("时间轴长度:      %v\n", ref.TL.Now())
	fmt.Printf("完成工作量:      %d 个任务\n", ref.Workload())
	fmt.Printf("恢复(唤醒)次数:  %d\n", ref.Recoveries())
	fmt.Printf("能量账(积分器):  %d pJ (%.9f J)\n", ref.Meter.TotalPicoJoules(), ref.Meter.TotalJoules())
	fmt.Printf("能量账(轨迹重算): %d pJ\n", tracePJ)
	fmt.Printf("快照续演能量:    %d pJ\n", contDev.Meter.TotalPicoJoules())
	fmt.Printf("快照续演工作量:  %d, 唤醒 %d\n", contDev.Workload(), contDev.Recoveries())
	if contDev.Meter.TotalPicoJoules() != ref.Meter.TotalPicoJoules() ||
		contDev.Workload() != ref.Workload() || contDev.Recoveries() != ref.Recoveries() {
		fmt.Println("对账失败: 续演与全程不一致")
	} else {
		fmt.Println("对账一致: 保存状态后续演与全程演练完全相同")
	}
}
