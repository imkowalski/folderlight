package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
)

// Camera JPEG previews avoid downloading/developing the whole RAW mosaic.
// Keep the existing size and pixel limits; absent, small, or damaged previews
// simply fall back to LibRaw's normal development path.
func rawEmbeddedPreview(ctx context.Context, f *os.File, p Photo, size int, dir string, maxPixels int64) (string, int) {
	required := min(size, max(p.Width, p.Height)/2)
	for _, tag := range []string{"-PreviewImage", "-JpgFromRaw"} {
		result, err := runMediaCommand(ctx, f, dir, "exiftool", false, 8<<20, "-j", "-n", "-b", "-Orientation", tag, sourceFD)
		if err != nil || len(result) == 0 {
			continue
		}
		var rows []map[string]json.RawMessage
		if json.Unmarshal(result, &rows) != nil || len(rows) != 1 {
			continue
		}
		var encoded string
		if json.Unmarshal(rows[0][strings.TrimPrefix(tag, "-")], &encoded) != nil || !strings.HasPrefix(encoded, "base64:") {
			continue
		}
		data, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(encoded, "base64:"))
		if err != nil {
			continue
		}
		orientation := 1
		var flip int
		if json.Unmarshal(rows[0]["Orientation"], &flip) == nil && flip >= 1 && flip <= 8 {
			orientation = flip
		}
		cfg, err := jpeg.DecodeConfig(bytes.NewReader(data))
		if err != nil || max(cfg.Width, cfg.Height) < required || !withinPixelLimit(cfg.Width, cfg.Height, maxPixels) {
			continue
		}
		// A capped helper response can contain a truncated JPEG. Decode once
		// before accepting it so damaged embedded data cannot hide a good RAW.
		if _, err = jpeg.Decode(bytes.NewReader(data)); err != nil {
			continue
		}
		name := filepath.Join(dir, "embedded.jpg")
		if os.WriteFile(name, data, 0600) == nil {
			return name, orientation
		}
	}
	return "", 1
}
