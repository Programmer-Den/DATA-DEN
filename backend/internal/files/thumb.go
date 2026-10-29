package files

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"strconv"
	"time"

	// Registers GIF for image.Decode.
	_ "image/gif"
	// Registers PNG for image.Decode.
	_ "image/png"

	"golang.org/x/image/draw"

	// Registers BMP for image.Decode.
	_ "golang.org/x/image/bmp"
	// Registers WebP for image.Decode.
	_ "golang.org/x/image/webp"
)

const (
	// thumbDir keeps the thumbnails, hidden like uploadDir.
	thumbDir = ".data-den-thumbs"
	// ThumbSize is the longer side of a thumbnail in pixels.
	ThumbSize = 320
	// maxPixels is the largest photo made into a thumbnail; decoding takes
	// 4 bytes per pixel, so 100 megapixels need 400 MB.
	maxPixels = 100_000_000
	// thumbWorkers is how many photos are decoded at the same time, so a
	// gallery full of large photos cannot use up the VM's memory.
	thumbWorkers = 2
)

// thumbFormats are the photos a thumbnail can be made of. Others, such as
// HEIC or SVG, answer 415 and the gallery shows the photo itself.
var thumbFormats = map[string]bool{".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true, ".bmp": true}

// thumb serves a small JPEG of a photo for the gallery. It is made on the
// first request and kept in thumbDir, named after the photo's path, size and
// modification time, so a changed photo gets a new one.
func (f *Files) thumb(w http.ResponseWriter, r *http.Request) {
	name, ok := relative(w, r.URL.Query().Get("path"))
	if !ok {
		return
	}
	if hidden(name) {
		writeError(w, http.StatusNotFound, "Nicht gefunden")
		return
	}
	if !thumbFormats[lowerExt(name)] {
		writeError(w, http.StatusUnsupportedMediaType, "Kein Vorschaubild möglich")
		return
	}
	info, err := f.root.Stat(name)
	if err != nil {
		openError(w, err)
		return
	}
	if !info.Mode().IsRegular() {
		writeError(w, http.StatusBadRequest, "Keine Datei")
		return
	}

	sum := sha256.Sum256([]byte(name + "\x00" + strconv.FormatInt(info.Size(), 10) + "\x00" + info.ModTime().UTC().Format(time.RFC3339Nano)))
	key := hex.EncodeToString(sum[:16])
	cached := path.Join(thumbDir, key[:2], key+".jpg")

	data, err := f.root.ReadFile(cached)
	if errors.Is(err, fs.ErrNotExist) {
		data, err = f.makeThumb(name, cached)
	}
	if errors.Is(err, errNoThumb) {
		writeError(w, http.StatusUnsupportedMediaType, "Kein Vorschaubild möglich")
		return
	}
	if err != nil {
		slog.Error("making a thumbnail", "path", name, "err", err)
		writeError(w, http.StatusInternalServerError, "Vorschaubild nicht möglich")
		return
	}

	h := w.Header()
	h.Set("Content-Type", "image/jpeg")
	h.Set("Cache-Control", "private, max-age=86400")
	h.Set("ETag", `"`+key+`"`)
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(data))
}

// errNoThumb means the photo cannot be read or is too large.
var errNoThumb = errors.New("no thumbnail")

func (f *Files) makeThumb(name, cached string) ([]byte, error) {
	f.thumbSlots <- struct{}{}
	defer func() { <-f.thumbSlots }()
	// Another request may have made it while this one waited.
	if data, err := f.root.ReadFile(cached); err == nil {
		return data, nil
	}

	file, err := f.root.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	config, _, err := image.DecodeConfig(file)
	if err != nil || config.Width*config.Height > maxPixels {
		return nil, errNoThumb
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	src, _, err := image.Decode(file)
	if err != nil {
		return nil, errNoThumb
	}
	orientation := 1
	if lowerExt(name) == ".jpg" || lowerExt(name) == ".jpeg" {
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return nil, err
		}
		orientation = exifOrientation(file)
	}

	var out bytes.Buffer
	if err := jpeg.Encode(&out, orient(scale(src, ThumbSize), orientation), &jpeg.Options{Quality: 80}); err != nil {
		return nil, err
	}
	data := out.Bytes()

	// Written under a temporary name and renamed, so a request never reads
	// half a thumbnail. If it cannot be kept, it is still served.
	if err := f.keepThumb(cached, data); err != nil {
		slog.Warn("keeping a thumbnail", "path", name, "err", err)
	}
	return data, nil
}

func (f *Files) keepThumb(cached string, data []byte) error {
	if err := f.root.MkdirAll(path.Dir(cached), 0o750); err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.%d.tmp", cached, time.Now().UnixNano())
	if err := f.root.WriteFile(tmp, data, 0o640); err != nil {
		return err
	}
	if err := f.root.Rename(tmp, cached); err != nil {
		_ = f.root.Remove(tmp)
		return err
	}
	return nil
}

// scale shrinks src so its longer side is at most size; smaller images stay.
func scale(src image.Image, size int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= size && h <= size {
		return src
	}
	if w >= h {
		h = max(1, h*size/w)
		w = size
	} else {
		w = max(1, w*size/h)
		h = size
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.ApproxBiLinear.Scale(dst, dst.Bounds(), src, b, draw.Src, nil)
	return dst
}

// orient turns an image the way its EXIF orientation (1 to 8) says, as a
// browser does when it shows the photo itself.
func orient(src image.Image, orientation int) image.Image {
	if orientation < 2 || orientation > 8 {
		return src
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	// Orientations 5 to 8 swap width and height.
	dw, dh := w, h
	if orientation >= 5 {
		dw, dh = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := range h {
		for x := range w {
			var dx, dy int
			switch orientation {
			case 2: // mirrored
				dx, dy = w-1-x, y
			case 3: // turned 180°
				dx, dy = w-1-x, h-1-y
			case 4: // mirrored upside down
				dx, dy = x, h-1-y
			case 5: // mirrored and turned 90° left
				dx, dy = y, x
			case 6: // turned 90° right
				dx, dy = h-1-y, x
			case 7: // mirrored and turned 90° right
				dx, dy = h-1-y, w-1-x
			case 8: // turned 90° left
				dx, dy = y, w-1-x
			}
			dst.Set(dx, dy, src.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst
}

// exifOrientation reads the orientation from a JPEG's EXIF data, 1 (as
// stored) when there is none.
func exifOrientation(r io.Reader) int {
	// EXIF sits in an APP1 segment near the start of the file.
	head := make([]byte, 128<<10)
	n, _ := io.ReadFull(r, head)
	head = head[:n]
	if len(head) < 4 || head[0] != 0xFF || head[1] != 0xD8 {
		return 1
	}
	for i := 2; i+4 <= len(head) && head[i] == 0xFF; {
		marker := head[i+1]
		length := int(binary.BigEndian.Uint16(head[i+2:]))
		if marker == 0xDA || length < 2 || i+2+length > len(head) {
			// Image data starts, or the segment is cut off.
			return 1
		}
		segment := head[i+4 : i+2+length]
		if marker == 0xE1 && bytes.HasPrefix(segment, []byte("Exif\x00\x00")) {
			return tiffOrientation(segment[6:])
		}
		i += 2 + length
	}
	return 1
}

// tiffOrientation finds tag 0x0112 in the first directory of TIFF data.
func tiffOrientation(tiff []byte) int {
	if len(tiff) < 8 {
		return 1
	}
	var order binary.ByteOrder
	switch string(tiff[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return 1
	}
	dir := int(order.Uint32(tiff[4:]))
	if dir < 8 || dir+2 > len(tiff) {
		return 1
	}
	count := int(order.Uint16(tiff[dir:]))
	for i := range count {
		entry := dir + 2 + i*12
		if entry+12 > len(tiff) {
			return 1
		}
		if order.Uint16(tiff[entry:]) == 0x0112 {
			if v := int(order.Uint16(tiff[entry+8:])); v >= 1 && v <= 8 {
				return v
			}
			return 1
		}
	}
	return 1
}
