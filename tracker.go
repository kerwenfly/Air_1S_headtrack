package main

// 头追运行时：与 imutool 的 mouse 模式复用同一套算法（glass 包），
// 差别在于参数可运行中热更新，且状态经互斥量暴露给 GUI 轮询显示。

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"imutool/glass"
)

const (
	ffalconVID = 0x1BBB
	ffalconPID = 0xAF50

	// DeviceMouse 把姿态换算成鼠标位移；DeviceDSU 以 DSU 协议向模拟器提供运动数据；
	// DeviceOT 以 opentrack 的 UDP 协议把姿态角发送给 opentrack。
	DeviceMouse = "mouse"
	DeviceDSU   = "dsu"
	DeviceOT    = "opentrack"
)

// findGlassDevices 枚举 VID/PID 匹配的眼镜 HID 接口。
func findGlassDevices() ([]glass.DeviceInfo, error) {
	return glass.Enumerate(func(vid, pid uint16) bool {
		return vid == ffalconVID && pid == ffalconPID
	})
}

// openGlasses 打开第一台眼镜设备。
func openGlasses() (*glass.Device, error) {
	infos, err := findGlassDevices()
	if err != nil {
		return nil, err
	}
	if len(infos) == 0 {
		return nil, fmt.Errorf("未找到眼镜设备 VID_%04X&PID_%04X", ffalconVID, ffalconPID)
	}
	return glass.OpenPath(infos[0].Path)
}

// writeCmd 下发一条无参命令。
func writeCmd(dev *glass.Device, cmd byte) error {
	_, err := dev.Write(glass.BuildCommand(cmd))
	return err
}

// applyDisplay 把显示档位换算为面板档位并下发（0x09 亮度命令）。
// 档位越界（含未设置的 0）时跳过，保持眼镜当前显示不变；
// 写入失败视为瞬时错误静默忽略，数据流异常会由读循环统一上报。
func applyDisplay(dev *glass.Device, p *Params) {
	m := glass.BrightnessModeFor(p.DisplayLv)
	if m == nil {
		return
	}
	_, _ = dev.Write(glass.BuildCommand(glass.CmdPanelLuminance, byte(m.Panel)))
}

// DSUParams 是 DSU 服务端参数（模拟 NS Pro 手柄姿态传感器时使用）。
type DSUParams struct {
	Port   int     `json:"port"`   // 监听端口
	Slot   int     `json:"slot"`   // 槽位（1~4，对应模拟器的 pad:0~3）
	Invert [3]bool `json:"invert"` // 轴向取反（pitch/yaw/roll），陀螺与加速度同步生效
}

// OTParams 是 opentrack 模式参数。
type OTParams struct {
	Port        int     `json:"port"`         // 目标端口（opentrack 输入插件「UDP over network」的监听端口）
	DeadzoneDeg float64 `json:"deadzone_deg"` // 死区（度），抑制微小抖动
	Invert      [3]bool `json:"invert"`       // yaw / pitch / roll 取反（轴向校正）
}

// Params 是可运行中调整的控制参数，同时也是持久化到磁盘的设置项。
type Params struct {
	Device      string    `json:"device"`       // 模拟设备：mouse | dsu | opentrack
	DisplayLv   int       `json:"display_lv"`   // 眼镜显示档位（glass.BrightnessModes 序号 1~13，0 表示未设置）
	DeadzoneDeg float64   `json:"deadzone_deg"` // 死区（度）
	Sensitivity float64   `json:"sensitivity"`  // 灵敏度（像素/度）
	InvertX     bool      `json:"invert_x"`     // 反转 X 轴（左右）
	InvertY     bool      `json:"invert_y"`     // 反转 Y 轴（上下）
	DSU         DSUParams `json:"dsu"`          // DSU 模式参数
	OT          OTParams  `json:"opentrack"`    // opentrack 模式参数
}

// mouseConfig 转换为 glass 包的鼠标控制参数。
func (p Params) mouseConfig() glass.MouseConfig {
	return glass.MouseConfig{
		DeadzoneDeg: p.DeadzoneDeg,
		Sensitivity: p.Sensitivity,
		InvertX:     p.InvertX,
		InvertY:     p.InvertY,
	}
}

// dsuConfig 转换为 glass 包的 DSU 服务端参数。
func (p Params) dsuConfig() glass.DSUConfig {
	return glass.DSUConfig{
		Port:   p.DSU.Port,
		Slot:   p.DSU.Slot,
		Invert: p.DSU.Invert,
	}
}

// dsuPortOrDefault 返回当前参数下合法的 DSU 监听端口。
// 范围判定统一由 glass.DSUConfig.PortOrDefault 提供，避免在界面层重复一份常量。
func (p Params) dsuPortOrDefault() int { return p.dsuConfig().PortOrDefault() }

// dsuSlotOrDefault 返回当前参数下合法的 DSU 槽位号（1~4）。
func (p Params) dsuSlotOrDefault() int { return p.dsuConfig().SlotIndex() + 1 }

// otPoseConfig 转换为 glass 包的 opentrack 姿态换算参数。
func (p Params) otPoseConfig() glass.OTPoseConfig {
	return glass.OTPoseConfig{DeadzoneDeg: p.OT.DeadzoneDeg, Invert: p.OT.Invert}
}

// otPortOrDefault 返回当前参数下合法的 opentrack 目标端口。
func (p Params) otPortOrDefault() int { return glass.OTPortOrDefault(p.OT.Port) }

// Status 是供 GUI 显示的状态快照。
type Status struct {
	Running     bool
	Calibrating bool       // 陀螺零偏校准中，此时尚未接管鼠标
	CalFrames   int        // 已累计的校准帧数
	YawDeg      float64    // 相对中位的累计 yaw 偏差
	PitchDeg    float64    // 相对中位的累计 pitch 偏差
	RollDeg     float64    // 相对中位的累计 roll 偏差（opentrack 模式）
	Frames      int64      // 已解析的 IMU 帧数
	Peers       int        // DSU 模式下的订阅者数量
	Sent        int64      // opentrack 模式下已发送的报文数
	Q           [4]float32 // AHRS 姿态四元数 (x, y, z, w)，供姿态立方体预览；未解算时为零
	Err         string     // 非空表示会话因异常结束
}

// Tracker 管理一次「启动 → 停止」的完整会话。
type Tracker struct {
	opMu sync.Mutex // 串行化 Start/Stop/SetParams 对会话资源的访问：
	// Stop 可能由 watch 协程（会话出错）与界面线程（停止按钮/关窗）并发调用，
	// 无锁时会双关设备句柄、对已置空的 dev 解引用而 panic。

	params  atomic.Pointer[Params]
	stop    atomic.Bool
	reset   atomic.Bool
	running atomic.Bool

	mu sync.Mutex
	st Status

	dev    *glass.Device
	srv    *glass.DSUServer // 仅 DSU 模式使用
	ot     *glass.OTSender  // 仅 opentrack 模式使用
	doneCh chan struct{}
}

// NewTracker 创建运行时对象，初始参数为 p。
func NewTracker(p Params) *Tracker {
	t := &Tracker{}
	t.params.Store(&p)
	return t
}

// SetParams 更新参数，立即作用于下一个采样帧。
// DSU 模式的轴向取反在此热更新到服务端；端口与槽位只在下次启动时生效；
// 显示档位变化时立即下发到运行中的会话（未运行则由下次 Start 应用）。
func (t *Tracker) SetParams(p Params) {
	old := t.params.Load()
	t.params.Store(&p)
	// srv 只在 opMu 保护下读写，避免与并发的 Stop（置空 srv）竞争
	t.opMu.Lock()
	defer t.opMu.Unlock()
	if t.srv != nil {
		t.srv.SetConfig(p.dsuConfig())
	}
	if t.dev != nil && old.DisplayLv != p.DisplayLv {
		applyDisplay(t.dev, &p)
	}
}

// RequestReset 请求把当前姿态设为角度中位（等价于 Ctrl+Alt+R 热键）。
func (t *Tracker) RequestReset() { t.reset.Store(true) }

// Running 返回会话是否进行中。
func (t *Tracker) Running() bool { return t.running.Load() }

// Status 返回当前状态快照。
func (t *Tracker) Status() Status {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.st
}

// update 在锁保护下修改状态。
func (t *Tracker) update(f func(*Status)) {
	t.mu.Lock()
	f(&t.st)
	t.mu.Unlock()
}

// Start 打开设备并启动采集协程；失败时返回原因。
func (t *Tracker) Start() error {
	t.opMu.Lock()
	defer t.opMu.Unlock()
	if !t.running.CompareAndSwap(false, true) {
		return fmt.Errorf("已在运行中")
	}
	dev, err := openGlasses()
	if err != nil {
		t.running.Store(false)
		return err
	}
	// 按所选模式先建立对外输出端；失败时立即释放设备
	p := t.params.Load()
	if p.Device == DeviceDSU {
		srv := glass.NewDSUServer(p.dsuConfig())
		if err := srv.Start(); err != nil {
			dev.Close()
			t.running.Store(false)
			return fmt.Errorf("启动 DSU 服务端失败：%w", err)
		}
		t.srv = srv
	}
	if p.Device == DeviceOT {
		sender := glass.NewOTSender()
		if err := sender.Start(p.otPortOrDefault()); err != nil {
			dev.Close()
			t.running.Store(false)
			return fmt.Errorf("连接 opentrack 失败：%w", err)
		}
		t.ot = sender
	}
	t.dev = dev
	t.stop.Store(false)
	t.reset.Store(false)
	t.doneCh = make(chan struct{})
	t.update(func(s *Status) { *s = Status{Running: true, Calibrating: true} })
	go t.loop(dev, t.doneCh)
	return nil
}

// Stop 停止会话：先让读循环退出，再关闭传感器推送并释放句柄。
// 可从任意协程并发调用（watch 的错误清理与界面线程的停止/关窗）：
// opMu 保证清理只执行一次，第二个进入者会看到 running 已复位而直接返回。
// 传感器持续上报，读循环会在数毫秒内自然退出，因此无需关闭句柄去打断读取。
func (t *Tracker) Stop() {
	t.opMu.Lock()
	defer t.opMu.Unlock()
	if !t.running.Load() {
		return
	}
	t.stop.Store(true)
	select {
	case <-t.doneCh:
		// 读循环已自然退出，句柄仍有效：正常关闭传感器推送
		_ = writeCmd(t.dev, glass.CmdSensorOutputOff)
	case <-time.After(2 * time.Second):
		// 兜底：数据流异常停滞时强行唤醒阻塞的读取；
		// 此时句柄已关闭，不再补发关闭命令（下次打开设备时会重新初始化）
		t.dev.Close()
		<-t.doneCh
	}
	t.dev.Close()
	t.dev = nil
	if t.srv != nil {
		t.srv.Stop()
		t.srv = nil
	}
	if t.ot != nil {
		t.ot.Stop()
		t.ot = nil
	}
	t.running.Store(false)
	t.update(func(s *Status) { *s = Status{} })
}

// session 承载一次采集会话中跨帧保持的状态。
// ahrs 供鼠标与 opentrack 模式解算姿态，ctrl 仅鼠标模式使用，
// srv 仅 DSU 模式使用，pose / ot 仅 opentrack 模式使用，
// dispAhrs 仅 DSU 模式使用（不做鼠标接管，仅供界面姿态预览）。
type session struct {
	dev      *glass.Device
	srv      *glass.DSUServer
	ot       *glass.OTSender
	pose     *glass.OTPoseTracker
	ahrs     *glass.AHRS
	dispAhrs *glass.AHRS
	ctrl     *glass.MouseController
	hot      glass.HotkeyWatcher

	cal    glass.GyroCalib
	bias   [3]float32 // 陀螺零偏（rad/s）
	dt     float32    // 本帧采样间隔（秒）
	prev   uint32     // 上一帧时间戳
	frames int64
	peers  int

	buf []byte
}

// newSession 按所选设备准备各模式需要的对象。
func (t *Tracker) newSession(dev *glass.Device, p *Params) *session {
	s := &session{dev: dev, buf: make([]byte, dev.InputReportLen)}
	switch p.Device {
	case DeviceDSU:
		// DSU 模式直接推送原始数据，不做姿态解算；dispAhrs 仅供界面姿态预览
		s.srv = t.srv
		s.dispAhrs = glass.NewAHRS()
	case DeviceOT:
		// opentrack 模式需要姿态解算，再把姿态角发给 opentrack
		s.ahrs = glass.NewAHRS()
		s.pose = glass.NewOTPoseTracker(p.otPoseConfig())
		s.ot = t.ot
	default:
		// 姿态解算与光标控制器仅鼠标模式需要
		s.ahrs = glass.NewAHRS()
		s.ctrl = glass.NewMouseController(p.mouseConfig())
	}
	return s
}

// loop 是采集主循环：解析每帧 → 零偏估计 → 按所选设备分发 → 统一发布状态。
// 状态只在这一处发布，各模式返回自己的快照，避免分支里各写一遍而漂移。
func (t *Tracker) loop(dev *glass.Device, doneCh chan struct{}) {
	defer close(doneCh)

	if err := writeCmd(dev, glass.CmdSensorOutputOn); err != nil {
		t.update(func(s *Status) { s.Err = "启用传感器失败：" + err.Error() })
		return
	}

	p := t.params.Load()
	s := t.newSession(dev, p)
	// 与官方 XRSDK_Init 一致：传感器开启后按设置下发显示档位
	applyDisplay(dev, p)
	for {
		n, err := s.dev.Read(s.buf)
		if t.stop.Load() {
			return
		}
		if err != nil {
			t.update(func(st *Status) { st.Err = "读取中断：" + err.Error() })
			return
		}
		f := glass.ParseIMU(s.buf[:n])
		if f == nil {
			continue
		}
		s.frames++
		// 零偏估计：连续静止足够帧数后提交均值
		if s.cal.Feed(f.Gyro) {
			s.bias = s.cal.Bias
		}
		// 采样间隔：时间戳原始值单位为 100µs，官方 dt = Δraw / 10000 秒
		s.dt = glass.TimestampToSeconds(s.prev, f.Stamp, glass.DefaultSampleHz)
		s.prev = f.Stamp

		var st Status
		switch p.Device {
		case DeviceDSU:
			st = t.stepDSU(s, f)
		case DeviceOT:
			st = t.stepOT(s, f)
		default:
			st = t.stepMouse(s, f)
		}
		t.update(func(cur *Status) { *cur = st })
	}
}

// stepDSU 处理一帧 DSU 推送并返回该帧的状态快照。
// 零偏未就绪前不推送：此时角速度含完整零偏，模拟器会看到「静止仍在持续旋转」，
// 并据此污染其内部姿态基准与零偏自估。这与鼠标模式「校准完成前不接管」保持一致。
func (t *Tracker) stepDSU(s *session, f *glass.IMUFrame) Status {
	// 复位：界面按钮或 Ctrl+Alt+R 热键。DSU 模式没有姿态中位的概念，
	// 视角漂移来自零偏估计误差，重新校准零偏即等价的「复位」；
	// 校准期间暂停推送，完成前模拟器收到的数据为空、视角保持不动。
	if t.reset.CompareAndSwap(true, false) || s.hot.Pressed() {
		s.cal.Restart()
	}
	// 立方体预览的绝对姿态（含校准期，零偏未就绪时会有缓慢漂转，无碍预览）
	q := s.dispAhrs.Update(glass.SubBias(f.Gyro, s.bias), f.Accel, s.dt)
	if s.srv != nil {
		if s.cal.Done {
			// 时间戳 100µs → µs
			s.srv.Push(f.Accel, glass.RadToDeg(glass.SubBias(f.Gyro, s.bias)), uint64(f.Stamp)*100)
		}
		if s.frames%32 == 1 { // 订阅者数量无需每帧刷新，降低加锁频次
			s.peers = s.srv.Subscribers()
		}
	}
	return Status{
		Running:     true,
		Calibrating: !s.cal.Done,
		CalFrames:   s.cal.Count(),
		Frames:      s.frames,
		Peers:       s.peers,
		Q:           q,
	}
}

// stepMouse 处理一帧姿态解算与光标控制并返回该帧的状态快照。
func (t *Tracker) stepMouse(s *session, f *glass.IMUFrame) Status {
	q := s.ahrs.Update(glass.SubBias(f.Gyro, s.bias), f.Accel, s.dt)

	// 零偏未就绪时姿态不可靠，先不接管鼠标
	if !s.ctrl.Ready() {
		if s.cal.Done {
			s.ctrl.Reset(q)
		}
		return Status{
			Running:     true,
			Calibrating: !s.cal.Done,
			CalFrames:   s.cal.Count(),
			Frames:      s.frames,
			Q:           q,
		}
	}

	// 复位：界面按钮或 Ctrl+Alt+R 热键
	if t.reset.CompareAndSwap(true, false) || s.hot.Pressed() {
		s.ctrl.Reset(q)
	}
	s.ctrl.SetConfig(t.params.Load().mouseConfig())
	glass.MoveMouseRel(s.ctrl.Move(q))

	return Status{
		Running:   true,
		CalFrames: glass.GyroCalibSamples,
		Frames:    s.frames,
		YawDeg:    s.ctrl.YawDeg,
		PitchDeg:  s.ctrl.PitchDeg,
		Q:         q,
	}
}

// stepOT 处理一帧姿态解算并把姿态角发给 opentrack，返回该帧的状态快照。
// 与鼠标模式一致：零偏未就绪时姿态不可靠、先不发送，校准完成后以当前姿态为角度中位。
func (t *Tracker) stepOT(s *session, f *glass.IMUFrame) Status {
	q := s.ahrs.Update(glass.SubBias(f.Gyro, s.bias), f.Accel, s.dt)

	if !s.pose.Ready() {
		if s.cal.Done {
			s.pose.Reset(q, f.Accel)
		}
		return Status{
			Running:     true,
			Calibrating: !s.cal.Done,
			CalFrames:   s.cal.Count(),
			Frames:      s.frames,
			Q:           q,
		}
	}

	// 复位：界面按钮或 Ctrl+Alt+R 热键
	if t.reset.CompareAndSwap(true, false) || s.hot.Pressed() {
		s.pose.Reset(q, f.Accel)
	}
	s.pose.SetConfig(t.params.Load().otPoseConfig())
	pose := s.pose.Angles(q, f.Accel)
	s.ot.Push(pose)

	return Status{
		Running:   true,
		CalFrames: glass.GyroCalibSamples,
		Frames:    s.frames,
		YawDeg:    pose.Yaw,
		PitchDeg:  pose.Pitch,
		RollDeg:   pose.Roll,
		Sent:      int64(s.ot.Sent()),
		Q:         q,
	}
}
