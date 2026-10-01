# 低功耗设备仿真系统

这个 Go 模块用纯时间线模型模拟处理器、外设和唤醒事件，不依赖硬件。

## 模型

- 时间单位使用 `time.Duration`，能耗累计单位为纳焦（nJ）。
- 处理器状态为 `active`、`entering-sleep`、`sleep`、`waking`、`waiting-for-energy`。
- 外设挂在具名电源域上（`AddPowerDomain`、`AddDomainPeripheral`）；旧的 `AddPeripheral` 使用内置 `core` 域。
- 外设在注册时可声明唤醒/传输依赖：`radio` 传输未结束时，它依赖的 `sensor` 所在电源域不允许关闭。
- `SleepUntil` 接受外设名或电源域名；只有被点名的域参与关闭，其他独立域保持原状。
- 进入休眠显式占用 `SleepEntryDuration`、消耗 `SleepEntryEnergyNanos`，恢复显式占用 `WakeDuration`、消耗 `WakeEnergyNanos`。
- `NewTimelineWithBudget` 给系统一个能量预算：预算不足时处理器进入 `waiting-for-energy`，按 `RechargeNanosPerMillisecond` 等待能量恢复，等待期间待处理事件不执行也不丢失，充电完成后只处理一次。
- 休眠进入与恢复都是可中断、可保存的过程：途中 `SaveSnapshot` 会保留 `TransitionStart`/`TransitionEnd`、待关闭域和待处理唤醒原因，恢复后继续走完剩余过程，不会直接跳到已唤醒。
- 事件按时间排序；同一时刻的多个原因合并为一次恢复和一个并发处理组。
- `SleepUntil` 会拒绝关闭仍有未完成传输、或仍被其他传输中设备依赖的电源域。
- `SaveSnapshot`/`LoadSnapshot` 使用 JSON 保存完整状态，可恢复后继续调度和演练。

## 核对场景

`main.go` 中 `sensor` 与 `radio` 位于独立电源域，`radio` 依赖 `sensor`。20ms 时周期采样与突发同时到达，50ms 时再来一次周期采样。供能预算只够第一批工作，第二批到达时进入充电等待。

85ms 时的账目为：

- 总时间：85ms
- CPU 工作量：5ms
- 活跃：56ms（含能量等待），休眠：23ms，进入休眠：2ms，恢复：4ms，能量等待：33ms
- 恢复次数：2，第一次保留两个唤醒原因
- 总能耗：196.8µJ

运行：

```sh
go test ./...
go run .
```

增加设备类型时，只需创建外设实例或事件类型并挂到同一时间线，不需要改变时间推进、快照和能耗累计逻辑。
