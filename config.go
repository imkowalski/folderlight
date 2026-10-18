package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	Library    string `json:"library"`
	State      string `json:"state"`
	Listen     string `json:"listen"`
	CacheBytes int64  `json:"cache_bytes"`
	Workers    int    `json:"workers"`
	MaxPixels  int64  `json:"max_pixels"`
}

func loadConfig(filename string, recovery ...bool) (Config, error) {
	c := Config{Listen: "127.0.0.1:8080", CacheBytes: 5 << 30, Workers: 1, MaxPixels: 40_000_000}
	f, err := os.Open(filename)
	if err != nil {
		return c, err
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 64<<10))
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil {
		return c, err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return c, errors.New("config must contain one JSON object")
	}
	if c.Library == "" || c.State == "" {
		return c, errors.New("library and state are required")
	}
	base, err := filepath.Abs(filepath.Dir(filename))
	if err != nil {
		return c, err
	}
	if !filepath.IsAbs(c.Library) {
		c.Library = filepath.Join(base, c.Library)
	}
	if !filepath.IsAbs(c.State) {
		c.State = filepath.Join(base, c.State)
	}
	reset := len(recovery) > 0 && recovery[0]
	if reset {
		c.Library, err = resolvePlannedPath(c.Library)
	} else {
		c.Library, err = filepath.EvalSymlinks(c.Library)
	}
	if err != nil {
		return c, fmt.Errorf("library: %w", err)
	}
	info, err := os.Stat(c.Library)
	if !reset && (err != nil || !info.IsDir()) {
		return c, errors.New("library must be an existing directory")
	}
	if c.Workers < 1 || c.Workers > 8 || c.CacheBytes < 1<<20 || c.MaxPixels < 1 || c.MaxPixels > 200_000_000 || c.Listen == "" {
		return c, errors.New("invalid limits: workers 1–8, cache >= 1 MiB, max_pixels 1–200 million, listen required")
	}
	c.State, err = resolvePlannedPath(c.State)
	if err != nil {
		return c, err
	}
	if within(c.Library, c.State) || within(c.State, c.Library) {
		return c, errors.New("library and state must be separate, non-overlapping directories")
	}
	if err = os.MkdirAll(c.State, 0700); err != nil {
		return c, err
	}
	return c, nil
}

func within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && !filepath.IsAbs(rel)
}

// Resolve existing parent symlinks without creating directories before validation.
func resolvePlannedPath(p string) (string, error) {
	p = filepath.Clean(p)
	candidate := p
	var tail []string
	for {
		resolved, err := filepath.EvalSymlinks(candidate)
		if err == nil {
			for i := len(tail) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, tail[i])
			}
			return resolved, nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		next := filepath.Dir(candidate)
		if next == candidate {
			return "", err
		}
		tail = append(tail, filepath.Base(candidate))
		candidate = next
	}
}
