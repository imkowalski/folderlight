package main

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPhoneJPEGScalingAndLimits(t *testing.T) {
	cfg := testConfig(t)
	src := image.NewRGBA(image.Rect(0, 0, 4000, 3000))
	for y := 0; y < 3000; y++ {
		for x := 0; x < 4000; x++ {
			c := color.RGBA{220, 20, 20, 255}
			if x >= 2000 {
				c = color.RGBA{20, 20, 220, 255}
			}
			src.SetRGBA(x, y, c)
		}
	}
	var encoded bytes.Buffer
	jpeg.Encode(&encoded, src, &jpeg.Options{Quality: 90})
	writeImage(t, cfg, "large.jpg", encoded.Bytes())
	f, err := os.Open(filepath.Join(cfg.Library, "large.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	decoded, err := decodeRaster(f, cfg.MaxPixels, 1024)
	if err != nil || decoded.Bounds().Dx() != 1000 || decoded.Bounds().Dy() != 750 {
		t.Fatal("JPEG should decode directly at phone size", err)
	}
	portrait := renderPreview(decoded, 6, 1024)
	if portrait.Bounds().Dx() != 750 || portrait.Bounds().Dy() != 1000 {
		t.Fatal(portrait.Bounds())
	}
	top := color.RGBAModel.Convert(portrait.At(375, 100)).(color.RGBA)
	bottom := color.RGBAModel.Convert(portrait.At(375, 900)).(color.RGBA)
	if top.R < 200 || bottom.B < 200 {
		t.Fatal("JPEG orientation/color changed", top, bottom)
	}
	f.Seek(0, 0)
	if _, err = decodeRaster(f, 11_999_999, 1024); err == nil {
		t.Fatal("scaled decoding bypassed original pixel limit")
	}
}

func TestScaledJPEGGoCorpus(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(runtime.GOROOT(), "src/image/testdata/*.jpeg"))
	if err != nil || len(files) == 0 {
		t.Fatal("Go JPEG fixtures unavailable", err)
	}
	checked := 0
	for _, name := range files {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		original, err := jpeg.Decode(bytes.NewReader(data))
		if err != nil {
			continue
		} // Go also includes intentionally unsupported/corrupt cases.
		t.Run(filepath.Base(name), func(t *testing.T) {
			f, err := os.Open(name)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			decoded, err := decodeRaster(f, 40_000_000, 31)
			if err != nil {
				t.Fatal(err)
			}
			got := renderPreview(decoded, 1, 31)
			want := renderPreview(original, 1, 31)
			// Native JPEG dimensions round at block boundaries. Compare global color
			// means, so this catches bad component layouts/CMYK inversions without
			// mistaking a one-pixel resampling difference for a color-space error.
			mean := func(img image.Image) [3]float64 {
				var sum [3]float64
				b := img.Bounds()
				for y := b.Min.Y; y < b.Max.Y; y++ {
					for x := b.Min.X; x < b.Max.X; x++ {
						r, g, b, _ := img.At(x, y).RGBA()
						sum[0] += float64(r >> 8)
						sum[1] += float64(g >> 8)
						sum[2] += float64(b >> 8)
					}
				}
				for i := range sum {
					sum[i] /= float64(b.Dx() * b.Dy())
				}
				return sum
			}
			g, w := mean(got), mean(want)
			for i := range g {
				delta := g[i] - w[i]
				if delta < -12 || delta > 12 {
					t.Fatal("scaled JPEG color mismatch", g, w)
				}
			}
			if got.Bounds().Dx() > 31 || got.Bounds().Dy() > 31 {
				t.Fatal(got.Bounds())
			}
		})
		checked++
	}
	t.Logf("Checked %d valid Go JPEG fixtures (baseline/progressive, subsampling, RGB/CMYK and gray)", checked)
}

func TestScaledJPEGRejectsTruncation(t *testing.T) {
	cfg := testConfig(t)
	data := benchmarkPhoto()
	writeImage(t, cfg, "cut.jpg", data[:len(data)/2])
	f, err := os.Open(filepath.Join(cfg.Library, "cut.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err = decodeRaster(f, cfg.MaxPixels, 1024); err == nil {
		t.Fatal("truncated JPEG was silently accepted")
	}
}

func TestPhoneCacheVariantsAndReuse(t *testing.T) {
	cfg := testConfig(t)
	store := testStore(t, cfg)
	writeImage(t, cfg, "portrait.jpg", taggedJPEG("Phone", 6))
	scanNow(t, store, cfg, false)
	p, _ := store.photo(context.Background(), 1)
	cache, err := newCache(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer cache.close()
	f, err := cache.get(context.Background(), p, 1024)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err = os.Rename(cfg.Library, cfg.Library+"-offline"); err != nil {
		t.Fatal(err)
	}
	for _, size := range []int{240, 480} {
		f, err := cache.get(context.Background(), p, size)
		if err != nil {
			t.Fatal("could not reuse phone preview", size, err)
		}
		decoded, _, err := image.DecodeConfig(f)
		f.Close()
		if err != nil || decoded.Width != 16 || decoded.Height != 24 {
			t.Fatal("orientation applied twice", decoded, err)
		}
	}
	for _, name := range []string{"small", "screen"} {
		if !strings.HasSuffix(cacheKey(p, map[string]int{"small": 240, "screen": 1024}[name]), ".jpg") {
			t.Fatal(name)
		}
	}
}

func TestPhoneJPEGNativeDCTSizes(t *testing.T) {
	cfg := testConfig(t)
	for _, width := range []int{1100, 1600, 2000, 2400, 3200, 5600} {
		src := image.NewRGBA(image.Rect(0, 0, width, width/2))
		for i := 0; i < len(src.Pix); i += 4 {
			copy(src.Pix[i:i+4], []byte{64, 128, 192, 255})
		}
		name := filepath.Join(cfg.State, "native.jpg")
		f, err := os.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if err = jpeg.Encode(f, src, &jpeg.Options{Quality: 90}); err != nil {
			t.Fatal(err)
		}
		f.Seek(0, 0)
		decoded, err := decodeRaster(f, cfg.MaxPixels, 1024)
		f.Close()
		if err != nil {
			t.Fatal(width, err)
		}
		if decoded.Bounds().Dx() > 1024 || decoded.Bounds().Dx() < 640 {
			t.Fatal("phone JPEG native size", width, decoded.Bounds())
		}
		pixel := color.RGBAModel.Convert(decoded.At(decoded.Bounds().Dx()/2, decoded.Bounds().Dy()/2)).(color.RGBA)
		if pixel.R < 60 || pixel.R > 68 || pixel.G < 124 || pixel.G > 132 || pixel.B < 188 || pixel.B > 196 {
			t.Fatal("native scaled IDCT color", width, pixel)
		}
	}
}
