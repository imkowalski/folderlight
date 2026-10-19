package main

import (
	"image"
	"image/color"

	"golang.org/x/image/draw"
)

// Scale the concrete decoder output first. Wrapping a full-resolution image in
// orientedImage disables the scaler's YCbCr/RGBA fast paths and allocates a
// color object for each sampled pixel. Rotation only needs the small result.
func renderPreview(src image.Image, orientation, size int) *image.RGBA {
	bounds := src.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if max(w, h) > size {
		ratio := float64(size) / float64(max(w, h))
		w = max(1, int(float64(w)*ratio))
		h = max(1, int(float64(h)*ratio))
	}
	scaled := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(scaled, scaled.Bounds(), &image.Uniform{color.RGBA{245, 245, 245, 255}}, image.Point{}, draw.Src)
	if factor := min(bounds.Dx()/w, bounds.Dy()/h); factor >= 2 {
		if reduced := areaReduce(src, factor); reduced != nil {
			src = reduced
			bounds = src.Bounds()
		}
	}
	if w == bounds.Dx() && h == bounds.Dy() {
		draw.Draw(scaled, scaled.Bounds(), src, bounds.Min, draw.Over)
	} else {
		draw.CatmullRom.Scale(scaled, scaled.Bounds(), src, bounds, draw.Over, nil)
	}
	if orientation < 2 || orientation > 8 {
		return scaled
	}
	view := orientedImage{src: scaled, orientation: orientation}
	output := image.NewRGBA(view.Bounds())
	for y := 0; y < output.Bounds().Dy(); y++ {
		for x := 0; x < output.Bounds().Dx(); x++ {
			sx, sy := x, y
			switch orientation {
			case 2:
				sx = w - 1 - x
			case 3:
				sx, sy = w-1-x, h-1-y
			case 4:
				sy = h - 1 - y
			case 5:
				sx, sy = y, x
			case 6:
				sx, sy = y, h-1-x
			case 7:
				sx, sy = w-1-y, h-1-x
			case 8:
				sx, sy = w-1-y, x
			}
			from := scaled.PixOffset(sx, sy)
			to := output.PixOffset(x, y)
			copy(output.Pix[to:to+4], scaled.Pix[from:from+4])
		}
	}
	return output
}

// Average source pixels before the final Catmull-Rom pass. Integer-area
// reduction retains fine-pattern antialiasing without a full-resolution
// floating-point filter buffer. Common decoder types avoid color interfaces.
func areaReduce(src image.Image, factor int) *image.RGBA {
	switch src.(type) {
	case *image.YCbCr, *image.RGBA, *image.NRGBA, *image.Gray:
	default:
		return nil
	}
	b := src.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, (b.Dx()+factor-1)/factor, (b.Dy()+factor-1)/factor))
	for y := 0; y < out.Bounds().Dy(); y++ {
		for x := 0; x < out.Bounds().Dx(); x++ {
			x0, y0 := b.Min.X+x*factor, b.Min.Y+y*factor
			x1, y1 := min(x0+factor, b.Max.X), min(y0+factor, b.Max.Y)
			r, g, blue, a := 0, 0, 0, 0
			switch s := src.(type) {
			case *image.YCbCr:
				for sy := y0; sy < y1; sy++ {
					for sx := x0; sx < x1; sx++ {
						c := s.COffset(sx, sy)
						rr, gg, bb := color.YCbCrToRGB(s.Y[s.YOffset(sx, sy)], s.Cb[c], s.Cr[c])
						r += int(rr)
						g += int(gg)
						blue += int(bb)
						a += 255
					}
				}
			case *image.RGBA:
				for sy := y0; sy < y1; sy++ {
					i := s.PixOffset(x0, sy)
					for sx := x0; sx < x1; sx++ {
						p := s.Pix[i : i+4]
						r += int(p[0])
						g += int(p[1])
						blue += int(p[2])
						a += int(p[3])
						i += 4
					}
				}
			case *image.NRGBA:
				for sy := y0; sy < y1; sy++ {
					i := s.PixOffset(x0, sy)
					for sx := x0; sx < x1; sx++ {
						p := s.Pix[i : i+4]
						alpha := int(p[3])
						r += (int(p[0])*alpha + 127) / 255
						g += (int(p[1])*alpha + 127) / 255
						blue += (int(p[2])*alpha + 127) / 255
						a += alpha
						i += 4
					}
				}
			case *image.Gray:
				for sy := y0; sy < y1; sy++ {
					i := s.PixOffset(x0, sy)
					for sx := x0; sx < x1; sx++ {
						v := int(s.Pix[i])
						r += v
						g += v
						blue += v
						a += 255
						i++
					}
				}
			}
			n := (x1 - x0) * (y1 - y0)
			out.SetRGBA(x, y, color.RGBA{uint8((r + n/2) / n), uint8((g + n/2) / n), uint8((blue + n/2) / n), uint8((a + n/2) / n)})
		}
	}
	return out
}
