package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	_ "golang.org/x/image/tiff"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func mediaKind(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".jpg", ".jpeg", ".png", ".webp", ".gif":
		return "image"
	case ".heic", ".heif", ".hif":
		return "heic"
	case ".raw", ".dng", ".nef", ".nrw", ".cr2", ".cr3", ".crw", ".arw", ".srf", ".sr2", ".raf", ".orf", ".rw2", ".pef", ".srw", ".3fr", ".fff", ".iiq", ".rwl", ".mos", ".kdc", ".dcr", ".erf", ".mef", ".mrw":
		return "raw"
	case ".mp4", ".mov", ".m4v", ".mkv", ".webm", ".avi", ".mts", ".m2ts", ".mpg", ".mpeg", ".wmv", ".3gp", ".ogv":
		return "video"
	}
	return ""
}
func mediaTools() map[string]bool {
	tools := map[string]bool{}
	for _, name := range []string{"ffmpeg", "ffprobe", "heif-thumbnailer", "dcraw_emu", "exiftool"} {
		_, err := exec.LookPath(name)
		tools[name] = err == nil
	}
	return tools
}

type cappedOutput struct {
	bytes.Buffer
	limit int
}

func (b *cappedOutput) Write(p []byte) (int, error) {
	n := len(p)
	if b.Len() < b.limit {
		b.Buffer.Write(p[:min(n, b.limit-b.Len())])
	}
	return n, nil
}

// A verified open descriptor avoids resolving a library path again in a helper.
// Helpers never receive the original pathname or shell-interpolated arguments.
func mediaCommand(ctx context.Context, f *os.File, dir, name string, args ...string) ([]byte, error) {
	if _, err := exec.LookPath(name); err != nil {
		return nil, fmt.Errorf("%s is not installed; see Settings → Media support", name)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.ExtraFiles = []*os.File{f}
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "OMP_NUM_THREADS=1", "OPENBLAS_NUM_THREADS=1")
	stdout := &cappedOutput{limit: 2 << 20}
	stderr := &cappedOutput{limit: 4096}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("%s: %w", name, ctx.Err())
		}
		// Avoid returning helper diagnostics containing host filesystem paths.
		return nil, fmt.Errorf("%s could not decode this file (%v)", name, err)
	}
	return stdout.Bytes(), nil
}

const sourceFD = "/proc/self/fd/3"

func readExtendedMetadata(parent context.Context, f *os.File, p *Photo, state string) {
	ctx, cancel := context.WithTimeout(parent, 20*time.Second)
	defer cancel()
	p.Orientation = 1
	data, err := mediaCommand(ctx, f, state, "exiftool", "-j", "-n", "-d", "%s", "-ImageWidth", "-ImageHeight", "-ExifImageWidth", "-ExifImageHeight", "-RawImageWidth", "-RawImageHeight", "-XPKeywords", "-Keywords", "-Subject", "-DateTimeOriginal", "-CreateDate", sourceFD)
	if err != nil {
		p.Error = "metadata: " + err.Error()
		return
	}
	if err = applyExtendedMetadata(data, p); err != nil {
		p.Error = "metadata: " + err.Error()
	}
}
func applyExtendedMetadata(data []byte, p *Photo) error {
	var rows []map[string]any
	if err := json.Unmarshal(data, &rows); err != nil || len(rows) != 1 {
		return errors.New("invalid metadata response")
	}
	row := rows[0]
	numeric := func(key string) int {
		n, _ := strconv.ParseFloat(fmt.Sprint(row[key]), 64)
		if n < 0 || n > 200000000 {
			return 0
		}
		return int(n)
	}
	p.Width = max(numeric("ImageWidth"), numeric("ExifImageWidth"), numeric("RawImageWidth"))
	p.Height = max(numeric("ImageHeight"), numeric("ExifImageHeight"), numeric("RawImageHeight"))
	for _, key := range []string{"XPKeywords", "Keywords", "Subject"} {
		add := func(value any) {
			if text, ok := value.(string); ok {
				if key == "XPKeywords" {
					p.Tags = append(p.Tags, strings.Split(text, ";")...)
				} else {
					p.Tags = append(p.Tags, text)
				}
			}
		}
		if values, ok := row[key].([]any); ok {
			for _, v := range values {
				add(v)
			}
		} else {
			add(row[key])
		}
	}
	for _, key := range []string{"DateTimeOriginal", "CreateDate"} {
		if n, err := strconv.ParseFloat(fmt.Sprint(row[key]), 64); err == nil && n > 0 && n < 1e12 {
			p.Taken = int64(n)
			break
		}
	}
	p.Tags = cleanTags(p.Tags)
	return nil
}
func videoDemuxer(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".mp4", ".mov", ".m4v", ".3gp":
		return "mov"
	case ".mkv", ".webm":
		return "matroska"
	case ".avi":
		return "avi"
	case ".mts", ".m2ts":
		return "mpegts"
	case ".mpg", ".mpeg":
		return "mpeg"
	case ".wmv":
		return "asf"
	case ".ogv":
		return "ogg"
	}
	return ""
}
func probeVideo(ctx context.Context, f *os.File, p Photo, dir string, maxPixels int64) (float64, error) {
	data, err := mediaCommand(ctx, f, dir, "ffprobe", "-v", "error", "-protocol_whitelist", "file,pipe", "-f", videoDemuxer(p.Name), "-select_streams", "v:0", "-show_entries", "stream=width,height:format=duration", "-of", "json", sourceFD)
	if err != nil {
		return 0, err
	}
	var out struct {
		Streams []struct{ Width, Height int }
		Format  struct{ Duration string }
	}
	if json.Unmarshal(data, &out) != nil || len(out.Streams) == 0 {
		return 0, errors.New("no playable video stream")
	}
	if !withinPixelLimit(out.Streams[0].Width, out.Streams[0].Height, maxPixels) {
		return 0, errors.New("video exceeds configured max_pixels")
	}
	duration, _ := strconv.ParseFloat(out.Format.Duration, 64)
	return duration, nil
}
func withinPixelLimit(w, h int, limit int64) bool {
	return w > 0 && h > 0 && int64(w) <= limit/int64(h)
}

func (c *ImageCache) convertedSource(f *os.File, p Photo, size int) (*os.File, func(), error) {
	dir, err := os.MkdirTemp(c.dir, "convert-*.tmp")
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { os.RemoveAll(dir) }
	ctx, cancel := context.WithTimeout(c.ctx, 90*time.Second)
	defer cancel()
	output := filepath.Join(dir, "frame.jpg")
	kind := mediaKind(p.Name)
	switch kind {
	case "video":
		_, err = probeVideo(ctx, f, p, dir, c.cfg.MaxPixels)
		if err == nil {
			_, err = mediaCommand(ctx, f, dir, "ffmpeg", "-v", "error", "-nostdin", "-threads", "1", "-filter_threads", "1", "-protocol_whitelist", "file,pipe", "-f", videoDemuxer(p.Name), "-i", sourceFD, "-map", "0:v:0", "-frames:v", "1", "-vf", fmt.Sprintf("scale='min(%d,iw)':'min(%d,ih)':force_original_aspect_ratio=decrease", size, size), "-an", "-y", output)
		}
	case "raw", "heic":
		probe := Photo{Tags: []string{}}
		readExtendedMetadata(ctx, f, &probe, dir)
		if probe.Error != "" {
			err = errors.New(probe.Error)
		} else if !withinPixelLimit(probe.Width, probe.Height, c.cfg.MaxPixels) {
			err = errors.New("image dimensions unavailable or above configured max_pixels")
		}
		if err == nil {
			if kind == "heic" {
				output = filepath.Join(dir, "frame.png")
				_, err = mediaCommand(ctx, f, dir, "heif-thumbnailer", "-p", "-s", strconv.Itoa(size), sourceFD, output)
			} else {
				output = filepath.Join(dir, "frame.tiff")
				_, err = mediaCommand(ctx, f, dir, "dcraw_emu", "-q", "0", "-h", "-w", "-T", "-Z", output, sourceFD)
			}
		}
	default:
		err = errors.New("unsupported media")
	}
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	converted, err := os.Open(output)
	if err != nil {
		cleanup()
		return nil, nil, errors.New("decoder produced no preview")
	}
	return converted, func() { converted.Close(); cleanup() }, nil
}
func (c *ImageCache) generateVideo(f *os.File, job *imageJob) error {
	ctx, cancel := context.WithTimeout(c.ctx, 10*time.Minute)
	defer cancel()
	duration, err := probeVideo(ctx, f, job.p, c.dir, c.cfg.MaxPixels)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(c.dir, "video-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	tmp.Close()
	defer os.Remove(name)
	limit := min(c.cfg.CacheBytes, int64(512<<20))
	_, err = mediaCommand(ctx, f, c.dir, "ffmpeg", "-v", "error", "-nostdin", "-threads", "1", "-filter_threads", "1", "-protocol_whitelist", "file,pipe", "-f", videoDemuxer(job.p.Name), "-i", sourceFD, "-map", "0:v:0", "-map", "0:a:0?", "-vf", "scale='min(1280,iw)':'min(1280,ih)':force_original_aspect_ratio=decrease:force_divisible_by=2", "-c:v", "libx264", "-preset", "veryfast", "-crf", "25", "-pix_fmt", "yuv420p", "-threads", "1", "-c:a", "aac", "-b:a", "128k", "-map_metadata", "-1", "-map_metadata:s", "-1", "-map_chapters", "-1", "-movflags", "+faststart", "-fs", strconv.FormatInt(limit+1, 10), "-f", "mp4", "-y", name)
	if err != nil {
		return err
	}
	info, err := os.Stat(name)
	if err != nil {
		return err
	}
	if info.Size() >= limit {
		return errors.New("video preview exceeds cache item limit (up to 512 MiB)")
	}
	preview, err := os.Open(name)
	if err != nil {
		return err
	}
	actualDuration, err := probeVideo(ctx, preview, Photo{Name: "preview.mp4"}, c.dir, c.cfg.MaxPixels)
	preview.Close()
	if err != nil {
		return err
	}
	if (duration > 0 && actualDuration+0.5 < duration) || (duration <= 0 && info.Size() > limit-(128<<10)) {
		return errors.New("video preview could not fit in the cache item limit; refusing a truncated video")
	}
	return c.commitCache(name, job.key, info.Size())
}
func (a *App) video(w http.ResponseWriter, r *http.Request) {
	p, ok := a.requestPhoto(w, r)
	if !ok {
		return
	}
	if mediaKind(p.Name) != "video" {
		apiError(w, 400, "this item is not a video")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	f, err := a.cache.get(ctx, p, 0)
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, errQueueFull) {
		w.Header().Set("Retry-After", "2")
		jsonResponse(w, 202, map[string]bool{"ready": false})
		return
	}
	if err != nil {
		apiError(w, 422, err.Error())
		return
	}
	defer f.Close()
	if r.URL.Query().Get("prepare") == "1" {
		jsonResponse(w, 200, map[string]bool{"ready": true})
		return
	}
	info, err := f.Stat()
	if err != nil {
		apiError(w, 500, "could not read video preview")
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Cache-Control", "private, max-age=0, must-revalidate")
	http.ServeContent(w, r, "preview.mp4", info.ModTime(), f)
}

// The same eviction budget and worker queue cover thumbnails, still previews,
// and seekable MP4 video previews.
func (c *ImageCache) commitCache(name, key string, size int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.evict(size); err != nil {
		return err
	}
	if err := os.Rename(name, filepath.Join(c.dir, key)); err != nil {
		return err
	}
	c.entries[key] = c.lru.PushFront(cacheEntry{key, size})
	c.bytes += size
	return nil
}
