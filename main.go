package main

// Rayneo Air 1s 头追程序（Go + Walk）
//
// 功能：读取眼镜 IMU → 姿态解算 → 注入鼠标相对位移。GUI 可调灵敏度、死区与
// 反转轴，设置自动保存并在下次启动时加载；未检测到设备时「启动」按钮不可用；
// 运行中可最小化到系统托盘。
//
// 使用前提（与 imutool 一致）：
//  1. 关闭官方「雷鸟 Mirror Studio」，避免 HID 读取队列冲突；
//  2. 若目标程序以管理员身份运行，本程序也需以管理员身份启动，否则输入注入会被 UIPI 拦截；
//  3. 启动后请保持眼镜静止约 3.3 秒，等待陀螺零偏校准完成。

import "syscall"

// 窗口状态操作：walk 未提供最小化相关的接口，直接调用 user32。
var (
	user32DLL      = syscall.NewLazyDLL("user32.dll")
	procIsIconic   = user32DLL.NewProc("IsIconic")
	procShowWindow = user32DLL.NewProc("ShowWindow")
)

// swRestore 对应 SW_RESTORE，用于把最小化的窗口还原。
const swRestore = 9

// isIconic 判断窗口当前是否处于最小化状态。
func isIconic(hwnd uintptr) bool {
	r, _, _ := procIsIconic.Call(hwnd)
	return r != 0
}

// restoreWindow 把最小化的窗口还原为正常状态。
func restoreWindow(hwnd uintptr) {
	_, _, _ = procShowWindow.Call(hwnd, swRestore)
}

func main() {
	a := newUI(loadParams())
	// walk 的 Run 只跑消息循环、不会自动显示窗口，需显式显示并置前
	a.mw.Show()
	_ = a.mw.Activate()
	// 状态刷新与设备检测放在后台协程，界面更新经 Synchronize 回到消息循环线程
	go a.watch()
	a.mw.Run()
}
