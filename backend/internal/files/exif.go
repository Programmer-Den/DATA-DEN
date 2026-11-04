package files

import (
	"bytes"
	"encoding/binary"
	"io"
	"io/fs"
	"math"
	"sync"
	"time"

	"github.com/Programmer-Den/DATA-DEN/backend/internal/places"
)

// EXIF tags that the gallery uses.
const (
	tagOrientation      = 0x0112
	tagDateTime         = 0x0132
	tagExifIFD          = 0x8769
	tagGPSIFD           = 0x8825
	tagDateTimeOriginal = 0x9003
	tagGPSLatitudeRef   = 1
	tagGPSLatitude      = 2
	tagGPSLongitudeRef  = 3
	tagGPSLongitude     = 4
	// maxIFDEntries guards against a broken directory claiming thousands of
	// entries.
	maxIFDEntries = 1000
)

// exifLayout is how EXIF writes a date and time, in the camera's local time.
const exifLayout = "2006:01:02 15:04:05"

// takenLayout is how the API reports when a photo was taken: the camera's
// local time without a zone, as EXIF has none either.
const takenLayout = "2006-01-02T15:04:05"

// photoMeta is what the EXIF data of a photo says.
type photoMeta struct {
	// Taken is when the photo was taken (takenLayout), "" when unknown.
	Taken string
	// Lat and Lon are where it was taken, nil when unknown.
	Lat, Lon *float64
	// Place is the town nearest to where it was taken, "" when unknown.
	Place string
	// Orientation is how to turn the photo, 1 to 8.
	Orientation int
}

// readExif reads the EXIF data of a JPEG; a photo without any gives the
// zero meta with orientation 1.
func readExif(r io.Reader) photoMeta {
	meta := photoMeta{Orientation: 1}
	t, ok := parseTIFF(exifTIFF(r))
	if !ok {
		return meta
	}
	ifd0 := t.ifd(t.order.Uint32(t.data[4:]))
	if v, ok := ifd0[tagOrientation].short(t); ok && v >= 1 && v <= 8 {
		meta.Orientation = v
	}
	var candidates []string
	if sub, ok := ifd0[tagExifIFD].long(t); ok {
		candidates = append(candidates, t.ifd(sub)[tagDateTimeOriginal].ascii(t))
	}
	// Cameras that leave out the original time, or write zeros, still write
	// when the file was made, which usually is the same.
	candidates = append(candidates, ifd0[tagDateTime].ascii(t))
	for _, taken := range candidates {
		if when, err := time.Parse(exifLayout, taken); err == nil && when.Year() > 1900 {
			meta.Taken = when.Format(takenLayout)
			break
		}
	}
	if gps, ok := ifd0[tagGPSIFD].long(t); ok {
		meta.Lat, meta.Lon = t.position(t.ifd(gps))
	}
	return meta
}

// exifOrientation reads the orientation from a JPEG's EXIF data, 1 (as
// stored) when there is none.
func exifOrientation(r io.Reader) int {
	return readExif(r).Orientation
}

// exifTIFF finds the TIFF data of the EXIF segment of a JPEG, nil when there
// is none.
func exifTIFF(r io.Reader) []byte {
	// EXIF sits in an APP1 segment near the start of the file, at most 64 KB.
	head := make([]byte, 128<<10)
	n, _ := io.ReadFull(r, head)
	head = head[:n]
	if len(head) < 4 || head[0] != 0xFF || head[1] != 0xD8 {
		return nil
	}
	for i := 2; i+4 <= len(head) && head[i] == 0xFF; {
		marker := head[i+1]
		length := int(binary.BigEndian.Uint16(head[i+2:]))
		if marker == 0xDA || length < 2 || i+2+length > len(head) {
			// Image data starts, or the segment is cut off.
			return nil
		}
		segment := head[i+4 : i+2+length]
		if marker == 0xE1 && bytes.HasPrefix(segment, []byte("Exif\x00\x00")) {
			return segment[6:]
		}
		i += 2 + length
	}
	return nil
}

// tiff is the TIFF data inside EXIF, with its byte order.
type tiff struct {
	data  []byte
	order binary.ByteOrder
}

func parseTIFF(data []byte) (tiff, bool) {
	if len(data) < 8 {
		return tiff{}, false
	}
	switch string(data[:2]) {
	case "II":
		return tiff{data, binary.LittleEndian}, true
	case "MM":
		return tiff{data, binary.BigEndian}, true
	}
	return tiff{}, false
}

// tiffEntry is one tag of a directory.
type tiffEntry struct {
	typ   uint16
	count uint32
	// value is where the value is: inside the entry or at an offset.
	value []byte
}

// typeSizes are the bytes per value of the TIFF types the gallery reads:
// BYTE, ASCII, SHORT, LONG and RATIONAL.
var typeSizes = map[uint16]int{1: 1, 2: 1, 3: 2, 4: 4, 5: 8}

// ifd reads the directory at offset; broken entries are left out.
func (t tiff) ifd(offset uint32) map[uint16]tiffEntry {
	entries := map[uint16]tiffEntry{}
	at := int(offset)
	if at < 8 || at+2 > len(t.data) {
		return entries
	}
	count := min(int(t.order.Uint16(t.data[at:])), maxIFDEntries)
	for i := range count {
		e := at + 2 + i*12
		if e+12 > len(t.data) {
			break
		}
		typ := t.order.Uint16(t.data[e+2:])
		n := t.order.Uint32(t.data[e+4:])
		size, ok := typeSizes[typ]
		if !ok || n == 0 || int(n) > len(t.data) {
			continue
		}
		total := size * int(n)
		value := t.data[e+8 : e+12]
		if total > 4 {
			start := int(t.order.Uint32(t.data[e+8:]))
			if start < 0 || start+total > len(t.data) {
				continue
			}
			value = t.data[start : start+total]
		}
		entries[t.order.Uint16(t.data[e:])] = tiffEntry{typ: typ, count: n, value: value[:min(total, len(value))]}
	}
	return entries
}

func (e tiffEntry) short(t tiff) (int, bool) {
	if e.typ != 3 || len(e.value) < 2 {
		return 0, false
	}
	return int(t.order.Uint16(e.value)), true
}

func (e tiffEntry) long(t tiff) (uint32, bool) {
	switch {
	case e.typ == 4 && len(e.value) >= 4:
		return t.order.Uint32(e.value), true
	case e.typ == 3 && len(e.value) >= 2:
		return uint32(t.order.Uint16(e.value)), true
	}
	return 0, false
}

func (e tiffEntry) ascii(_ tiff) string {
	if e.typ != 2 {
		return ""
	}
	s, _, _ := bytes.Cut(e.value, []byte{0})
	return string(s)
}

// degrees reads a position as three rationals: degrees, minutes, seconds.
func (e tiffEntry) degrees(t tiff) (float64, bool) {
	if e.typ != 5 || len(e.value) < 24 {
		return 0, false
	}
	sum := 0.0
	for i, unit := range []float64{1, 60, 3600} {
		num := float64(t.order.Uint32(e.value[i*8:]))
		den := float64(t.order.Uint32(e.value[i*8+4:]))
		if den == 0 {
			if num != 0 {
				return 0, false
			}
			continue
		}
		sum += num / den / unit
	}
	return sum, true
}

// position reads latitude and longitude from the GPS directory, nil for
// both when either is missing or impossible.
func (t tiff) position(gps map[uint16]tiffEntry) (lat, lon *float64) {
	la, ok1 := gps[tagGPSLatitude].degrees(t)
	lo, ok2 := gps[tagGPSLongitude].degrees(t)
	if !ok1 || !ok2 || la > 90 || lo > 180 {
		return nil, nil
	}
	if gps[tagGPSLatitudeRef].ascii(t) == "S" {
		la = -la
	}
	if gps[tagGPSLongitudeRef].ascii(t) == "W" {
		lo = -lo
	}
	if la == 0 && lo == 0 {
		// Phones without a fix write 0, 0, in the sea off Africa.
		return nil, nil
	}
	// Rounded to about a metre; more only looks precise.
	la = math.Round(la*1e5) / 1e5
	lo = math.Round(lo*1e5) / 1e5
	return &la, &lo
}

// photoCache keeps the EXIF data of each photo, so the timeline reads a photo
// only when it is new or changed.
type photoCache struct {
	mu     sync.Mutex
	byName map[string]cachedMeta
}

type cachedMeta struct {
	size     int64
	modified time.Time
	meta     photoMeta
}

// exifFormats are the photos whose EXIF data is read.
var exifFormats = map[string]bool{".jpg": true, ".jpeg": true}

// photoMeta reads the EXIF data of the photo name, from the cache when the
// photo did not change.
func (f *Files) photoMeta(name string, info fs.FileInfo) photoMeta {
	f.photos.mu.Lock()
	c, ok := f.photos.byName[name]
	f.photos.mu.Unlock()
	if ok && c.size == info.Size() && c.modified.Equal(info.ModTime()) {
		return c.meta
	}
	meta := photoMeta{Orientation: 1}
	if exifFormats[lowerExt(name)] {
		if file, err := f.root.Open(name); err == nil {
			meta = readExif(file)
			_ = file.Close()
		}
		if meta.Lat != nil {
			meta.Place = places.Nearest(*meta.Lat, *meta.Lon)
		}
	}
	f.photos.mu.Lock()
	if f.photos.byName == nil {
		f.photos.byName = map[string]cachedMeta{}
	}
	f.photos.byName[name] = cachedMeta{size: info.Size(), modified: info.ModTime(), meta: meta}
	f.photos.mu.Unlock()
	return meta
}

// prunePhotos forgets the photos that are gone, keeping those in seen.
func (f *Files) prunePhotos(seen map[string]bool) {
	f.photos.mu.Lock()
	defer f.photos.mu.Unlock()
	for name := range f.photos.byName {
		if !seen[name] {
			delete(f.photos.byName, name)
		}
	}
}
