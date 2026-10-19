package main

import (
	"context"
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition timed out")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestThumbnailReusesCachedPreviewWithoutNAS(t *testing.T) {
	cfg := testConfig(t)
	store := testStore(t, cfg)
	writeImage(t, cfg, "portrait.jpg", taggedJPEG("Test", 6))
	scanNow(t, store, cfg, false)
	p, _ := store.photo(context.Background(), 1)
	cache, err := newCache(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer cache.close()
	preview, err := cache.get(context.Background(), p, 1600)
	if err != nil {
		t.Fatal(err)
	}
	preview.Close()
	if err = os.Rename(cfg.Library, cfg.Library+"-offline"); err != nil {
		t.Fatal(err)
	}
	thumb, err := cache.get(context.Background(), p, 480)
	if err != nil {
		t.Fatal("cached preview reuse failed with offline source", err)
	}
	defer thumb.Close()
	decoded, _, err := image.DecodeConfig(thumb)
	if err != nil || decoded.Width != 16 || decoded.Height != 24 {
		t.Fatal("cached preview was rotated twice", decoded, err)
	}
}
func TestBackgroundPreviewPromotion(t *testing.T) {
	cfg := testConfig(t)
	cfg.Workers = 0
	store := testStore(t, cfg)
	writeImage(t, cfg, "photo.jpg", baseJPEG())
	scanNow(t, store, cfg, false)
	p, _ := store.photo(context.Background(), 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cache, err := newCache(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer cache.close()
	results := make(chan error, 2)
	request := func(background bool) {
		f, err := cache.getPriority(ctx, p, 1600, background)
		if f != nil {
			f.Close()
		}
		results <- err
	}
	go request(true)
	waitFor(t, func() bool { return len(cache.background) == 1 })
	go request(false)
	waitFor(t, func() bool { return len(cache.high) == 1 })
	cache.processJob(<-cache.high)
	cache.processJob(<-cache.background)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if len(cache.entries) != 1 {
		t.Fatal("promoted preview generated twice")
	}
}
func TestCacheTouchThrottlingAndReplacement(t *testing.T) {
	cfg := testConfig(t)
	cache, err := newCache(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer cache.close()
	key := cacheKey(Photo{Path: "a.jpg"}, 480)
	for _, text := range []string{"old", "newer"} {
		name := filepath.Join(cache.dir, "replace.tmp")
		os.WriteFile(name, []byte(text), 0600)
		if err := cache.commitCache(name, key, int64(len(text))); err != nil {
			t.Fatal(err)
		}
	}
	if len(cache.entries) != 1 || cache.bytes != 5 {
		t.Fatal("replacement double counted", len(cache.entries), cache.bytes)
	}
	cache.mu.Lock()
	element := cache.entries[key]
	entry := element.Value.(cacheEntry)
	entry.touched = time.Now().Add(-2 * time.Minute)
	element.Value = entry
	cache.mu.Unlock()
	f, err := cache.open(key)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	first, _ := os.Stat(filepath.Join(cache.dir, key))
	f, err = cache.open(key)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	second, _ := os.Stat(filepath.Join(cache.dir, key))
	if !first.ModTime().Equal(second.ModTime()) {
		t.Fatal("cache hit rewrote the access timestamp")
	}
}
func TestVideoDoesNotBlockImageWorker(t *testing.T) {
	cfg := testConfig(t)
	cfg.VideoWorkers = 1
	tools := t.TempDir()
	started := filepath.Join(tools, "started")
	t.Setenv("FOLDERLIGHT_VIDEO_STARTED", started)
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	scripts := map[string]string{
		"ffprobe": "#!/bin/sh\nprintf '%s' '{\"streams\":[{\"width\":24,\"height\":16}],\"format\":{\"duration\":\"2\"}}'\n",
		"ffmpeg":  "#!/bin/sh\ntouch \"$FOLDERLIGHT_VIDEO_STARTED\"\nfor output do :; done\nsleep 1\nprintf 'synthetic video' > \"$output\"\n",
	}
	for name, script := range scripts {
		if err := os.WriteFile(filepath.Join(tools, name), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	writeImage(t, cfg, "video.mp4", []byte("source"))
	writeImage(t, cfg, "photo.jpg", baseJPEG())
	videoInfo, _ := os.Stat(filepath.Join(cfg.Library, "video.mp4"))
	photoInfo, _ := os.Stat(filepath.Join(cfg.Library, "photo.jpg"))
	video := Photo{Path: "video.mp4", Name: "video.mp4", Size: videoInfo.Size(), Mtime: videoInfo.ModTime().UnixNano()}
	photo := Photo{Path: "photo.jpg", Name: "photo.jpg", Size: photoInfo.Size(), Mtime: photoInfo.ModTime().UnixNano(), Orientation: 1}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cache, err := newCache(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer cache.close()
	done := make(chan error, 1)
	go func() {
		f, err := cache.get(ctx, video, 0)
		if f != nil {
			f.Close()
		}
		done <- err
	}()
	waitFor(t, func() bool { _, err := os.Stat(started); return err == nil })
	quick, cancelQuick := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancelQuick()
	f, err := cache.get(quick, photo, 480)
	if err != nil {
		t.Fatal("image blocked behind video", err)
	}
	f.Close()
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	// The mocked encoder must never receive a writable source path.
	original, _ := os.ReadFile(filepath.Join(cfg.Library, "video.mp4"))
	if strings.TrimSpace(string(original)) != "source" {
		t.Fatal("source modified")
	}
}
