package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"
)

func TestEmbeddedRAWPreviewAndOrientation(t *testing.T) {
	cfg := testConfig(t)
	tools := t.TempDir()
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	response := filepath.Join(tools, "response.json")
	t.Setenv("FOLDERLIGHT_EMBEDDED_RESPONSE", response)
	// No metadata probe or LibRaw development should run when dimensions and a
	// usable embedded JPEG are available. Only verified descriptor input is used.
	script := "#!/bin/sh\n[ \"$*\" = '-j -n -b -Orientation -PreviewImage /proc/self/fd/3' ] || exit 1\ncat \"$FOLDERLIGHT_EMBEDDED_RESPONSE\"\n"
	if err := os.WriteFile(filepath.Join(tools, "exiftool"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	src := image.NewRGBA(image.Rect(0, 0, 640, 320))
	for y := range 320 {
		for x := range 640 {
			c := color.RGBA{R: 220, A: 255}
			if x >= 320 {
				c = color.RGBA{B: 220, A: 255}
			}
			src.SetRGBA(x, y, c)
		}
	}
	var encoded bytes.Buffer
	jpeg.Encode(&encoded, src, nil)
	data, _ := json.Marshal([]map[string]any{{"Orientation": 6, "PreviewImage": "base64:" + base64.StdEncoding.EncodeToString(encoded.Bytes())}})
	os.WriteFile(response, data, 0600)
	writeImage(t, cfg, "camera.nef", []byte("synthetic RAW"))
	info, _ := os.Stat(filepath.Join(cfg.Library, "camera.nef"))
	p := Photo{Path: "camera.nef", Name: "camera.nef", Size: info.Size(), Mtime: info.ModTime().UnixNano(), Width: 1280, Height: 640, Orientation: 1}
	cache, err := newCache(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer cache.close()
	f, err := cache.get(context.Background(), p, 480)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	result, err := jpeg.Decode(f)
	if err != nil || result.Bounds().Dx() != 240 || result.Bounds().Dy() != 480 {
		t.Fatal("embedded preview orientation", result, err)
	}
	top := color.RGBAModel.Convert(result.At(120, 60)).(color.RGBA)
	bottom := color.RGBAModel.Convert(result.At(120, 420)).(color.RGBA)
	if top.R < 200 || bottom.B < 200 {
		t.Fatal("embedded preview rotated incorrectly", top, bottom)
	}
}

func TestEmbeddedRAWRejectsBadCandidates(t *testing.T) {
	cfg := testConfig(t)
	tools := t.TempDir()
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	response := filepath.Join(tools, "response.json")
	t.Setenv("FOLDERLIGHT_EMBEDDED_RESPONSE", response)
	os.WriteFile(filepath.Join(tools, "exiftool"), []byte("#!/bin/sh\ncat \"$FOLDERLIGHT_EMBEDDED_RESPONSE\"\n"), 0700)
	f, err := os.CreateTemp(cfg.State, "raw-source")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, candidate := range []string{"", "bad base64!", base64.StdEncoding.EncodeToString(baseJPEG()), base64.StdEncoding.EncodeToString(baseJPEG()[:100])} {
		t.Run(candidate[:min(len(candidate), 10)], func(t *testing.T) {
			data, _ := json.Marshal([]map[string]any{{"PreviewImage": "base64:" + candidate, "JpgFromRaw": "base64:" + candidate}})
			os.WriteFile(response, data, 0600)
			if name, _ := rawEmbeddedPreview(context.Background(), f, Photo{Width: 4000, Height: 3000}, 1600, cfg.State, cfg.MaxPixels); name != "" {
				t.Fatal("accepted corrupt or undersized JPEG", name)
			}
		})
	}
}
