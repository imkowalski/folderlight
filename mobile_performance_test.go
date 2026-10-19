package main

import (
	"image/jpeg"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// Keep the exact pre-1.5 full-decode rendering path for comparison. The JPEG
// bytes and output quality are identical for the two 1600px cases.
func BenchmarkMobilePreview12MP(b *testing.B) {
	filename := filepath.Join(b.TempDir(), "photo.jpg")
	os.WriteFile(filename, benchmarkPhoto(), 0600)
	f, err := os.Open(filename)
	if err != nil {
		b.Fatal(err)
	}
	defer f.Close()
	for _, tc := range []struct {
		name                  string
		target, size, quality int
	}{
		{"v1.4-full-1600", 0, 1600, 85},
		{"scaled-full-1600", 1600, 1600, 85},
		{"scaled-phone-1024", 1024, 1024, 82},
		{"scaled-thumb-240", 240, 240, 80},
	} {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				f.Seek(0, 0)
				src, err := decodeRaster(f, 40_000_000, tc.target)
				if err != nil {
					b.Fatal(err)
				}
				out := renderPreview(src, 6, tc.size)
				if err := jpeg.Encode(io.Discard, out, &jpeg.Options{Quality: tc.quality}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
