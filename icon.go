package main

// 程序图标：编译期把 glass.png 嵌入二进制，供窗口标题栏、任务栏与系统托盘共用。
// exe 自身的资源图标（资源管理器/任务栏固定项）由 rsrc 打入 headtrack.ico + manifest。

import (
	"bytes"
	_ "embed"
	"image"
	_ "image/png"

	"github.com/lxn/walk"
)

// glassPNG 是应用程序图标源文件（256×256，带透明通道）。
//
//go:embed glass.png
var glassPNG []byte

// loadAppIcon 解码嵌入的 PNG 并生成 walk 图标（Windows 会按 DPI 自动缩放）。
func loadAppIcon() *walk.Icon {
	img, _, err := image.Decode(bytes.NewReader(glassPNG))
	if err != nil {
		panic(err)
	}
	return must(walk.NewIconFromImage(img))
}
