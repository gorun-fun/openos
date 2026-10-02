// genicon —— 生成 openos-desktop 的 Windows 应用图标（icon.ico）。
//
// 设计与仓库根 icon-o.svg 完全一致（512 设计稿）：
//   - 黑底圆角方形：rect(16, 16, 480, 480)，圆角半径 112；
//   - 白色粗描边圆环（字母 O）：圆心 (256, 256)，中线半径 118、线宽 52，
//     即外径 144 / 内径 92。
//
// 不用 ImageMagick 等外部工具做 SVG 光栅化的原因：其内置 SVG 渲染器对 stroke
// 属性支持不可靠（实测白色圆环整个丢失）。纯 Go 光栅化可复现：几何 inside 判定
// + 8 倍超采样盒式降采样，边缘足够平滑。
//
// ICO 容器条目策略：≤64px 用 32bpp BMP DIB（兼容旧系统的资源加载路径），
// ≥128px 用 PNG 条目（Vista+ 支持，体积小）。6 档尺寸：16/32/48/64/128/256。
//
// 用法（在 desktop/ 目录下执行）：
//
//	go run ./tools/genicon            # 生成 ./icon.ico
//	go run ./tools/genicon -o xx.ico  # 指定输出路径
//
// 生成 .syso（链接进 Windows exe）用 go-winres，见 desktop/README.md「应用图标」。
package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
)

// 设计稿（512 viewBox）几何常量，与 icon-o.svg 一一对应
const (
	design    = 512.0 // 设计稿边长
	rectInset = 16.0  // 圆角方形留边
	rectSide  = 480.0 // 圆角方形边长
	rectR     = 112.0 // 圆角半径
	cx, cy    = 256.0, 256.0
	ringMid   = 118.0 // 圆环中线半径
	ringWidth = 52.0  // 线宽
)

// insideRect 判定点是否在圆角方形内（先把角外的坐标钳到圆心，再按圆判定）
func insideRect(x, y float64) bool {
	half := rectSide / 2
	ax, ay := abs(x-cx), abs(y-cy)
	dx, dy := ax-(half-rectR), ay-(half-rectR)
	if dx < 0 {
		dx = 0
	}
	if dy < 0 {
		dy = 0
	}
	return dx*dx+dy*dy <= rectR*rectR
}

// insideRing 判定点是否在白色圆环上（内径 ≤ 距心 ≤ 外径）
func insideRing(x, y float64) bool {
	outer := ringMid + ringWidth/2
	inner := ringMid - ringWidth/2
	d2 := (x-cx)*(x-cx) + (y-cy)*(y-cy)
	return inner*inner <= d2 && d2 <= outer*outer
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

// render 以 8 倍超采样光栅化出 size×size 的 RGBA 图像（设计稿等比缩放）
func render(size int) *image.RGBA {
	const ss = 8
	scale := float64(size) / design
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	for py := 0; py < size; py++ {
		for px := 0; px < size; px++ {
			// 每个目标像素对 ss×ss 个子样本做覆盖计数，再盒式平均：
			// 圆环白色叠加在黑色方形之上，方形外全透明
			var r, g, b, a uint32
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					x := (float64(px) + (float64(sx)+0.5)/ss) / scale
					y := (float64(py) + (float64(sy)+0.5)/ss) / scale
					switch {
					case insideRing(x, y):
						r, g, b, a = r+255, g+255, b+255, a+255
					case insideRect(x, y):
						a += 255 // 黑色 (0,0,0) 只累加 alpha
					}
				}
			}
			n := uint32(ss * ss)
			img.SetRGBA(px, py, color.RGBA{
				R: uint8(r / n), G: uint8(g / n), B: uint8(b / n), A: uint8(a / n),
			})
		}
	}
	return img
}

// dibEntry 打包 32bpp BMP DIB 条目（小尺寸用）：BITMAPINFOHEADER + 自底向上
// BGRA 像素 + 全零 AND 掩码（行按 32 位对齐）
func dibEntry(img *image.RGBA) []byte {
	w := img.Bounds().Dx()
	h := img.Bounds().Dy()
	rowMask := ((w + 31) / 32) * 4 // AND 掩码每行字节数
	var buf bytes.Buffer
	be := func(v any) { _ = binary.Write(&buf, binary.LittleEndian, v) }
	// BITMAPINFOHEADER：biHeight = 2h（XOR 像素 + AND 掩码两部分）
	be(uint32(40))
	be(int32(w))
	be(int32(2 * h))
	be(uint16(1))  // biPlanes
	be(uint16(32)) // biBitCount
	be(uint32(0))  // BI_RGB
	be(uint32(w*4*h + rowMask*h))
	for i := 0; i < 4; i++ { // biXPels/biYPels/biClrUsed/biClrImportant 全零
		be(uint32(0))
	}
	for y := h - 1; y >= 0; y-- { // 自底向上
		for x := 0; x < w; x++ {
			c := img.RGBAAt(x, y)
			buf.WriteByte(c.B)
			buf.WriteByte(c.G)
			buf.WriteByte(c.R)
			buf.WriteByte(c.A)
		}
	}
	buf.Write(make([]byte, rowMask*h)) // AND 掩码全零（alpha 已承载透明度）
	return buf.Bytes()
}

func pngEntry(img *image.RGBA) ([]byte, error) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func main() {
	out := flag.String("o", "icon.ico", "输出路径")
	flag.Parse()

	sizes := []int{16, 32, 48, 64, 128, 256}
	var images [][]byte
	for _, s := range sizes {
		img := render(s)
		var data []byte
		var err error
		if s >= 128 {
			data, err = pngEntry(img)
		} else {
			data = dibEntry(img)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "genicon:", err)
			os.Exit(1)
		}
		images = append(images, data)
	}

	var ico bytes.Buffer
	w16 := func(v uint16) { _ = binary.Write(&ico, binary.LittleEndian, v) }
	w32 := func(v uint32) { _ = binary.Write(&ico, binary.LittleEndian, v) }
	// ICONDIR：保留 0、类型 1（icon）、条目数
	w16(0)
	w16(1)
	w16(uint16(len(sizes)))
	offset := uint32(6 + 16*len(sizes)) // 头部 + 目录表之后是第一个图像
	for i, s := range sizes {
		dim := uint8(s % 256) // 256 编码为 0
		ico.WriteByte(dim)
		ico.WriteByte(dim)
		ico.WriteByte(0) // bColorCount（真彩为 0）
		ico.WriteByte(0) // bReserved
		w16(1)           // wPlanes
		w16(32)          // wBitCount
		w32(uint32(len(images[i])))
		w32(offset)
		offset += uint32(len(images[i]))
	}
	for _, data := range images {
		ico.Write(data)
	}

	if err := os.WriteFile(*out, ico.Bytes(), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "genicon:", err)
		os.Exit(1)
	}
	fmt.Printf("✓ 已生成 %s（尺寸 %v）\n", *out, sizes)
}
