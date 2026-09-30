package main

import (
	"sync"
	"testing"

	"imutool/glass"
)

// 参数换算的边界值：非法端口/槽位必须回退到默认值，否则启动时会监听失败。
func TestPortAndSlotFallback(t *testing.T) {
	cases := []struct {
		name        string
		p           Params
		wantDSUPort int
		wantOTPort  int
		wantSlot    int
	}{
		{"合法值保持不变", Params{DSU: DSUParams{Port: 26761, Slot: 3}, OT: OTParams{Port: 5555}}, 26761, 5555, 3},
		{"DSU端口越界回退", Params{DSU: DSUParams{Port: 0}, OT: OTParams{Port: 5555}}, glass.DSUDefaultPort, 5555, 1},
		{"OT端口越界回退", Params{DSU: DSUParams{Port: 26761}, OT: OTParams{Port: 70000}}, 26761, glass.OTDefaultPort, 1},
		{"槽位越界回退", Params{DSU: DSUParams{Port: 26761, Slot: 9}, OT: OTParams{Port: 5555}}, 26761, 5555, 1},
		{"零值全部回退", Params{}, glass.DSUDefaultPort, glass.OTDefaultPort, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.p.dsuPortOrDefault(); got != c.wantDSUPort {
				t.Errorf("dsuPortOrDefault() = %d, want %d", got, c.wantDSUPort)
			}
			if got := c.p.otPortOrDefault(); got != c.wantOTPort {
				t.Errorf("otPortOrDefault() = %d, want %d", got, c.wantOTPort)
			}
			if got := c.p.dsuSlotOrDefault(); got != c.wantSlot {
				t.Errorf("dsuSlotOrDefault() = %d, want %d", got, c.wantSlot)
			}
		})
	}
}

// deviceIndexOf 决定设置回填时下拉框的选中项，未知值必须回退到「鼠标」。
func TestDeviceIndexOf(t *testing.T) {
	cases := []struct {
		device string
		want   int
	}{
		{DeviceMouse, 0},
		{DeviceDSU, 1},
		{DeviceOT, 2},
		{"unknown", 0},
		{"", 0},
	}
	for _, c := range cases {
		if got := deviceIndexOf(c.device); got != c.want {
			t.Errorf("deviceIndexOf(%q) = %d, want %d", c.device, got, c.want)
		}
	}
}

// 回归：Stop 会由 watch 协程与界面线程并发调用，无互斥时会双关句柄或对已置空的
// dev 解引用而 panic。此处并发调用必须安全且最终保持未运行状态。
func TestStopConcurrentSafe(t *testing.T) {
	tr := NewTracker(Params{})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tr.Stop()
		}()
	}
	wg.Wait()
	if tr.Running() {
		t.Error("未启动会话时 Stop 后 Running 不应为 true")
	}
}
