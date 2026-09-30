package main

// 姿态立方体：把 AHRS 解算的四元数实时画成旋转线框，直观预览眼镜姿态。
// 渲染只做「四元数旋转 → 透视投影 → GDI 画线」，刷新频率由 startCubeLoop 驱动。

import (
	"github.com/lxn/walk"
	"github.com/lxn/win"
)

// 立方体几何：8 个顶点（半边长 1）与 12 条棱；三条姿态轴带轴名标注。
var (
	cubeVerts = [8][3]float32{
		{-1, -1, -1}, {1, -1, -1}, {1, 1, -1}, {-1, 1, -1},
		{-1, -1, 1}, {1, -1, 1}, {1, 1, 1}, {-1, 1, 1},
	}
	cubeEdges = [12][2]int{
		{0, 1}, {1, 2}, {2, 3}, {3, 0},
		{4, 5}, {5, 6}, {6, 7}, {7, 4},
		{0, 4}, {1, 5}, {2, 6}, {3, 7},
	}
	axisTips = [3][3]float32{{2.2, 0, 0}, {0, 2.2, 0}, {0, 0, 2.2}}
	axisText = [3]string{"X", "Y", "Z"}
	// 与轴画笔同色的标注文字颜色，下标一一对应
	axisColors = [3]walk.Color{
		walk.RGB(255, 92, 92),  // X
		walk.RGB(96, 220, 112), // Y
		walk.RGB(92, 160, 255), // Z
	}

	// camDist 相机到原点的距离（半边长单位），决定透视强弱；远大于立方体尺寸时趋于正交
	camDist = float32(5)
)

// cube 姿态立方体控件：画笔与背景刷只建一次复用；姿态仅由 UI 线程经 setPose 写入。
type cube struct {
	widget   *walk.CustomWidget
	bg       *walk.SolidColorBrush
	edgeNear *walk.CosmeticPen // 朝向相机的棱（亮）
	edgeFar  *walk.CosmeticPen // 背向相机的棱（暗）
	axes     [3]*walk.CosmeticPen
	q        [4]float32 // 当前姿态 (x, y, z, w)
}

// newCube 在 parent 中创建立方体控件；构造失败属于环境异常，用 must 直接 panic。
func newCube(parent walk.Container) *cube {
	c := &cube{q: [4]float32{0, 0, 0, 1}}
	c.widget = must(walk.NewCustomWidgetPixels(parent, win.WS_VISIBLE, c.paint))
	// 双缓冲消除高频重绘闪烁；尺寸变化后立即重绘一次
	c.widget.SetPaintMode(walk.PaintBuffered)
	c.widget.SetInvalidatesOnResize(true)
	// 宽度随面板填满，高度给出下限保证立体感
	c.widget.SetMinMaxSize(walk.Size{Height: 150}, walk.Size{})

	c.bg = must(walk.NewSolidColorBrush(walk.RGB(24, 26, 32)))
	c.edgeNear = must(walk.NewCosmeticPen(walk.PenSolid, walk.RGB(235, 235, 240)))
	c.edgeFar = must(walk.NewCosmeticPen(walk.PenSolid, walk.RGB(108, 112, 122)))
	for i, color := range axisColors {
		c.axes[i] = must(walk.NewCosmeticPen(walk.PenSolid, color))
	}
	return c
}

// setPose 更新姿态（须在 UI 线程调用）；全零四元数表示无数据，回正为单位姿态。
func (c *cube) setPose(q [4]float32) {
	if q == ([4]float32{}) {
		q = [4]float32{0, 0, 0, 1}
	}
	c.q = q
}

// paint 绘制一帧：清背景 → 旋转投影顶点 → 按深度画棱 → 画姿态轴。
func (c *cube) paint(canvas *walk.Canvas, _ walk.Rectangle) error {
	bounds := c.widget.ClientBoundsPixels()
	_ = canvas.FillRectangle(c.bg, bounds)

	cx, cy := bounds.Width/2, bounds.Height/2
	size := float32(min(bounds.Width, bounds.Height)) / 8.5

	proj := make([]walk.Point, len(cubeVerts))
	for i, v := range cubeVerts {
		proj[i] = project(quatRotate(c.q, v), size, cx, cy)
	}
	for _, e := range cubeEdges {
		pen := c.edgeFar
		// 棱中点 z 为正即朝向相机，画亮色增强立体感
		if (cubeVerts[e[0]][2]+cubeVerts[e[1]][2])*0.5 > 0 {
			pen = c.edgeNear
		}
		_ = canvas.DrawLinePixels(pen, proj[e[0]], proj[e[1]])
	}
	c.drawAxes(canvas, size, cx, cy)
	return nil
}

// drawAxes 画三条着色姿态轴，并在端点标注轴名。
func (c *cube) drawAxes(canvas *walk.Canvas, size float32, cx, cy int) {
	origin := walk.Point{X: cx, Y: cy}
	for i, tip := range axisTips {
		end := project(quatRotate(c.q, tip), size, cx, cy)
		_ = canvas.DrawLinePixels(c.axes[i], origin, end)
		// 轴名以端点为中心绘制
		r := walk.Rectangle{Width: 24, Height: 16, X: end.X - 12, Y: end.Y - 8}
		_ = canvas.DrawTextPixels(axisText[i], c.widget.Font(), axisColors[i],
			r, walk.TextCenter|walk.TextVCenter|walk.TextSingleLine)
	}
}

// quatRotate 用四元数 (x, y, z, w) 旋转向量：v' = v + 2w(u×v) + 2u×(u×v)，u 为向量部。
func quatRotate(q [4]float32, v [3]float32) [3]float32 {
	qx, qy, qz, qw := q[0], q[1], q[2], q[3]
	tx := 2 * (qy*v[2] - qz*v[1])
	ty := 2 * (qz*v[0] - qx*v[2])
	tz := 2 * (qx*v[1] - qy*v[0])
	return [3]float32{
		v[0] + qw*tx + qy*tz - qz*ty,
		v[1] + qw*ty + qz*tx - qx*tz,
		v[2] + qw*tz + qx*ty - qy*tx,
	}
}

// project 把旋转后的顶点透视投影为屏幕坐标（y 轴翻转，屏幕 y 向下）。
// camDist 大于顶点半径 √3，分母恒为正。
func project(v [3]float32, size float32, cx, cy int) walk.Point {
	s := camDist / (camDist - v[2])
	return walk.Point{
		X: cx + int(v[0]*s*size+0.5),
		Y: cy - int(v[1]*s*size+0.5),
	}
}
