package main

import (
	"os"
	"testing"
)

// 设置持久化往返：保存后再读回应得到相同参数。
func TestParamsRoundTrip(t *testing.T) {
	p := defaultParams()
	p.Device = DeviceOT
	p.Sensitivity = 123.5
	p.InvertX = true
	p.DSU.Port = 26761
	p.DSU.Slot = 2
	p.DSU.Invert = [3]bool{true, false, true}
	p.OT.DeadzoneDeg = 3.5

	if err := saveParams(p); err != nil {
		t.Fatalf("saveParams: %v", err)
	}
	if got := loadParams(); got != p {
		t.Errorf("loadParams() = %+v, want %+v", got, p)
	}
}

// 文件损坏时应回退到默认参数，而不是让程序无法启动。
func TestLoadParamsCorrupted(t *testing.T) {
	if err := os.WriteFile(settingsPath(), []byte("{invalid json"), 0o644); err != nil {
		t.Fatalf("write corrupted settings: %v", err)
	}
	if got := loadParams(); got != defaultParams() {
		t.Errorf("损坏文件未回退到默认值：got %+v, want %+v", got, defaultParams())
	}
}
