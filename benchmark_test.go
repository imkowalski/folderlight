package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// Opt-in integration benchmark: real directory traversal and metadata extraction,
// with a synthetic tiny JPEG corpus. It does not measure large-file/NAS I/O or resizing.
func TestLargeLibrary(t *testing.T) {
	if os.Getenv("FOLDERLIGHT_BENCH") != "1" {
		t.Skip("set FOLDERLIGHT_BENCH=1 to benchmark 100,000 images")
	}
	c := testConfig(t)
	s := testStore(t, c)
	b := taggedJPEG("Family;Sommer;Łódź;Ærø", 1)
	seed := filepath.Join(c.State, "seed.jpg")
	if err := os.WriteFile(seed, b, 0600); err != nil {
		t.Fatal(err)
	}
	for d := 0; d < 1000; d++ {
		folder := filepath.Join(c.Library, fmt.Sprintf("Album%04d", d))
		if err := os.Mkdir(folder, 0700); err != nil {
			t.Fatal(err)
		}
		for n := 0; n < 100; n++ {
			if err := os.Link(seed, filepath.Join(folder, fmt.Sprintf("photo%05d.jpg", d*100+n))); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Logf("runtime=%s architecture=%s CPUs=%d corpus=100000 hardlinked %d-byte JPEGs / 1000 directories / 4 tags", runtime.Version(), runtime.GOARCH, runtime.NumCPU(), len(b))
	var peak atomic.Uint64
	stop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				var m runtime.MemStats
				runtime.ReadMemStats(&m)
				if m.Alloc > peak.Load() {
					peak.Store(m.Alloc)
				}
			}
		}
	}()
	defer close(stop)
	start := time.Now()
	status := scanNow(t, s, c, false)
	initial := time.Since(start)
	if status.Errors != 0 || status.Updated != 100000 {
		t.Fatalf("scan failed: %+v", status)
	}
	start = time.Now()
	status = scanNow(t, s, c, false)
	incremental := time.Since(start)
	if status.Errors != 0 || status.Updated != 0 || status.Visited != 100000 {
		t.Fatalf("incremental scan failed: %+v", status)
	}
	queries := []struct {
		name string
		q    Query
	}{{"root browse", Query{Page: 1, Limit: 60}}, {"folder tree tag", Query{Folder: "Album0500", Tag: "ÆRØ", Scope: "tree", Page: 1, Limit: 60}}, {"global filename fragment", Query{Search: "50001", Scope: "all", Page: 1, Limit: 60}}, {"global common tag AND filename", Query{Search: "ŁÓDŹ photo500", Scope: "all", Page: 1, Limit: 60}}, {"global common exact tag", Query{Tag: "Family", Scope: "all", Page: 1, Limit: 60}}, {"global short fragment", Query{Search: "01", Scope: "all", Page: 1, Limit: 60}}}
	for _, tc := range queries {
		var times []time.Duration
		for range 20 {
			start = time.Now()
			if _, err := s.list(context.Background(), tc.q); err != nil {
				t.Fatal(err)
			}
			times = append(times, time.Since(start))
		}
		sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
		t.Logf("%s: median=%s p95=%s (20 warm runs)", tc.name, times[len(times)/2].Round(time.Microsecond), times[18].Round(time.Microsecond))
	}
	var usage syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &usage)
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	var dbBytes int64
	for _, ext := range []string{"", "-wal", "-shm"} {
		if f, e := os.Stat(filepath.Join(c.State, "library.sqlite"+ext)); e == nil {
			dbBytes += f.Size()
		}
	}
	t.Logf("initial scan=%s incremental scan=%s peak Go live heap=%.1f MiB process max RSS=%.1f MiB Go reserved=%.1f MiB database+WAL=%.1f MiB", initial.Round(time.Millisecond), incremental.Round(time.Millisecond), float64(peak.Load())/(1<<20), float64(usage.Maxrss)/1024, float64(m.Sys)/(1<<20), float64(dbBytes)/(1<<20))
}
