package main

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"sync"
	"testing"
	"unicode/utf16"
)

func testConfig(t *testing.T) Config {
	t.Helper()
	base := t.TempDir()
	c := Config{Library: filepath.Join(base, "photos"), State: filepath.Join(base, "state"), Listen: "127.0.0.1:8080", CacheBytes: 1 << 20, Workers: 1, MaxPixels: 40_000_000}
	for _, p := range []string{c.Library, c.State} {
		if err := os.MkdirAll(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	return c
}
func testStore(t *testing.T, c Config) *Store {
	t.Helper()
	s, err := openStore(c.State)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.db.Close() })
	return s
}
func scanNow(t *testing.T, s *Store, c Config, force bool) ScanStatus {
	t.Helper()
	ix := &Indexer{store: s, config: c}
	ix.run(context.Background(), force)
	return ix.snapshot()
}
func writeImage(t *testing.T, c Config, name string, b []byte) {
	t.Helper()
	p := filepath.Join(c.Library, name)
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, b, 0600); err != nil {
		t.Fatal(err)
	}
}
func baseJPEG() []byte {
	m := image.NewRGBA(image.Rect(0, 0, 24, 16))
	for y := 0; y < 16; y++ {
		for x := 0; x < 24; x++ {
			m.Set(x, y, color.RGBA{uint8(x * 10), uint8(y * 15), 90, 255})
		}
	}
	var b bytes.Buffer
	_ = jpeg.Encode(&b, m, &jpeg.Options{Quality: 90})
	return b.Bytes()
}
func xpEXIF(keywords string, orientation uint16) []byte {
	var data bytes.Buffer
	for _, u := range utf16.Encode([]rune(keywords + "\x00")) {
		_ = binary.Write(&data, binary.LittleEndian, u)
	}
	b := make([]byte, 38+data.Len())
	copy(b, "II")
	binary.LittleEndian.PutUint16(b[2:], 42)
	binary.LittleEndian.PutUint32(b[4:], 8)
	binary.LittleEndian.PutUint16(b[8:], 2)
	binary.LittleEndian.PutUint16(b[10:], 0x0112)
	binary.LittleEndian.PutUint16(b[12:], 3)
	binary.LittleEndian.PutUint32(b[14:], 1)
	binary.LittleEndian.PutUint16(b[18:], orientation)
	binary.LittleEndian.PutUint16(b[22:], 0x9c9e)
	binary.LittleEndian.PutUint16(b[24:], 1)
	binary.LittleEndian.PutUint32(b[26:], uint32(data.Len()))
	binary.LittleEndian.PutUint32(b[30:], 38)
	copy(b[38:], data.Bytes())
	return b
}
func jpegSegment(marker byte, payload []byte) []byte {
	var b bytes.Buffer
	b.Write([]byte{0xff, marker})
	_ = binary.Write(&b, binary.BigEndian, uint16(len(payload)+2))
	b.Write(payload)
	return b.Bytes()
}
func taggedJPEG(tags string, orientation uint16) []byte {
	base := baseJPEG()
	segment := jpegSegment(0xe1, append([]byte("Exif\x00\x00"), xpEXIF(tags, orientation)...))
	return append(append(append([]byte{}, base[:2]...), segment...), base[2:]...)
}

func TestWindowsTagsAndOrientation(t *testing.T) {
	p := Photo{Tags: []string{}}
	readMetadata(bytes.NewReader(taggedJPEG(" Sommer ; Łódź ; ÆØÅ ; sommer ; rodziną ; 🚲", 6)), &p)
	if p.Error != "" {
		t.Fatal(p.Error)
	}
	if want := []string{"Sommer", "Łódź", "ÆØÅ", "rodziną", "🚲"}; !reflect.DeepEqual(p.Tags, want) {
		t.Fatalf("%q != %q", p.Tags, want)
	}
	if p.Width != 24 || p.Height != 16 || p.Orientation != 6 {
		t.Fatalf("wrong geometry: %+v", p)
	}
}

func TestIPTCAndXMP(t *testing.T) {
	base := baseJPEG()
	xmp := `<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"><rdf:Description xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:subject><rdf:Bag><rdf:li>Łódź</rdf:li><rdf:li>Family</rdf:li><rdf:li>Ærø</rdf:li></rdf:Bag></dc:subject></rdf:Description></rdf:RDF></x:xmpmeta>`
	var iptc bytes.Buffer
	iptc.Write([]byte{0x1c, 1, 90, 0, 3, 0x1b, 0x25, 0x47})
	for _, tag := range []string{"Family", "Beach", "København", "Gdańsk"} {
		iptc.Write([]byte{0x1c, 2, 25})
		_ = binary.Write(&iptc, binary.BigEndian, uint16(len(tag)))
		iptc.WriteString(tag)
	}
	var resource bytes.Buffer
	resource.WriteString("Photoshop 3.0\x00")
	resource.WriteString("8BIM")
	_ = binary.Write(&resource, binary.BigEndian, uint16(0x0404))
	resource.Write([]byte{0, 0})
	_ = binary.Write(&resource, binary.BigEndian, uint32(iptc.Len()))
	resource.Write(iptc.Bytes())
	if iptc.Len()%2 != 0 {
		resource.WriteByte(0)
	}
	b := append([]byte{}, base[:2]...)
	b = append(b, jpegSegment(0xe1, append([]byte("http://ns.adobe.com/xap/1.0/\x00"), []byte(xmp)...))...)
	b = append(b, jpegSegment(0xed, resource.Bytes())...)
	b = append(b, base[2:]...)
	p := Photo{Tags: []string{}}
	readMetadata(bytes.NewReader(b), &p)
	if p.Error != "" {
		t.Fatal(p.Error)
	}
	sort.Strings(p.Tags)
	want := []string{"Beach", "Family", "Ærø", "Łódź", "København", "Gdańsk"}
	sort.Strings(want)
	if !reflect.DeepEqual(p.Tags, want) {
		t.Fatalf("got %q want %q", p.Tags, want)
	}
}

func pngChunk(kind string, payload []byte) []byte {
	var b bytes.Buffer
	_ = binary.Write(&b, binary.BigEndian, uint32(len(payload)))
	b.WriteString(kind)
	b.Write(payload)
	_ = binary.Write(&b, binary.BigEndian, crc32.ChecksumIEEE(b.Bytes()[4:]))
	return b.Bytes()
}
func TestPNGXMPAndCompressedMetadataLimit(t *testing.T) {
	xmp := []byte(`<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"><rdf:Description xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:subject><rdf:Bag><rdf:li>Ærø</rdf:li><rdf:li>Łódź</rdf:li></rdf:Bag></dc:subject></rdf:Description></rdf:RDF></x:xmpmeta>`)
	var base bytes.Buffer
	_ = png.Encode(&base, image.NewRGBA(image.Rect(0, 0, 3, 2)))
	for _, compressed := range []bool{false, true} {
		data := xmp
		flag := byte(0)
		if compressed {
			flag = 1
			var out bytes.Buffer
			z := zlib.NewWriter(&out)
			_, _ = z.Write(data)
			_ = z.Close()
			data = out.Bytes()
		}
		packet := append([]byte("XML:com.adobe.xmp\x00"), flag, 0, 0, 0)
		packet = append(packet, data...)
		b := append(append(append([]byte{}, base.Bytes()[:33]...), pngChunk("iTXt", packet)...), base.Bytes()[33:]...)
		p := Photo{Tags: []string{}}
		readMetadata(bytes.NewReader(b), &p)
		if p.Error != "" || !reflect.DeepEqual(p.Tags, []string{"Ærø", "Łódź"}) {
			t.Fatalf("compressed=%v: %+v", compressed, p)
		}
	}
	var bomb bytes.Buffer
	z := zlib.NewWriter(&bomb)
	_, _ = z.Write(bytes.Repeat([]byte{'x'}, (2<<20)+1))
	_ = z.Close()
	if _, err := limitedInflate(bomb.Bytes()); err == nil {
		t.Fatal("unbounded compressed metadata accepted")
	}
}

func TestPNGWindowsTagsAndGIF(t *testing.T) {
	var plain bytes.Buffer
	_ = png.Encode(&plain, image.NewRGBA(image.Rect(0, 0, 3, 2)))
	payload := xpEXIF("Ærø;Łódź", 1)
	var chunk bytes.Buffer
	_ = binary.Write(&chunk, binary.BigEndian, uint32(len(payload)))
	chunk.WriteString("eXIf")
	chunk.Write(payload)
	crc := crc32.ChecksumIEEE(chunk.Bytes()[4:])
	_ = binary.Write(&chunk, binary.BigEndian, crc)
	b := plain.Bytes()
	b = append(append(append([]byte{}, b[:33]...), chunk.Bytes()...), b[33:]...)
	p := Photo{Tags: []string{}}
	readMetadata(bytes.NewReader(b), &p)
	if p.Error != "" || len(p.Tags) != 2 {
		t.Fatalf("PNG: %+v", p)
	}
	var gifFile bytes.Buffer
	_ = gif.Encode(&gifFile, image.NewPaletted(image.Rect(0, 0, 3, 2), []color.Color{color.Black, color.White}), nil)
	p = Photo{Tags: []string{}}
	readMetadata(bytes.NewReader(gifFile.Bytes()), &p)
	if p.Error != "" || p.Width != 3 {
		t.Fatalf("GIF: %+v", p)
	}
}

func TestSearchScopesNaturalSortAndPagination(t *testing.T) {
	c := testConfig(t)
	s := testStore(t, c)
	ctx := context.Background()
	for _, name := range []string{"Trip/photo10.jpg", "Trip/photo2.jpg", "Trip/Nested/Łódź.jpg", "Other/photo1.jpg"} {
		p := Photo{Path: name, Folder: parent(name), Name: filepath.Base(name), Taken: 1, Tags: []string{"Ærø", "family holiday"}, Orientation: 1}
		if err := s.put(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	query := func(q Query) Listing {
		t.Helper()
		q.Page = max(1, q.Page)
		q.Limit = max(1, q.Limit)
		out, err := s.list(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if out := query(Query{Folder: "Trip", Limit: 60}); out.Total != 2 || out.Photos[0].Name != "photo2.jpg" {
		t.Fatalf("natural browse: %+v", out)
	}
	cases := []struct {
		q    Query
		want int
	}{{Query{Folder: "Trip", Search: "photo", Scope: "tree"}, 2}, {Query{Folder: "Trip", Search: "ŁÓDŹ", Scope: "tree"}, 1}, {Query{Folder: "Trip", Search: "ŁÓDŹ", Scope: "folder"}, 0}, {Query{Folder: "Trip", Search: "photo", Scope: "all"}, 3}, {Query{Folder: "Tri", Search: "photo", Scope: "tree"}, 0}, {Query{Search: "ÆRØ holiday", Scope: "all"}, 4}, {Query{Search: "oto2", Scope: "all"}, 1}, {Query{Search: "2", Scope: "all"}, 1}, {Query{Search: "%_", Scope: "all"}, 0}, {Query{Tag: "ÆRØ", Scope: "all"}, 4}, {Query{Tag: "family", Scope: "all"}, 0}}
	for _, tc := range cases {
		tc.q.Limit = 60
		if out := query(tc.q); out.Total != tc.want {
			t.Fatalf("%+v: %d != %d", tc.q, out.Total, tc.want)
		}
	}
	if out := query(Query{Search: "photo", Scope: "all", Limit: 1, Page: 2}); out.Total != 3 || out.Page != 2 || out.Pages != 3 || len(out.Photos) != 1 {
		t.Fatalf("pagination: %+v", out)
	}
	// Updating tags removes old FTS matches and keeps stable image ids.
	p, _ := s.photo(ctx, 1)
	p.Tags = []string{"updated"}
	if err := s.put(ctx, p); err != nil {
		t.Fatal(err)
	}
	if out := query(Query{Tag: "ÆRØ", Scope: "all", Limit: 60}); out.Total != 3 {
		t.Fatal("stale tag survived update")
	}
}

func TestIncrementalScanDeletionForceAndReadOnly(t *testing.T) {
	c := testConfig(t)
	s := testStore(t, c)
	b := taggedJPEG("AAAA", 1)
	writeImage(t, c, "Trip/photo2.jpg", b)
	writeImage(t, c, "Trip/Nested/photo10.jpg", b)
	writeImage(t, c, "bad.jpg", []byte("broken"))
	if err := os.Symlink("/etc", filepath.Join(c.Library, "outside")); err != nil {
		t.Fatal(err)
	}
	if st := scanNow(t, s, c, false); st.Updated != 3 || st.Errors != 1 {
		t.Fatalf("initial scan: %+v", st)
	}
	if st := scanNow(t, s, c, false); st.Updated != 0 {
		t.Fatalf("unchanged scan: %+v", st)
	}
	file := filepath.Join(c.Library, "Trip/photo2.jpg")
	info, _ := os.Stat(file)
	writeImage(t, c, "Trip/photo2.jpg", taggedJPEG("BBBB", 1))
	_ = os.Chtimes(file, info.ModTime(), info.ModTime())
	if st := scanNow(t, s, c, false); st.Updated != 0 {
		t.Fatal(st)
	}
	q := Query{Search: "AAAA", Scope: "all", Page: 1, Limit: 60}
	out, _ := s.list(context.Background(), q)
	if out.Total != 2 {
		t.Fatal("incremental read should preserve tags until force")
	}
	scanNow(t, s, c, true)
	q.Search = "BBBB"
	out, _ = s.list(context.Background(), q)
	if out.Total != 1 {
		t.Fatal("forced metadata update failed")
	}
	if err := os.RemoveAll(filepath.Join(c.Library, "Trip/Nested")); err != nil {
		t.Fatal(err)
	}
	scanNow(t, s, c, false)
	var count int
	_ = s.db.QueryRow(`SELECT count(*) FROM images`).Scan(&count)
	if count != 2 {
		t.Fatalf("stale subtree: %d", count)
	}
	// A readonly library must be enough for both indexing and preview generation.
	_ = os.Chmod(file, 0400)
	_ = os.Chmod(filepath.Join(c.Library, "Trip"), 0500)
	_ = os.Chmod(c.Library, 0500)
	t.Cleanup(func() { _ = os.Chmod(c.Library, 0700); _ = os.Chmod(filepath.Join(c.Library, "Trip"), 0700) })
	before, _ := os.ReadFile(file)
	scanNow(t, s, c, true)
	after, _ := os.ReadFile(file)
	if !bytes.Equal(before, after) {
		t.Fatal("source image was modified")
	}
	entries, _ := os.ReadDir(c.Library)
	if len(entries) != 3 {
		t.Fatal("unexpected library files")
	}
}

func TestUnavailableLibraryAndUnreadableSubtree(t *testing.T) {
	c := testConfig(t)
	s := testStore(t, c)
	writeImage(t, c, "Trip/photo.jpg", baseJPEG())
	scanNow(t, s, c, false)
	trip := filepath.Join(c.Library, "Trip")
	_ = os.Chmod(trip, 0000)
	st := scanNow(t, s, c, false)
	_ = os.Chmod(trip, 0700)
	if st.Errors == 0 {
		t.Fatal("expected unreadable subtree warning")
	}
	var count int
	_ = s.db.QueryRow(`SELECT count(*) FROM images`).Scan(&count)
	if count != 1 {
		t.Fatal("unreadable subtree was pruned")
	}
	moved := c.Library + "-offline"
	if err := os.Rename(c.Library, moved); err != nil {
		t.Fatal(err)
	}
	defer os.Rename(moved, c.Library)
	_ = os.Mkdir(c.Library, 0700)
	defer os.Remove(c.Library)
	st = scanNow(t, s, c, false)
	if st.Errors == 0 {
		t.Fatal("changed root should warn")
	}
	_ = s.db.QueryRow(`SELECT count(*) FROM images`).Scan(&count)
	if count != 1 {
		t.Fatal("unavailable mount was pruned")
	}
}

func TestInterruptedScanAndPersistence(t *testing.T) {
	c := testConfig(t)
	s, err := openStore(c.State)
	if err != nil {
		t.Fatal(err)
	}
	writeImage(t, c, "keep.jpg", baseJPEG())
	scanNow(t, s, c, false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ix := &Indexer{store: s, config: c}
	ix.run(ctx, false)
	s.db.Close()
	s, err = openStore(c.State)
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	out, err := s.list(context.Background(), Query{Page: 1, Limit: 60})
	if err != nil || out.Total != 1 {
		t.Fatalf("interruption/persistence: %+v %v", out, err)
	}
}

func TestOrientationTransforms(t *testing.T) {
	src := image.NewGray(image.Rect(0, 0, 3, 2))
	copy(src.Pix, []byte{1, 2, 3, 4, 5, 6})
	expected := [][]uint8{{1, 2, 3, 4, 5, 6}, {3, 2, 1, 6, 5, 4}, {6, 5, 4, 3, 2, 1}, {4, 5, 6, 1, 2, 3}, {1, 4, 2, 5, 3, 6}, {4, 1, 5, 2, 6, 3}, {6, 3, 5, 2, 4, 1}, {3, 6, 2, 5, 1, 4}}
	for o := 1; o <= 8; o++ {
		view := orientedImage{src, o}
		var got []uint8
		for y := 0; y < view.Bounds().Dy(); y++ {
			for x := 0; x < view.Bounds().Dx(); x++ {
				got = append(got, color.GrayModel.Convert(view.At(x, y)).(color.Gray).Y)
			}
		}
		if !reflect.DeepEqual(got, expected[o-1]) {
			t.Fatalf("orientation %d: %v", o, got)
		}
	}
}

func TestCacheConcurrencyPixelLimitAndEviction(t *testing.T) {
	c := testConfig(t)
	s := testStore(t, c)
	writeImage(t, c, "photo.jpg", taggedJPEG("Test", 6))
	scanNow(t, s, c, false)
	p, _ := s.photo(context.Background(), 1)
	cache, err := newCache(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	defer cache.close()
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f, e := cache.get(context.Background(), p, 480)
			if e != nil {
				t.Error(e)
				return
			}
			defer f.Close()
			config, _, e := image.DecodeConfig(f)
			if e != nil || config.Width != 16 || config.Height != 24 {
				t.Errorf("preview config: %+v %v", config, e)
			}
		}()
	}
	wg.Wait()
	cache.mu.Lock()
	if len(cache.entries) != 1 {
		t.Error("concurrent cache generation was not deduplicated")
	}
	cache.mu.Unlock()
	c2 := c
	c2.MaxPixels = 10
	c2.State = filepath.Join(t.TempDir(), "state")
	limited, err := newCache(context.Background(), c2)
	if err != nil {
		t.Fatal(err)
	}
	defer limited.close()
	if f, e := limited.get(context.Background(), p, 480); e == nil {
		f.Close()
		t.Fatal("oversized image was decoded")
	}
	cache.mu.Lock()
	cache.cfg.CacheBytes = 1
	err = cache.evict(0)
	empty := cache.bytes == 0
	cache.mu.Unlock()
	if err != nil || !empty {
		t.Fatalf("cache cap not enforced: %v", err)
	}
}

func TestSourceContainmentAndHTTP(t *testing.T) {
	c := testConfig(t)
	s := testStore(t, c)
	writeImage(t, c, "photo.jpg", baseJPEG())
	scanNow(t, s, c, false)
	_ = os.Symlink("/etc/passwd", filepath.Join(c.Library, "escape.jpg"))
	_ = os.Symlink("photo.jpg", filepath.Join(c.Library, "inside.jpg"))
	root, err := os.OpenRoot(c.Library)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, p := range []string{"../etc/passwd", "/etc/passwd", "escape.jpg", "inside.jpg", "a/../photo.jpg", "a\\b.jpg"} {
		if f, e := openSource(root, p); e == nil {
			f.Close()
			t.Errorf("unsafe source accepted: %s", p)
		}
	}
	cache, err := newCache(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	defer cache.close()
	ix := &Indexer{store: s, config: c}
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); ix.wg.Wait() }()
	app := &App{s, ix, cache, ctx}
	handler := app.handler()
	for _, tc := range []struct {
		method, path string
		code         int
	}{{"GET", "/", 200}, {"GET", "/app.js", 200}, {"GET", "/robots.txt", 200}, {"GET", "/api/browse?folder=..", 400}, {"GET", "/api/tags", 200}, {"GET", "/api/tags?scope=wrong", 400}, {"GET", "/api/tags?limit=999", 400}, {"GET", "/api/tags?folder=..", 400}, {"GET", "/api/search?q=tag%3A", 400}, {"GET", "/api/browse?scope=invalid", 400}, {"GET", "/api/browse?limit=121", 400}, {"GET", "/api/browse?page=-1", 400}, {"GET", "/api/image?id=1", 200}, {"GET", "/api/image?id=1&size=large", 400}, {"GET", "/api/photo?id=999", 404}, {"POST", "/api/rescan", 403}} {
		r := httptest.NewRequest(tc.method, tc.path, nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != tc.code {
			t.Errorf("%s: got %d want %d (%s)", tc.path, w.Code, tc.code, w.Body.String())
		}
		if w.Header().Get("X-Robots-Tag") == "" {
			t.Error("missing noindex header")
		}
	}
	r := httptest.NewRequest("POST", "/api/rescan", nil)
	r.Header.Set("X-Folderlight-Request", "1")
	r.Header.Set("Origin", "https://other.example")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("cross-origin rescan accepted")
	}
	r = httptest.NewRequest("GET", "/api/browse", nil)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	var result Listing
	if err = json.Unmarshal(w.Body.Bytes(), &result); err != nil || result.Total != 1 {
		t.Fatalf("API output %v %s", err, w.Body.String())
	}
	// Conditional requests still get a private ETag and can return 304.
	r = httptest.NewRequest("GET", "/api/image?id=1", nil)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	etag := w.Header().Get("ETag")
	r = httptest.NewRequest("GET", "/api/image?id=1", nil)
	r.Header.Set("If-None-Match", etag)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusNotModified {
		t.Fatalf("ETag failed: %d", w.Code)
	}
}

func TestConfigValidation(t *testing.T) {
	c := testConfig(t)
	for _, tc := range []struct {
		name   string
		change func(*Config)
		valid  bool
	}{{"valid", func(*Config) {}, true}, {"inside library", func(c *Config) { c.State = filepath.Join(c.Library, "state") }, false}, {"overlap parent", func(c *Config) { c.State = filepath.Dir(c.Library) }, false}, {"zero workers", func(c *Config) { c.Workers = 0 }, false}, {"huge pixel limit", func(c *Config) { c.MaxPixels = 300_000_000 }, false}} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := c
			tc.change(&cfg)
			b, _ := json.Marshal(cfg)
			f := filepath.Join(t.TempDir(), "config.json")
			_ = os.WriteFile(f, b, 0600)
			_, err := loadConfig(f)
			if (err == nil) != tc.valid {
				t.Fatalf("validation: %v", err)
			}
		})
	}
}

func TestWebPHeaderAndPreview(t *testing.T) {
	// A 3x2 solid-color, lossless WebP fixture. Keep it inline so tests do not
	// depend on a developer's GOPATH or third-party module testdata.
	b, err := hex.DecodeString("524946461e000000574542505650384c110000002f024000000750b2cad4b2ff8188e87f0000")
	if err != nil {
		t.Fatal(err)
	}
	p := Photo{Tags: []string{}}
	readMetadata(bytes.NewReader(b), &p)
	if p.Width != 3 || p.Height != 2 {
		t.Fatalf("WebP header: %+v", p)
	}
	c := testConfig(t)
	writeImage(t, c, "test.webp", b)
	s := testStore(t, c)
	scanNow(t, s, c, false)
	photo, _ := s.photo(context.Background(), 1)
	cache, err := newCache(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	defer cache.close()
	preview, err := cache.get(context.Background(), photo, 480)
	if err != nil {
		t.Fatal(err)
	}
	defer preview.Close()
	config, _, err := image.DecodeConfig(preview)
	if err != nil || config.Width != 3 || config.Height != 2 {
		t.Fatalf("WebP preview: %+v %v", config, err)
	}
}

func Example_xpDecode() {
	var b bytes.Buffer
	for _, u := range utf16.Encode([]rune("Łódź;Ærø\x00")) {
		_ = binary.Write(&b, binary.LittleEndian, u)
	}
	fmt.Println(xpDecode(b.Bytes())) // Output: Łódź;Ærø
}
