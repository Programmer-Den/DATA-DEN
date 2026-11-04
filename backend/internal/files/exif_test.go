package files

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"image"
	"image/jpeg"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"
)

// tiffBuilder writes TIFF data with directories, for EXIF in tests.
type tiffBuilder struct {
	order binary.ByteOrder
	buf   []byte
}

type tiffTag struct {
	tag, typ uint16
	count    uint32
	value    []byte
}

func newTIFF(order binary.ByteOrder) *tiffBuilder {
	b := &tiffBuilder{order: order, buf: make([]byte, 8)}
	if order == binary.LittleEndian {
		copy(b.buf, "II")
	} else {
		copy(b.buf, "MM")
	}
	order.PutUint16(b.buf[2:], 42)
	return b
}

// dir appends a directory and returns its offset. A value longer than four
// bytes goes after it.
func (b *tiffBuilder) dir(tags ...tiffTag) uint32 {
	at := len(b.buf)
	extra := at + 2 + len(tags)*12 + 4
	entries := make([]byte, 2+len(tags)*12+4)
	b.order.PutUint16(entries, uint16(len(tags))) //nolint:gosec // a few tags
	var data []byte
	for i, tag := range tags {
		e := entries[2+i*12:]
		b.order.PutUint16(e, tag.tag)
		b.order.PutUint16(e[2:], tag.typ)
		b.order.PutUint32(e[4:], tag.count)
		if len(tag.value) <= 4 {
			copy(e[8:], tag.value)
		} else {
			b.order.PutUint32(e[8:], uint32(extra+len(data))) //nolint:gosec // small test data
			data = append(data, tag.value...)
		}
	}
	b.buf = append(append(b.buf, entries...), data...)
	return uint32(at) //nolint:gosec // small test data
}

func (b *tiffBuilder) long(v uint32) []byte {
	out := make([]byte, 4)
	b.order.PutUint32(out, v)
	return out
}

func (b *tiffBuilder) short(v uint16) []byte {
	out := make([]byte, 2)
	b.order.PutUint16(out, v)
	return out
}

func (b *tiffBuilder) rationals(v ...uint32) []byte {
	var out []byte
	for _, x := range v {
		out = append(out, b.long(x)...)
	}
	return out
}

func ascii(s string) tiffTag {
	return tiffTag{typ: 2, count: uint32(len(s) + 1), value: append([]byte(s), 0)} //nolint:gosec // short strings
}

func withTag(t tiffTag, tag uint16) tiffTag {
	t.tag = tag
	return t
}

// photoTIFF is EXIF for a photo taken at taken in Berlin (52°31'12" N,
// 13°24'18" E), or without a position when gps is false.
func photoTIFF(order binary.ByteOrder, taken string, gps bool) []byte {
	b := newTIFF(order)
	sub := b.dir(withTag(ascii(taken), tagDateTimeOriginal))
	tags := []tiffTag{
		{tag: tagOrientation, typ: 3, count: 1, value: b.short(1)},
		withTag(ascii("2000:01:01 00:00:00"), tagDateTime),
		{tag: tagExifIFD, typ: 4, count: 1, value: b.long(sub)},
	}
	if gps {
		g := b.dir(
			withTag(ascii("N"), tagGPSLatitudeRef),
			tiffTag{tag: tagGPSLatitude, typ: 5, count: 3, value: b.rationals(52, 1, 31, 1, 1200, 100)},
			withTag(ascii("E"), tagGPSLongitudeRef),
			tiffTag{tag: tagGPSLongitude, typ: 5, count: 3, value: b.rationals(13, 1, 24, 1, 18, 1)},
		)
		tags = append(tags, tiffTag{tag: tagGPSIFD, typ: 4, count: 1, value: b.long(g)})
	}
	ifd0 := b.dir(tags...)
	b.order.PutUint32(b.buf[4:], ifd0)
	return b.buf
}

// jpegWithExif puts tiff as EXIF into a small JPEG.
func jpegWithExif(t *testing.T, tiff []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewGray(image.Rect(0, 0, 4, 4)), nil); err != nil {
		t.Fatal(err)
	}
	app1 := []byte{0xFF, 0xE1, 0, 0}
	binary.BigEndian.PutUint16(app1[2:], uint16(2+6+len(tiff))) //nolint:gosec // small test data
	app1 = append(append(app1, "Exif\x00\x00"...), tiff...)
	jpg := buf.Bytes()
	return append(append(append([]byte{}, jpg[:2]...), app1...), jpg[2:]...)
}

func TestReadExif(t *testing.T) {
	for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		meta := readExif(bytes.NewReader(jpegWithExif(t, photoTIFF(order, "2026:07:14 15:32:10", true))))
		if meta.Taken != "2026-07-14T15:32:10" {
			t.Errorf("%v: taken %q", order, meta.Taken)
		}
		if meta.Lat == nil || meta.Lon == nil || *meta.Lat != 52.52 || *meta.Lon != 13.405 {
			t.Errorf("%v: position %v, %v", order, meta.Lat, meta.Lon)
		}
	}
}

func TestReadExifFallbacks(t *testing.T) {
	// No original time: the file's time.
	b := newTIFF(binary.BigEndian)
	ifd0 := b.dir(withTag(ascii("2019:12:24 18:00:00"), tagDateTime))
	b.order.PutUint32(b.buf[4:], ifd0)
	if meta := readExif(bytes.NewReader(jpegWithExif(t, b.buf))); meta.Taken != "2019-12-24T18:00:00" || meta.Lat != nil {
		t.Errorf("only DateTime: %+v", meta)
	}
	// A camera without a clock writes zeros.
	if meta := readExif(bytes.NewReader(jpegWithExif(t, photoTIFF(binary.BigEndian, "0000:00:00 00:00:00", false)))); meta.Taken != "2000-01-01T00:00:00" {
		t.Errorf("zero original time: taken %q, want DateTime", meta.Taken)
	}
	// Broken data gives nothing, and no panic.
	tiff := photoTIFF(binary.LittleEndian, "2026:07:14 15:32:10", true)
	for cut := range len(tiff) {
		readExif(bytes.NewReader(jpegWithExif(t, tiff[:cut])))
	}
	if meta := readExif(bytes.NewReader([]byte("no jpeg"))); meta.Taken != "" || meta.Orientation != 1 {
		t.Errorf("not a JPEG: %+v", meta)
	}
}

func TestMediaTimeline(t *testing.T) {
	data := t.TempDir()
	write := func(name string, content []byte, modified time.Time) {
		full := filepath.Join(data, name)
		if err := os.WriteFile(full, content, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(full, modified, modified); err != nil {
			t.Fatal(err)
		}
	}
	// Copied to the NAS recently, but taken long ago.
	recent := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	write("alt.jpg", jpegWithExif(t, photoTIFF(binary.BigEndian, "2015:08:01 10:00:00", true)), recent)
	write("mitte.jpg", jpegWithExif(t, photoTIFF(binary.LittleEndian, "2020:05:05 12:00:00", false)), recent)
	writePNG(t, filepath.Join(data, "ohne.png"), 2, 2)
	if err := os.Chtimes(filepath.Join(data, "ohne.png"), time.Date(2018, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2018, 1, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	f, err := Open(data)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	f.places = towns{"52.52,13.405": "Berlin"}
	h := http.NewServeMux()
	f.Register(h, func(next http.HandlerFunc) http.HandlerFunc { return next })

	rec := media(h, "photo")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
	var media Media
	if err := json.Unmarshal(rec.Body.Bytes(), &media); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, item := range media.Items {
		got = append(got, item.Path)
	}
	if want := []string{"/mitte.jpg", "/ohne.png", "/alt.jpg"}; !slices.Equal(got, want) {
		t.Fatalf("order %v, want %v", got, want)
	}
	if old := media.Items[2]; old.Taken != "2015-08-01T10:00:00" || old.Lat == nil || *old.Lat != 52.52 || old.Place != "Berlin" {
		t.Errorf("alt.jpg: %+v", old)
	}
	if png := media.Items[1]; png.Taken != "" || png.Lat != nil || png.Place != "" {
		t.Errorf("ohne.png: %+v", png)
	}
}

// towns names positions from a map by "lat,lon".
type towns map[string]string

func (t towns) Name(lat, lon float64) string {
	return t[strconv.FormatFloat(lat, 'f', -1, 64)+","+strconv.FormatFloat(lon, 'f', -1, 64)]
}
