package main

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"testing"

	"golang.org/x/image/draw"
)

// Version 1.2's rendering path is kept only as a benchmark baseline. It wraps
// the full-resolution image before scaling, disabling concrete-image fast paths.
func legacyPreview(src image.Image, orientation, size int) *image.RGBA {
	oriented := orientedImage{src: src, orientation: orientation}
	bounds := oriented.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if max(w, h) > size {
		ratio := float64(size) / float64(max(w, h))
		w = max(1, int(float64(w)*ratio))
		h = max(1, int(float64(h)*ratio))
	}
	output := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(output, output.Bounds(), &image.Uniform{color.RGBA{245, 245, 245, 255}}, image.Point{}, draw.Src)
	draw.CatmullRom.Scale(output, output.Bounds(), oriented, bounds, draw.Over, nil)
	return output
}
func benchmarkPhoto() []byte {
	src := image.NewYCbCr(image.Rect(0, 0, 4000, 3000), image.YCbCrSubsampleRatio420)
	for y := 0; y < 3000; y++ {
		for x := 0; x < 4000; x++ {
			src.Y[src.YOffset(x, y)] = uint8((x/17+y/13+(x*y)%23)%220 + 16)
		}
	}
	for i := range src.Cb {
		src.Cb[i] = uint8(100 + i%35)
		src.Cr[i] = uint8(130 + i%25)
	}
	var out bytes.Buffer
	jpeg.Encode(&out, src, &jpeg.Options{Quality: 90})
	return out.Bytes()
}
func BenchmarkPreview12MP(b *testing.B) {
	data := benchmarkPhoto()
	renderers := []struct {
		name   string
		render func(image.Image, int, int) *image.RGBA
	}{{"v1.2", legacyPreview}, {"optimized", renderPreview}}
	for _, renderer := range renderers {
		for _, size := range []int{480, 1600} {
			for _, orientation := range []int{1, 6} {
				name := renderer.name + "/"
				if size == 480 {
					name += "thumbnail"
				} else {
					name += "viewer"
				}
				if orientation == 6 {
					name += "-rotated"
				}
				b.Run(name, func(b *testing.B) {
					b.ReportAllocs()
					for b.Loop() {
						src, err := jpeg.Decode(bytes.NewReader(data))
						if err != nil {
							b.Fatal(err)
						}
						result := renderer.render(src, orientation, size)
						if err := jpeg.Encode(io.Discard, result, &jpeg.Options{Quality: 85}); err != nil {
							b.Fatal(err)
						}
					}
				})
			}
		}
	}
}
