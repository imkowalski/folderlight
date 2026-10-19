package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"image"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

//go:embed testdata/synthetic.dng
var syntheticDNG []byte

//go:embed testdata/synthetic-preview.dng
var syntheticPreviewDNG []byte

func TestExtendedMetadataAndKinds(t *testing.T) {
	p := Photo{Taken: 1, Tags: []string{}}
	err := applyExtendedMetadata([]byte(`[{"ImageWidth":128,"ImageHeight":96,"XPKeywords":" Ærø; Sommer; ÆRØ ","Subject":["Łódź","Sommer"],"Keywords":["Family holiday"],"DateTimeOriginal":"1700000000"}]`), &p)
	if err != nil || p.Width != 128 || p.Height != 96 || p.Taken != 1700000000 || !reflect.DeepEqual(p.Tags, []string{"Ærø", "Sommer", "Family holiday", "Łódź"}) {
		t.Fatal(p, err)
	}
	for name, kind := range map[string]string{"a.CR3": "raw", "a.NEF": "raw", "a.dng": "raw", "a.HEIC": "heic", "a.heif": "heic", "a.MOV": "video", "a.mkv": "video", "a.webm": "video", "a.jpg": "image", "a.txt": ""} {
		if mediaKind(name) != kind || supported(name) != (kind != "") {
			t.Fatal(name)
		}
	}
	if !withinPixelLimit(4000, 3000, 12000000) || withinPixelLimit(4001, 3000, 12000000) || withinPixelLimit(0, 1, 12000000) {
		t.Fatal("pixel limits")
	}
	cfg := testConfig(t)
	writeImage(t, cfg, "filename-only.heic", []byte("broken"))
	store := testStore(t, cfg)
	scanNow(t, store, cfg, false)
	list, err := store.list(context.Background(), Query{Search: "filename-only", Scope: "all", Sort: "name", Page: 1, Limit: 60})
	if err != nil || list.Total != 1 || list.Photos[0].Kind != "heic" {
		t.Fatal(list, err)
	}
}

// Run in Alpine with FOLDERLIGHT_MEDIA_TEST=1 after installing the documented
// optional packages. Every media source is synthetic and safe to publish.
func TestRealMediaDecoders(t *testing.T) {
	if os.Getenv("FOLDERLIGHT_MEDIA_TEST") != "1" {
		t.Skip("optional Alpine decoder integration test")
	}
	for name, installed := range mediaTools() {
		if !installed {
			t.Fatalf("missing integration tool %s", name)
		}
	}
	cfg := testConfig(t)
	ctx := context.Background()
	store := testStore(t, cfg)
	run := func(name string, args ...string) {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Env = append(os.Environ(), "OMP_NUM_THREADS=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v: %s", name, err, out)
		}
	}
	writeImage(t, cfg, "still.jpg", baseJPEG())
	writeImage(t, cfg, "sensor.dng", syntheticDNG)
	writeImage(t, cfg, "embedded.dng", syntheticPreviewDNG)
	run("heif-enc", "-q", "50", "-o", filepath.Join(cfg.Library, "phone.heic"), filepath.Join(cfg.Library, "still.jpg"))
	run("ffmpeg", "-v", "error", "-threads", "1", "-filter_threads", "1", "-f", "lavfi", "-i", "testsrc=size=160x120:rate=10", "-f", "lavfi", "-i", "sine=frequency=440:duration=2", "-t", "2", "-c:v", "libx264", "-threads", "1", "-pix_fmt", "yuv420p", "-c:a", "aac", filepath.Join(cfg.Library, "clip.mp4"))
	run("ffmpeg", "-v", "error", "-threads", "1", "-filter_threads", "1", "-f", "lavfi", "-i", "testsrc=size=160x120:rate=10", "-t", "1", "-c:v", "libvpx-vp9", "-threads", "1", filepath.Join(cfg.Library, "web.webm"))
	for _, name := range []string{"sensor.dng", "embedded.dng", "phone.heic", "clip.mp4"} {
		run("exiftool", "-overwrite_original", "-XMP-dc:Subject=Ærø", "-XMP-dc:Subject+=Łódź", filepath.Join(cfg.Library, name))
		if name != "clip.mp4" {
			run("exiftool", "-overwrite_original", "-XPKeywords=Ærø; Sommer", filepath.Join(cfg.Library, name))
		}
	}
	originals := map[string][]byte{}
	entries, _ := os.ReadDir(cfg.Library)
	for _, entry := range entries {
		originals[entry.Name()], _ = os.ReadFile(filepath.Join(cfg.Library, entry.Name()))
	}
	status := scanNow(t, store, cfg, true)
	if status.Errors != 0 {
		t.Fatal(status)
	}
	cache, err := newCache(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer cache.close()
	list, err := store.list(ctx, Query{Scope: "all", Page: 1, Limit: 60})
	if err != nil || list.Total != 6 {
		t.Fatal(list, err)
	}
	for _, p := range list.Photos {
		t.Run(p.Name, func(t *testing.T) {
			if p.Name == "embedded.dng" {
				f, err := os.Open(filepath.Join(cfg.Library, p.Name))
				if err != nil {
					t.Fatal(err)
				}
				defer f.Close()
				name, flip := rawEmbeddedPreview(ctx, f, p, 480, t.TempDir(), cfg.MaxPixels)
				if name == "" || flip != 6 {
					t.Fatal("real ExifTool embedded JPEG extraction/orientation", name, flip)
				}
			}
			expected := []string{"Ærø", "Łódź"}
			if p.Kind == "raw" || p.Kind == "heic" {
				expected = []string{"Ærø", "Sommer", "Łódź"}
			}
			if p.Kind != "image" && p.Name != "web.webm" && !reflect.DeepEqual(p.Tags, expected) {
				t.Fatal(p.Tags)
			}
			f, err := cache.get(ctx, p, 480)
			if err != nil {
				t.Fatal("thumbnail", err)
			}
			decoded, _, err := image.DecodeConfig(f)
			f.Close()
			if err != nil || decoded.Width < 1 || decoded.Height < 1 || decoded.Width > 480 || decoded.Height > 480 {
				t.Fatal(decoded, err)
			}
			if p.Name == "embedded.dng" && (decoded.Width != 96 || decoded.Height != 128) {
				t.Fatal("embedded RAW rotation", decoded)
			}
			if p.Kind == "video" {
				f, err := cache.get(ctx, p, 0)
				if err != nil {
					t.Fatal("video transcode", err)
				}
				info, _ := f.Stat()
				f.Close()
				if info.Size() == 0 {
					t.Fatal("empty video")
				}
				app := &App{store: store, indexer: &Indexer{store: store, config: cfg}, cache: cache, ctx: ctx}
				handler := app.handler()
				r := httptest.NewRequest("GET", "/api/video?id="+jsonNumber(p.ID), nil)
				r.Header.Set("Range", "bytes=0-99")
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				if w.Code != http.StatusPartialContent || w.Body.Len() != 100 || w.Header().Get("Content-Type") != "video/mp4" {
					t.Fatal(w.Code, w.Body.Len(), w.Header())
				}
			}
		})
	}
	for name, before := range originals {
		after, _ := os.ReadFile(filepath.Join(cfg.Library, name))
		if string(before) != string(after) {
			t.Fatal("library source changed", name)
		}
	}
	entries, _ = os.ReadDir(cfg.Library)
	if len(entries) != len(originals) {
		t.Fatal("helper wrote into source library")
	}
	bad := list.Photos[0]
	bad.Path = "../escape.mp4"
	bad.Name = "escape.mp4"
	if _, err := cache.get(ctx, bad, 480); err == nil {
		t.Fatal("media path traversal accepted")
	}
}
func jsonNumber(n int64) string { b, _ := json.Marshal(n); return string(b) }

func TestMediaVideoPreparationValidation(t *testing.T) {
	cfg := testConfig(t)
	store := testStore(t, cfg)
	writeImage(t, cfg, "still.jpg", baseJPEG())
	scanNow(t, store, cfg, false)
	cache, err := newCache(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer cache.close()
	app := &App{store: store, indexer: &Indexer{store: store, config: cfg}, cache: cache, ctx: context.Background()}
	w := httptest.NewRecorder()
	app.handler().ServeHTTP(w, httptest.NewRequest("GET", "/api/video?id=1", nil))
	if w.Code != 400 || !strings.Contains(w.Body.String(), "not a video") {
		t.Fatal(w.Code, w.Body.String())
	}
}
