package main

import (
	"bufio"
	"errors"
	"image"
	"io"
	"os"

	jpegscaled "github.com/m8rge/go-scaled-jpeg"
)

// JPEG's inverse transform can reconstruct fewer pixels directly (native
// block sizes 1–8). Validate original dimensions first: scaling must never bypass the
// source pixel limit. Other formats keep their standard decoders.
func decodeRaster(f *os.File, maxPixels int64, targets ...int) (image.Image, error) {
	reader := bufio.NewReaderSize(f, 128<<10)
	config, format, err := image.DecodeConfig(reader)
	if err != nil {
		return nil, err
	}
	if !withinPixelLimit(config.Width, config.Height, maxPixels) {
		return nil, errors.New("image exceeds configured max_pixels")
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	reader.Reset(f)
	target := 0
	if len(targets) > 0 {
		target = targets[0]
	}
	if format == "jpeg" && target > 0 {
		factor := 1
		for factor < 8 && max(config.Width, config.Height)/(factor*2) >= target {
			factor *= 2
		}
		dctSize := 8 / factor
		if target == 1024 && max(config.Width, config.Height) > target {
			// A phone needs a screen-sized image, not an exact pixel count.
			// Choose the largest native IDCT size that fits: a 4000px JPEG
			// becomes 1000px directly, avoiding a second floating-point resize.
			dctSize = max(1, min(8, 8*target/max(config.Width, config.Height)))
		}
		if dctSize < 8 {
			src, decodeErr := jpegscaled.Decode(reader, jpegscaled.DecodeOptions{DCTSizeScaled: dctSize, Tolerant: false})
			if decodeErr == nil {
				return src, nil
			}
			// Keep compatibility with JPEG features handled by the standard decoder.
			if _, err = f.Seek(0, io.SeekStart); err != nil {
				return nil, err
			}
			reader.Reset(f)
		}
	}
	src, _, err := image.Decode(reader)
	return src, err
}

func previewQuality(size int) int {
	if size == 240 {
		return 80
	}
	if size == 1024 {
		return 82
	}
	return 85
}
