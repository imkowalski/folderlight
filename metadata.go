package main

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/bep/imagemeta"
	_ "golang.org/x/image/webp"
)

func supported(name string) bool {
	return mediaKind(name) != ""
}
func cleanTags(in []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, t := range in {
		t = strings.TrimSpace(strings.Trim(t, "\x00"))
		if t == "" {
			continue
		}
		key := fold(t)
		if !seen[key] {
			seen[key] = true
			out = append(out, t)
		}
	}
	return out
}
func xpDecode(data []byte) string {
	units := make([]uint16, 0, len(data)/2)
	for len(data) >= 2 {
		v := binary.LittleEndian.Uint16(data)
		if v == 0 {
			break
		}
		units = append(units, v)
		data = data[2:]
	}
	return string(utf16.Decode(units))
}

// Read XPKeywords before imagemeta's printable-string conversion can lose UTF-16 bytes.
func xpTIFF(b []byte) string {
	if len(b) < 8 {
		return ""
	}
	var order binary.ByteOrder
	switch string(b[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return ""
	}
	if order.Uint16(b[2:4]) != 42 {
		return ""
	}
	offset := uint64(order.Uint32(b[4:8]))
	if offset+2 > uint64(len(b)) {
		return ""
	}
	count := uint64(order.Uint16(b[offset : offset+2]))
	offset += 2
	for i := uint64(0); i < count; i++ {
		p := offset + i*12
		if p+12 > uint64(len(b)) {
			return ""
		}
		entry := b[p : p+12]
		if order.Uint16(entry[:2]) != 0x9c9e || order.Uint16(entry[2:4]) != 1 {
			continue
		}
		n := uint64(order.Uint32(entry[4:8]))
		start := p + 8
		if n > 4 {
			start = uint64(order.Uint32(entry[8:12]))
		}
		if start+n > uint64(len(b)) {
			return ""
		}
		return xpDecode(b[start : start+n])
	}
	return ""
}

func containerEXIF(r io.ReadSeeker, format string) ([]byte, error) {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	read := func(n int64) ([]byte, error) {
		if n < 0 || n > 2<<20 {
			return nil, errors.New("EXIF packet exceeds 2 MiB")
		}
		b := make([]byte, n)
		_, err := io.ReadFull(r, b)
		return b, err
	}
	switch format {
	case "jpeg":
		if _, err := r.Seek(2, io.SeekStart); err != nil {
			return nil, err
		}
		for i := 0; i < 1024; i++ {
			var marker [2]byte
			if _, err := io.ReadFull(r, marker[:]); err != nil {
				return nil, err
			}
			if marker[0] != 0xff {
				return nil, errors.New("invalid JPEG marker")
			}
			for marker[1] == 0xff {
				if _, err := io.ReadFull(r, marker[1:]); err != nil {
					return nil, err
				}
			}
			if marker[1] == 0xda || marker[1] == 0xd9 {
				return nil, nil
			}
			if marker[1] == 0x01 || (marker[1] >= 0xd0 && marker[1] <= 0xd7) {
				continue
			}
			var size [2]byte
			if _, err := io.ReadFull(r, size[:]); err != nil {
				return nil, err
			}
			n := int64(binary.BigEndian.Uint16(size[:])) - 2
			if n < 0 {
				return nil, errors.New("invalid JPEG segment")
			}
			if marker[1] == 0xe1 {
				b, err := read(n)
				if err != nil {
					return nil, err
				}
				if len(b) >= 6 && string(b[:6]) == "Exif\x00\x00" {
					return b[6:], nil
				}
			} else {
				if _, err := r.Seek(n, io.SeekCurrent); err != nil {
					return nil, err
				}
			}
		}
	case "png", "webp":
		start := int64(8)
		if format == "webp" {
			start = 12
		}
		if _, err := r.Seek(start, io.SeekStart); err != nil {
			return nil, err
		}
		for i := 0; i < 1024; i++ {
			var h [8]byte
			if _, err := io.ReadFull(r, h[:]); err != nil {
				return nil, err
			}
			var n uint32
			var name string
			extra := int64(4)
			if format == "png" {
				n = binary.BigEndian.Uint32(h[:4])
				name = string(h[4:])
			} else {
				n = binary.LittleEndian.Uint32(h[4:])
				name = string(h[:4])
				extra = int64(n % 2)
			}
			if name == "eXIf" || name == "EXIF" {
				b, err := read(int64(n))
				if err != nil {
					return nil, err
				}
				if len(b) >= 6 && string(b[:6]) == "Exif\x00\x00" {
					b = b[6:]
				}
				return b, nil
			}
			if name == "IEND" {
				return nil, nil
			}
			if _, err := r.Seek(int64(n)+extra, io.SeekCurrent); err != nil {
				return nil, err
			}
		}
	}
	return nil, nil
}

func readMetadata(r io.ReadSeeker, p *Photo) {
	config, format, err := image.DecodeConfig(r)
	if err != nil {
		p.Error = "image header: " + err.Error()
		return
	}
	p.Width = config.Width
	p.Height = config.Height
	p.Orientation = 1
	exif, xerr := containerEXIF(r, format)
	keywords := xpTIFF(exif)
	if keywords != "" {
		p.Tags = append(p.Tags, strings.Split(keywords, ";")...)
	}
	var f imagemeta.ImageFormat
	switch format {
	case "jpeg":
		f = imagemeta.JPEG
	case "png":
		f = imagemeta.PNG
	case "webp":
		f = imagemeta.WebP
	default:
		p.Tags = cleanTags(p.Tags)
		return
	}
	if _, err = r.Seek(0, io.SeekStart); err != nil {
		p.Error = err.Error()
		return
	}
	var dates imagemeta.Tags
	opts := imagemeta.Options{R: r, ImageFormat: f, Sources: imagemeta.IPTC | imagemeta.XMP, LimitTagSize: 60 << 10, LimitNumTags: 1000,
		ShouldHandleTag: func(t imagemeta.TagInfo) bool {
			return t.Tag == "CodedCharacterSet" || t.Tag == "Orientation" || t.Tag == "Keywords" || t.Tag == "Subject" || t.Tag == "DateTimeOriginal" || t.Tag == "DateTime" || t.Tag == "CreateDate" || t.Tag == "DateCreated"
		},
		HandleTag: func(t imagemeta.TagInfo) error {
			dates.Add(t)
			switch t.Tag {
			case "Orientation":
				v, _ := strconv.Atoi(fmt.Sprint(t.Value))
				if v >= 1 && v <= 8 {
					p.Orientation = v
				}
			case "Keywords", "Subject":
				switch v := t.Value.(type) {
				case string:
					p.Tags = append(p.Tags, v)
				case []string:
					p.Tags = append(p.Tags, v...)
				case []any:
					for _, x := range v {
						p.Tags = append(p.Tags, fmt.Sprint(x))
					}
				}
			}
			return nil
		},
		HandleXMP: func(r io.Reader) error { return readXMP(r, &p.Tags, &dates) },
	}
	// JPEG uses APP1 for both EXIF and XMP. Extract the actual EXIF packet
	// separately so XMP-first and XMP-only images work consistently.
	if len(exif) > 0 {
		exifOpts := opts
		exifOpts.R = bytes.NewReader(exif)
		exifOpts.ImageFormat = imagemeta.TIFF
		exifOpts.Sources = imagemeta.EXIF | imagemeta.IPTC | imagemeta.XMP
		_, exifErr := imagemeta.Decode(exifOpts)
		if exifErr != nil {
			xerr = exifErr
		}
	}
	if format == "png" {
		err = readPNGMetadata(r, opts, &p.Tags, &dates)
	} else {
		_, err = imagemeta.Decode(opts)
	}
	if d, e := dates.GetDateTime(); e == nil && !d.IsZero() {
		p.Taken = d.Unix()
	}
	p.Tags = cleanTags(p.Tags)
	if err != nil {
		p.Error = "metadata: " + err.Error()
	} else if xerr != nil && xerr != io.EOF {
		p.Error = "Windows metadata: " + xerr.Error()
	}
}

func limitedInflate(b []byte) ([]byte, error) {
	z, err := zlib.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer z.Close()
	out, err := io.ReadAll(io.LimitReader(z, (2<<20)+1))
	if len(out) > 2<<20 {
		return nil, errors.New("compressed metadata exceeds 2 MiB")
	}
	return out, err
}

// Handle PNG's standard XMP iTXt form, including compressed packets. Decode
// compressed profiles with a size cap before handing IPTC/EXIF to imagemeta.
func readPNGMetadata(r io.ReadSeeker, opts imagemeta.Options, tags *[]string, dates *imagemeta.Tags) error {
	if _, err := r.Seek(8, io.SeekStart); err != nil {
		return err
	}
	for chunks := 0; chunks < 100000; chunks++ {
		var header [8]byte
		if _, err := io.ReadFull(r, header[:]); err != nil {
			return err
		}
		n := int64(binary.BigEndian.Uint32(header[:4]))
		kind := string(header[4:])
		if kind == "IEND" {
			return nil
		}
		if kind != "iTXt" && kind != "tEXt" && kind != "zTXt" {
			if _, err := r.Seek(n+4, io.SeekCurrent); err != nil {
				return err
			}
			continue
		}
		if n > 2<<20 {
			return errors.New("PNG metadata chunk exceeds 2 MiB")
		}
		b := make([]byte, n)
		if _, err := io.ReadFull(r, b); err != nil {
			return err
		}
		if _, err := r.Seek(4, io.SeekCurrent); err != nil {
			return err
		}
		key, data, ok := bytes.Cut(b, []byte{0})
		if !ok {
			continue
		}
		name := string(key)
		var err error
		if kind == "zTXt" {
			if len(data) < 1 || data[0] != 0 {
				return errors.New("invalid PNG text compression")
			}
			data, err = limitedInflate(data[1:])
			if err != nil {
				return err
			}
		}
		if kind == "iTXt" {
			if len(data) < 2 {
				return errors.New("invalid PNG international text")
			}
			compressed, method := data[0], data[1]
			data = data[2:]
			_, data, ok = bytes.Cut(data, []byte{0})
			if !ok {
				return errors.New("invalid PNG text language")
			}
			_, data, ok = bytes.Cut(data, []byte{0})
			if !ok {
				return errors.New("invalid PNG translated keyword")
			}
			if compressed == 1 {
				if method != 0 {
					return errors.New("invalid PNG compression method")
				}
				data, err = limitedInflate(data)
				if err != nil {
					return err
				}
			} else if compressed != 0 {
				return errors.New("invalid PNG compression flag")
			}
		}
		switch name {
		case "XML:com.adobe.xmp":
			if err := readXMP(bytes.NewReader(data), tags, dates); err != nil {
				return err
			}
		case "Keywords":
			*tags = append(*tags, strings.Split(string(data), ";")...)
		case "Raw profile type exif", "Raw profile type iptc":
			fields := strings.Fields(string(data))
			if len(fields) < 3 {
				return errors.New("invalid PNG raw metadata profile")
			}
			raw, err := hex.DecodeString(strings.Join(fields[2:], ""))
			if err != nil {
				return err
			}
			if name == "Raw profile type exif" {
				if bytes.HasPrefix(raw, []byte("Exif\x00\x00")) {
					raw = raw[6:]
				}
				if keyword := xpTIFF(raw); keyword != "" {
					*tags = append(*tags, strings.Split(keyword, ";")...)
				}
				copyOpts := opts
				copyOpts.R = bytes.NewReader(raw)
				copyOpts.ImageFormat = imagemeta.TIFF
				copyOpts.Sources = imagemeta.EXIF | imagemeta.IPTC | imagemeta.XMP
				if _, err = imagemeta.Decode(copyOpts); err != nil {
					return err
				}
			} else {
				if !bytes.HasPrefix(raw, []byte("8BIM")) {
					var block bytes.Buffer
					block.WriteString("8BIM")
					_ = binary.Write(&block, binary.BigEndian, uint16(0x0404))
					block.Write([]byte{0, 0})
					_ = binary.Write(&block, binary.BigEndian, uint32(len(raw)))
					block.Write(raw)
					if len(raw)%2 != 0 {
						block.WriteByte(0)
					}
					raw = block.Bytes()
				}
				payload := append([]byte("Photoshop 3.0\x00"), raw...)
				if len(payload) > 65533 {
					return errors.New("PNG IPTC profile exceeds 64 KiB")
				}
				var jpeg bytes.Buffer
				jpeg.Write([]byte{0xff, 0xd8, 0xff, 0xed})
				_ = binary.Write(&jpeg, binary.BigEndian, uint16(len(payload)+2))
				jpeg.Write(payload)
				jpeg.Write([]byte{0xff, 0xda})
				copyOpts := opts
				copyOpts.R = bytes.NewReader(jpeg.Bytes())
				copyOpts.ImageFormat = imagemeta.JPEG
				copyOpts.Sources = imagemeta.IPTC
				if _, err = imagemeta.Decode(copyOpts); err != nil {
					return err
				}
			}
		}
	}
	return errors.New("too many PNG chunks")
}

func readXMP(r io.Reader, tags *[]string, dates *imagemeta.Tags) error {
	b, err := io.ReadAll(io.LimitReader(r, (2<<20)+1))
	if err != nil {
		return err
	}
	if len(b) > 2<<20 {
		return errors.New("XMP packet exceeds 2 MiB")
	}
	d := xml.NewDecoder(strings.NewReader(string(b)))
	depth := 0
	subjectDepth := -1
	var text strings.Builder
	inItem := false
	for {
		token, err := d.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		switch t := token.(type) {
		case xml.StartElement:
			depth++
			if t.Name.Space == "http://purl.org/dc/elements/1.1/" && t.Name.Local == "subject" {
				subjectDepth = depth
			}
			if subjectDepth >= 0 && t.Name.Local == "li" {
				inItem = true
				text.Reset()
			}
			for _, a := range t.Attr {
				switch a.Name.Local {
				case "subject":
					if a.Name.Space == "http://purl.org/dc/elements/1.1/" {
						*tags = append(*tags, a.Value)
					}
				case "DateTimeOriginal", "CreateDate", "DateCreated":
					dates.Add(imagemeta.TagInfo{Source: imagemeta.XMP, Tag: a.Name.Local, Value: a.Value})
				}
			}
		case xml.CharData:
			if inItem {
				text.Write([]byte(t))
			}
		case xml.EndElement:
			if inItem && t.Name.Local == "li" {
				*tags = append(*tags, text.String())
				inItem = false
			}
			if depth == subjectDepth {
				subjectDepth = -1
			}
			depth--
		}
	}
}
