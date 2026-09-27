package main

// GUI 主体：控件布局、事件绑定、状态刷新与系统托盘。

import (
	"fmt"
	"math"
	"time"

	"github.com/lxn/walk"

	"imutool/glass"
)

// 布局常量：参数区各行共用同一组列宽，保证两个分组框内容左右对齐。
const (
	labelWidth = 150 // 行首说明文字宽度
	valueWidth = 96  // 滑块右侧当前值宽度
)

// ui 持有窗口控件与运行时对象，所有界面操作都发生在 walk 的消息循环线程上。
type ui struct {
	mw  *walk.MainWindow
	ni  *walk.NotifyIcon
	ico *walk.Icon

	connLabel  *walk.Label
	stateLabel *walk.Label
	hintLabel  *walk.Label

	sensRow *sliderRow
	dzRow   *sliderRow
	invertX *walk.CheckBox
	invertY *walk.CheckBox

	startBtn *walk.PushButton
	resetBtn *walk.PushButton

	tracker   *Tracker
	params    Params
	saveTimer *time.Timer
	trayHint  bool
}

// newUI 构建窗口与全部控件；构造失败属于环境异常，直接 panic（用 must 包装）。
func newUI(p Params) *ui {
	a := &ui{params: p, tracker: NewTracker(p)}

	mw, err := walk.NewMainWindow()
	if err != nil {
		panic(err)
	}
	a.mw = mw
	_ = mw.SetTitle("Rayneo Air1S 头追")
	_ = mw.SetMinMaxSize(walk.Size{Width: 470, Height: 320}, walk.Size{})
	_ = mw.SetClientSize(walk.Size{Width: 480, Height: 332})
	_ = mw.SetLayout(walk.NewVBoxLayout())

	a.ico = loadAppIcon()
	_ = mw.SetIcon(a.ico)

	a.addStatusBox()
	a.addParamBox()
	a.addButtonRow()
	a.addTray()

	// 先套用设置再挂事件，避免初始化时的赋值触发保存
	a.applyParams(p)
	a.attachEvents()

	// 首次连接检测在 UI 线程内同步完成（Run 之前不能调用 Synchronize）
	infos, err := findGlassDevices()
	a.applyConn(err == nil && len(infos) > 0, productOf(infos))
	return a
}

// addStatusBox 构建状态显示区。
func (a *ui) addStatusBox() {
	box := newGroupBox(a.mw, "状态")
	a.connLabel = newStatusLabel(box, "设备：未连接")
	a.stateLabel = newStatusLabel(box, "状态：未运行")
	a.hintLabel = newStatusLabel(box, "提示：连接眼镜后「启动」按钮才可用")
}

// addParamBox 构建参数设置区。
func (a *ui) addParamBox() {
	box := newGroupBox(a.mw, "控制参数")
	a.sensRow = newSliderRow(box, "灵敏度（像素/度）", 1, 300, 1, " px/°")
	a.dzRow = newSliderRow(box, "死区（度）", 0, 30, 0.1, " °")
	a.invertX = newCheckBox(box, "反转 X 轴（左右）")
	a.invertY = newCheckBox(box, "反转 Y 轴（上下）")
}

// addButtonRow 构建按钮行。
func (a *ui) addButtonRow() {
	row := must(walk.NewComposite(a.mw))
	_ = row.SetLayout(walk.NewHBoxLayout())
	a.startBtn = newButton(row, "启动", a.toggleRun)
	a.resetBtn = newButton(row, "复位视角", a.doReset)
	newButton(row, "最小化到托盘", a.hideToTray)
}

// addTray 创建常显的托盘图标及其菜单。
func (a *ui) addTray() {
	a.ni = must(walk.NewNotifyIcon(a.mw))
	_ = a.ni.SetIcon(a.ico)
	_ = a.ni.SetToolTip("Rayneo Air1S 头追")

	show := walk.NewAction()
	_ = show.SetText("显示主界面")
	show.Triggered().Attach(a.showWindow)

	quit := walk.NewAction()
	_ = quit.SetText("退出")
	quit.Triggered().Attach(func() { _ = a.mw.Close() })

	_ = a.ni.ContextMenu().Actions().Add(show)
	_ = a.ni.ContextMenu().Actions().Add(quit)
	a.ni.MouseDown().Attach(func(_, _ int, button walk.MouseButton) {
		if button == walk.LeftButton {
			a.showWindow()
		}
	})
	_ = a.ni.SetVisible(true)
}

// attachEvents 绑定界面事件。
func (a *ui) attachEvents() {
	a.sensRow.Changed().Attach(a.onParamsChanged)
	a.dzRow.Changed().Attach(a.onParamsChanged)
	a.invertX.CheckedChanged().Attach(a.onParamsChanged)
	a.invertY.CheckedChanged().Attach(a.onParamsChanged)

	// 点击标题栏最小化按钮时收进托盘
	a.mw.SizeChanged().Attach(func() {
		if isIconic(uintptr(a.mw.Handle())) {
			a.hideToTray()
		}
	})
	a.mw.Closing().Attach(func(_ *bool, _ walk.CloseReason) { a.shutdown() })
}

// applyParams 把参数写入控件。
func (a *ui) applyParams(p Params) {
	a.sensRow.SetValue(p.Sensitivity)
	a.dzRow.SetValue(p.DeadzoneDeg)
	a.invertX.SetChecked(p.InvertX)
	a.invertY.SetChecked(p.InvertY)
}

// readParams 读取控件当前值（滑块已由 SetRange 限制范围，无需再校验）。
func (a *ui) readParams() Params {
	return Params{
		DeadzoneDeg: a.dzRow.Value(),
		Sensitivity: a.sensRow.Value(),
		InvertX:     a.invertX.Checked(),
		InvertY:     a.invertY.Checked(),
	}
}

// onParamsChanged 参数变更：立即作用于运行时，延迟落盘。
func (a *ui) onParamsChanged() {
	p := a.readParams()
	a.params = p
	a.tracker.SetParams(p)
	if a.saveTimer != nil {
		a.saveTimer.Stop()
	}
	// 落盘用的是捕获的副本，避免定时器协程与界面线程共享状态
	a.saveTimer = time.AfterFunc(saveDelay, func() {
		if err := saveParams(p); err != nil {
			a.mw.Synchronize(func() { a.setHint("设置保存失败：" + err.Error()) })
		}
	})
}

// toggleRun 在「启动 / 停止」之间切换。
func (a *ui) toggleRun() {
	if a.tracker.Running() {
		a.tracker.Stop()
		a.setHint("已停止：已关闭眼镜传感器推送")
		a.refresh(a.tracker.Status())
		return
	}
	a.tracker.SetParams(a.readParams())
	if err := a.tracker.Start(); err != nil {
		a.setHint("启动失败：" + err.Error())
		return
	}
	a.setHint(fmt.Sprintf("已启动：请保持静止约 %.1f 秒完成陀螺零偏校准",
		float64(glass.GyroCalibSamples)/sampleHz))
}

// doReset 把当前姿态设为角度中位。
func (a *ui) doReset() {
	a.tracker.RequestReset()
	a.setHint("已复位：以当前姿态为角度中位（等价于热键 Ctrl+Alt+R）")
}

// hideToTray 隐藏窗口，程序继续在托盘中运行。
func (a *ui) hideToTray() {
	a.mw.Hide()
	a.setHint("已最小化到托盘：单击托盘图标可恢复窗口")
	if a.trayHint {
		return
	}
	a.trayHint = true
	_ = a.ni.ShowInfo("Rayneo Air1S 头追", "程序已最小化到托盘继续运行，单击图标可恢复窗口。")
}

// showWindow 从托盘恢复窗口（若处于最小化状态先还原）。
func (a *ui) showWindow() {
	hwnd := uintptr(a.mw.Handle())
	if isIconic(hwnd) {
		restoreWindow(hwnd)
	}
	a.mw.Show()
	_ = a.mw.Activate()
}

// shutdown 在窗口关闭时停止采集、落盘并移除托盘图标。
func (a *ui) shutdown() {
	a.tracker.Stop()
	if a.saveTimer != nil {
		a.saveTimer.Stop()
	}
	_ = saveParams(a.params)
	_ = a.ni.Dispose()
}

// setHint 更新提示行。
func (a *ui) setHint(text string) {
	_ = a.hintLabel.SetText("提示：" + text)
}

// refresh 按运行时状态刷新界面（由 watch 协程经 Synchronize 调用）。
func (a *ui) refresh(st Status) {
	switch {
	case st.Err != "":
		a.startBtn.SetText("启动")
		a.startBtn.SetEnabled(false)
		a.resetBtn.SetEnabled(false)
		a.stateLabel.SetText("状态：已停止")
		a.setHint(st.Err)
		a.tracker.Stop() // 清理句柄并清空状态，下次刷新即回到「未运行」
	case st.Running && st.Calibrating:
		a.startBtn.SetText("停止")
		a.resetBtn.SetEnabled(false)
		a.stateLabel.SetText(fmt.Sprintf("状态：陀螺零偏校准中 %d/%d（请保持静止）",
			st.CalFrames, glass.GyroCalibSamples))
	case st.Running:
		a.startBtn.SetText("停止")
		a.resetBtn.SetEnabled(true)
		a.stateLabel.SetText(fmt.Sprintf("姿态：yaw %+.2f°   pitch %+.2f°   帧 %d",
			st.YawDeg, st.PitchDeg, st.Frames))
	default:
		a.startBtn.SetText("启动")
		a.resetBtn.SetEnabled(false)
		a.stateLabel.SetText("状态：未运行")
	}
}

// applyConn 更新连接状态与「启动」按钮的可用性。
func (a *ui) applyConn(connected bool, product string) {
	name := "未连接"
	if connected {
		name = "已连接"
		if product != "" {
			name += " " + product
		}
	}
	_ = a.connLabel.SetText("设备：" + name)
	a.startBtn.SetEnabled(connected)
}

// watch 以 5Hz 刷新状态显示，并在空闲时约每秒检测一次设备接入情况。
func (a *ui) watch() {
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	idleTicks := 0
	for range tick.C {
		if !a.tracker.Running() {
			idleTicks++
			if idleTicks >= 5 {
				idleTicks = 0
				a.detectAsync()
			}
		}
		st := a.tracker.Status()
		a.mw.Synchronize(func() { a.refresh(st) })
	}
}

// detectAsync 在后台枚举设备（枚举较慢，避免阻塞界面线程）。
func (a *ui) detectAsync() {
	infos, err := findGlassDevices()
	connected := err == nil && len(infos) > 0
	a.mw.Synchronize(func() { a.applyConn(connected, productOf(infos)) })
}

// productOf 返回首个设备的产品名。
func productOf(infos []glass.DeviceInfo) string {
	if len(infos) == 0 {
		return ""
	}
	return infos[0].Product
}

// must 用于包装构造控件等理论上不会失败的操作。
func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

// newGroupBox 创建分组框（宽度由 walk 布局按可伸展的子控件决定）。
func newGroupBox(parent walk.Container, title string) *walk.GroupBox {
	box := must(walk.NewGroupBox(parent))
	_ = box.SetTitle(title)
	_ = box.SetLayout(walk.NewVBoxLayout())
	return box
}

// newLabel 创建文本标签。
func newLabel(parent walk.Container, text string) *walk.Label {
	l := must(walk.NewLabel(parent))
	_ = l.SetText(text)
	return l
}

// newStatusLabel 创建状态区标签。
// 必须开启末尾省略号：walk 中只有「可收缩」的标签才会让所在分组框横向撑满，
// 否则分组框会按文字宽度居中，与「控制参数」分组框宽度不一致。
func newStatusLabel(parent walk.Container, text string) *walk.Label {
	l := newLabel(parent, text)
	_ = l.SetEllipsisMode(walk.EllipsisEnd)
	return l
}

// newCheckBox 创建复选框。复选框自身横向可伸展，默认会在分组框内居中，
// 这里固定为左上对齐，与左侧说明文字列保持一致。
func newCheckBox(parent walk.Container, text string) *walk.CheckBox {
	cb := must(walk.NewCheckBox(parent))
	_ = cb.SetText(text)
	_ = cb.SetAlignment(walk.AlignHNearVNear)
	return cb
}

// newButton 创建按钮并绑定点击事件。
func newButton(parent walk.Container, text string, onClick func()) *walk.PushButton {
	b := must(walk.NewPushButton(parent))
	_ = b.SetText(text)
	b.Clicked().Attach(onClick)
	return b
}

// sliderRow 是「说明 + 滑块 + 当前值」的一行，用于灵敏度与死区。
// 滑块只取整数，故用 scale 把整数值换算为实际值（如死区步长 0.1°）。
type sliderRow struct {
	slider *walk.Slider
	value  *walk.Label
	scale  float64
	suffix string
}

// newSliderRow 创建一行滑块；min/max 为实际值范围，scale 为每格对应的实际值增量。
func newSliderRow(parent walk.Container, title string, min, max, scale float64, suffix string) *sliderRow {
	row := must(walk.NewComposite(parent))
	rowLayout := walk.NewHBoxLayout()
	// 去掉行内边距，使各行的说明文字与分组框内的复选框左边缘对齐
	_ = rowLayout.SetMargins(walk.Margins{})
	_ = row.SetLayout(rowLayout)

	label := newLabel(row, title)
	_ = label.SetMinMaxSize(walk.Size{Width: labelWidth}, walk.Size{Width: labelWidth})

	r := &sliderRow{scale: scale, suffix: suffix}
	r.slider = must(walk.NewSlider(row))
	r.slider.SetRange(int(math.Round(min/scale)), int(math.Round(max/scale)))
	r.slider.SetLineSize(1)
	r.slider.SetPageSize(10)
	r.slider.SetTracking(true) // 拖动过程中即生效，无需松开鼠标
	r.slider.ValueChanged().Attach(r.syncValue)

	r.value = newLabel(row, "")
	_ = r.value.SetTextAlignment(walk.AlignFar)
	// 固定宽度，避免数值位数变化时整行抖动
	_ = r.value.SetMinMaxSize(walk.Size{Width: valueWidth}, walk.Size{Width: valueWidth})

	r.syncValue()
	return r
}

// Value 返回滑块对应的实际值。
func (r *sliderRow) Value() float64 { return float64(r.slider.Value()) * r.scale }

// SetValue 把实际值换算成滑块位置写入。
func (r *sliderRow) SetValue(v float64) { r.slider.SetValue(int(math.Round(v / r.scale))) }

// Changed 返回滑块值变化事件。
func (r *sliderRow) Changed() *walk.Event { return r.slider.ValueChanged() }

// syncValue 刷新右侧数值显示。
func (r *sliderRow) syncValue() {
	_ = r.value.SetText(fmt.Sprintf("%.1f%s", r.Value(), r.suffix))
}
