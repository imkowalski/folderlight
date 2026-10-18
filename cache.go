package main

import (
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"golang.org/x/image/draw"
)

var errQueueFull = errors.New("image queue is full; try again shortly")

type cacheEntry struct {
	name string
	size int64
}
type imageJob struct {
	key      string
	p        Photo
	size     int
	done     chan struct{}
	err      error
	contexts []context.Context
}
type ImageCache struct {
	cfg     Config
	dir     string
	mu      sync.Mutex
	entries map[string]*list.Element
	lru     *list.List
	bytes   int64
	pending map[string]*imageJob
	high    chan *imageJob
	low     chan *imageJob
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

func newCache(ctx context.Context, cfg Config) (*ImageCache, error) {
	ctx, cancel := context.WithCancel(ctx)
	c := &ImageCache{cfg: cfg, dir: filepath.Join(cfg.State, "cache"), entries: map[string]*list.Element{}, lru: list.New(), pending: map[string]*imageJob{}, high: make(chan *imageJob, 16), low: make(chan *imageJob, 64), ctx: ctx, cancel: cancel}
	if err := os.MkdirAll(c.dir, 0700); err != nil {
		cancel()
		return nil, err
	}
	files, err := os.ReadDir(c.dir)
	if err != nil {
		cancel()
		return nil, err
	}
	type disk struct {
		name     string
		size     int64
		modified time.Time
	}
	var saved []disk
	for _, f := range files {
		if f.IsDir() {
			continue
		}
		if filepath.Ext(f.Name()) == ".tmp" {
			if err = os.Remove(filepath.Join(c.dir, f.Name())); err != nil {
				cancel()
				return nil, err
			}
			continue
		}
		if len(f.Name()) != 68 || filepath.Ext(f.Name()) != ".jpg" {
			continue
		}
		info, e := f.Info()
		if e != nil {
			cancel()
			return nil, e
		}
		saved = append(saved, disk{f.Name(), info.Size(), info.ModTime()})
	}
	sort.Slice(saved, func(i, j int) bool { return saved[i].modified.Before(saved[j].modified) })
	for _, f := range saved {
		c.entries[f.name] = c.lru.PushFront(cacheEntry{f.name, f.size})
		c.bytes += f.size
	}
	if err = c.evict(0); err != nil {
		cancel()
		return nil, err
	}
	for range cfg.Workers {
		c.wg.Add(1)
		go c.worker()
	}
	return c, nil
}
func (c *ImageCache) close() { c.cancel(); c.wg.Wait() }
func (c *ImageCache) evict(incoming int64) error {
	for c.bytes+incoming > c.cfg.CacheBytes {
		el := c.lru.Back()
		if el == nil {
			return errors.New("generated image exceeds cache limit")
		}
		entry := el.Value.(cacheEntry)
		if err := os.Remove(filepath.Join(c.dir, entry.name)); err != nil && !os.IsNotExist(err) {
			return err
		}
		delete(c.entries, entry.name)
		c.lru.Remove(el)
		c.bytes -= entry.size
	}
	return nil
}
func cacheKey(p Photo, size int) string {
	h := sha256.Sum256(fmt.Appendf(nil, "v1:%s:%d:%d:%d:%d", p.Path, p.Size, p.Mtime, p.Orientation, size))
	return hex.EncodeToString(h[:]) + ".jpg"
}
func (c *ImageCache) open(key string) (*os.File, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	f, err := os.Open(filepath.Join(c.dir, key))
	if err != nil {
		return nil, err
	}
	if el := c.entries[key]; el != nil {
		c.lru.MoveToFront(el)
		now := time.Now()
		_ = os.Chtimes(filepath.Join(c.dir, key), now, now)
	}
	return f, nil
}
func (c *ImageCache) get(ctx context.Context, p Photo, size int) (*os.File, error) {
	key := cacheKey(p, size)
	for range 2 {
		if f, err := c.open(key); err == nil {
			return f, nil
		}
		c.mu.Lock()
		job := c.pending[key]
		if job == nil {
			job = &imageJob{key: key, p: p, size: size, done: make(chan struct{})}
			c.pending[key] = job
			queue := c.low
			if size > 480 {
				queue = c.high
			}
			select {
			case queue <- job:
			default:
				delete(c.pending, key)
				c.mu.Unlock()
				return nil, errQueueFull
			}
		}
		job.contexts = append(job.contexts, ctx)
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-c.ctx.Done():
			return nil, c.ctx.Err()
		case <-job.done:
			if job.err != nil {
				return nil, job.err
			}
		}
	}
	return nil, errors.New("image was evicted before it could be opened; retry")
}
func (c *ImageCache) worker() {
	defer c.wg.Done()
	for {
		var job *imageJob
		select {
		case <-c.ctx.Done():
			return
		case job = <-c.high:
		default:
			select {
			case <-c.ctx.Done():
				return
			case job = <-c.high:
			case job = <-c.low:
			}
		}
		c.mu.Lock()
		active := false
		for _, ctx := range job.contexts {
			if ctx.Err() == nil {
				active = true
				break
			}
		}
		c.mu.Unlock()
		var err error
		if !active {
			err = context.Canceled
		} else {
			err = c.generate(job)
		}
		c.mu.Lock()
		job.err = err
		delete(c.pending, job.key)
		close(job.done)
		c.mu.Unlock()
	}
}
func (c *ImageCache) generate(job *imageJob) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("image decode failed: %v", r)
		}
	}()
	root, err := os.OpenRoot(c.cfg.Library)
	if err != nil {
		return err
	}
	defer root.Close()
	f, err := openSource(root, job.p.Path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.Size() != job.p.Size || info.ModTime().UnixNano() != job.p.Mtime {
		return errors.New("image changed; rescan the library")
	}
	config, _, err := image.DecodeConfig(f)
	if err != nil {
		return err
	}
	if config.Width < 1 || config.Height < 1 || int64(config.Width) > c.cfg.MaxPixels/int64(config.Height) {
		return errors.New("image exceeds configured max_pixels")
	}
	if _, err = f.Seek(0, 0); err != nil {
		return err
	}
	src, _, err := image.Decode(f)
	if err != nil {
		return err
	}
	oriented := orientedImage{src: src, orientation: job.p.Orientation}
	bounds := oriented.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if max(w, h) > job.size {
		ratio := float64(job.size) / float64(max(w, h))
		w = max(1, int(float64(w)*ratio))
		h = max(1, int(float64(h)*ratio))
	}
	output := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(output, output.Bounds(), &image.Uniform{color.RGBA{245, 245, 245, 255}}, image.Point{}, draw.Src)
	draw.CatmullRom.Scale(output, output.Bounds(), oriented, bounds, draw.Over, nil)
	tmp, err := os.CreateTemp(c.dir, "render-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err = jpeg.Encode(tmp, output, &jpeg.Options{Quality: 85}); err != nil {
		tmp.Close()
		return err
	}
	info, err = tmp.Stat()
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err = c.evict(info.Size()); err != nil {
		return err
	}
	if err = os.Rename(tmp.Name(), filepath.Join(c.dir, job.key)); err != nil {
		return err
	}
	c.entries[job.key] = c.lru.PushFront(cacheEntry{job.key, info.Size()})
	c.bytes += info.Size()
	return nil
}

// Present a transformed view without allocating a second full-size image.
type orientedImage struct {
	src         image.Image
	orientation int
}

func (o orientedImage) ColorModel() color.Model { return o.src.ColorModel() }
func (o orientedImage) Bounds() image.Rectangle {
	b := o.src.Bounds()
	w, h := b.Dx(), b.Dy()
	if o.orientation >= 5 && o.orientation <= 8 {
		w, h = h, w
	}
	return image.Rect(0, 0, w, h)
}
func (o orientedImage) At(x, y int) color.Color {
	b := o.src.Bounds()
	w, h := b.Dx(), b.Dy()
	var sx, sy int
	switch o.orientation {
	case 2:
		sx, sy = w-1-x, y
	case 3:
		sx, sy = w-1-x, h-1-y
	case 4:
		sx, sy = x, h-1-y
	case 5:
		sx, sy = y, x
	case 6:
		sx, sy = y, h-1-x
	case 7:
		sx, sy = w-1-y, h-1-x
	case 8:
		sx, sy = w-1-y, x
	default:
		sx, sy = x, y
	}
	return o.src.At(b.Min.X+sx, b.Min.Y+sy)
}
