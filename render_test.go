package main

import (
	"image"
	"image/color"
	"testing"
)

func TestFastRenderOrientationAndBounds(t *testing.T) {
	src := image.NewRGBA(image.Rect(3, 7, 12, 14))
	for y := 7; y < 14; y++ {
		for x := 3; x < 12; x++ {
			src.SetRGBA(x, y, color.RGBA{uint8(x * 20), uint8(y * 13), uint8(x + y), 255})
		}
	}
	for orientation := 1; orientation <= 8; orientation++ {
		got, want := renderPreview(src, orientation, 128), legacyPreview(src, orientation, 128)
		if got.Bounds() != want.Bounds() {
			t.Fatalf("orientation %d bounds: %v != %v", orientation, got.Bounds(), want.Bounds())
		}
		for i, v := range got.Pix {
			delta := int(v) - int(want.Pix[i])
			if delta < -1 || delta > 1 {
				t.Fatalf("orientation %d pixel %d: %d != %d", orientation, i, v, want.Pix[i])
			}
		}
	}
}
func TestAreaReductionAntialiasAndAlpha(t *testing.T) {
	gray := image.NewGray(image.Rect(0, 0, 128, 128))
	alpha := image.NewNRGBA(gray.Bounds())
	for y := 0; y < 128; y++ {
		for x := 0; x < 128; x++ {
			if (x+y)%2 == 0 {
				gray.SetGray(x, y, color.Gray{255})
				alpha.SetNRGBA(x, y, color.NRGBA{0, 0, 255, 255})
			} else {
				alpha.SetNRGBA(x, y, color.NRGBA{255, 0, 0, 0})
			}
		}
	}
	got := renderPreview(gray, 1, 16)
	for _, v := range []color.RGBA{got.RGBAAt(0, 0), got.RGBAAt(8, 8), got.RGBAAt(15, 15)} {
		if v.R < 127 || v.R > 129 || v.R != v.G || v.G != v.B {
			t.Fatal("checkerboard aliasing", v)
		}
	}
	rgba := renderPreview(alpha, 1, 16).RGBAAt(8, 8)
	if rgba.R < 121 || rgba.R > 124 || rgba.G != rgba.R || rgba.B < 248 || rgba.B > 251 || rgba.A != 255 {
		t.Fatal("transparent colors leaked into the preview", rgba)
	}
	yuv := image.NewYCbCr(image.Rect(0, 0, 129, 97), image.YCbCrSubsampleRatio420)
	for i := range yuv.Y {
		yuv.Y[i] = 100
	}
	for i := range yuv.Cb {
		yuv.Cb[i] = 140
		yuv.Cr[i] = 150
	}
	r, g, b := color.YCbCrToRGB(100, 140, 150)
	pixel := renderPreview(yuv, 6, 16).RGBAAt(5, 5)
	if pixel.R != r || pixel.G != g || pixel.B != b {
		t.Fatal("YCbCr color changed", pixel, r, g, b)
	}
}
