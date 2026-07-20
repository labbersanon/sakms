package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
)

// iconGreen/iconAmber/iconRed return 16×16 solid-colour PNGs for the system
// tray icon. Generated at runtime to avoid binary blobs in the repository.

func iconGreen() []byte { return solidPNG(0x22, 0xc5, 0x5e) } // Tailwind green-500
func iconAmber() []byte { return solidPNG(0xf5, 0x9e, 0x0b) } // Tailwind amber-500
func iconRed() []byte   { return solidPNG(0xef, 0x44, 0x44) } // Tailwind red-500

func solidPNG(r, g, b uint8) []byte {
	img := image.NewRGBA(image.Rect(0, 0, 16, 16))
	c := color.RGBA{R: r, G: g, B: b, A: 0xff}
	for y := range 16 {
		for x := range 16 {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		panic(err)
	}
	return buf.Bytes()
}
