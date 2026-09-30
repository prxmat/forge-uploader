// Draws Forge Uploader's icon (an anvil on the Forge red) as PNGs and packs them into assets/icon.ico.
package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"os"
)

var (
	red  = color.RGBA{0xf4, 0x43, 0x36, 0xff}
	dark = color.RGBA{0x19, 0x1e, 0x24, 0xff}
	pale = color.RGBA{0xee, 0xf0, 0xf3, 0xff}
)

func draw(size int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	s := float64(size)
	radius := s * 0.22
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			fx, fy := float64(x)+0.5, float64(y)+0.5
			// Rounded square background.
			dx := max(0, max(radius-fx, fx-(s-radius)))
			dy := max(0, max(radius-fy, fy-(s-radius)))
			if dx*dx+dy*dy > radius*radius {
				continue
			}
			img.Set(x, y, red)
			u, v := fx/s, fy/s
			// Anvil: top face, horn, neck, foot.
			top := v >= 0.30 && v <= 0.46 && u >= 0.16 && u <= 0.84
			horn := v >= 0.34 && v <= 0.46 && u >= 0.08 && u < 0.16 && (v-0.34) >= (0.16-u)*0.9
			neck := v > 0.46 && v <= 0.64 && u >= 0.40 && u <= 0.66
			foot := v > 0.64 && v <= 0.76 && u >= 0.28 && u <= 0.78
			if top || horn || neck || foot {
				img.Set(x, y, dark)
			}
			// A spark above the horn.
			if (u-0.24)*(u-0.24)+(v-0.20)*(v-0.20) < 0.0025 {
				img.Set(x, y, pale)
			}
		}
	}
	return img
}

func main() {
	sizes := []int{16, 24, 32, 48, 64, 128, 256}
	var pngs [][]byte
	for _, size := range sizes {
		var buf bytes.Buffer
		png.Encode(&buf, draw(size))
		pngs = append(pngs, buf.Bytes())
	}
	os.WriteFile("assets/icon-256.png", pngs[len(pngs)-1], 0o644)
	// ICO container holding PNG images (Vista and later).
	var ico bytes.Buffer
	binary.Write(&ico, binary.LittleEndian, [3]uint16{0, 1, uint16(len(sizes))})
	offset := 6 + 16*len(sizes)
	for index, size := range sizes {
		dim := byte(size)
		if size >= 256 {
			dim = 0
		}
		ico.Write([]byte{dim, dim, 0, 0})
		binary.Write(&ico, binary.LittleEndian, [2]uint16{1, 32})
		binary.Write(&ico, binary.LittleEndian, uint32(len(pngs[index])))
		binary.Write(&ico, binary.LittleEndian, uint32(offset))
		offset += len(pngs[index])
	}
	for _, data := range pngs {
		ico.Write(data)
	}
	os.WriteFile("assets/icon.ico", ico.Bytes(), 0o644)
}
