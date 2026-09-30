package main

// 设置持久化：保存在可执行文件同目录的 headtrack.json，随参数变更延迟落盘。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"imutool/glass"
)

// saveDelay 是参数变更后延迟落盘的时长，避免输入过程中频繁写文件。
const saveDelay = 300 * time.Millisecond

// saveMu 串行化落盘：延迟保存定时器的协程与关窗时的同步保存可能并发写同一文件。
var saveMu sync.Mutex

// defaultParams 返回出厂默认参数（与 glass 包保持一致）。
func defaultParams() Params {
	return Params{
		Device:      DeviceMouse,
		DisplayLv:   1, // 官方默认面板档位
		DeadzoneDeg: glass.DefaultDeadzoneDeg,
		Sensitivity: glass.DefaultSensitivity,
		DSU: DSUParams{
			Port: glass.DSUDefaultPort,
			Slot: 1,
		},
		OT: OTParams{
			Port:        glass.OTDefaultPort,
			DeadzoneDeg: glass.OTDefaultDeadzoneDeg,
		},
	}
}

// settingsPath 返回设置文件路径，与可执行文件同目录，便于整包拷贝迁移。
func settingsPath() string {
	exe, err := os.Executable()
	if err != nil {
		return "headtrack.json"
	}
	return filepath.Join(filepath.Dir(exe), "headtrack.json")
}

// loadParams 读取上次保存的参数；文件缺失或损坏时回退到默认值。
func loadParams() Params {
	data, err := os.ReadFile(settingsPath())
	if err != nil {
		return defaultParams()
	}
	p := defaultParams()
	if err := json.Unmarshal(data, &p); err != nil {
		return defaultParams()
	}
	return p
}

// saveParams 把参数写入磁盘。
func saveParams(p Params) error {
	saveMu.Lock()
	defer saveMu.Unlock()
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(settingsPath(), data, 0o644)
}
