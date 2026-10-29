package files

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func writePNG(t *testing.T, name string, w, h int) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 100, 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func decodeJPEG(t *testing.T, data []byte) image.Image {
	t.Helper()
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("not a JPEG: %v", err)
	}
	return img
}

func TestThumb(t *testing.T) {
	data := t.TempDir()
	writePNG(t, filepath.Join(data, "weit.png"), 1000, 500)
	writePNG(t, filepath.Join(data, "klein.png"), 40, 30)
	h := handler(t, data)

	rec := get(h, "/api/thumb", "/weit.png", nil)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/jpeg" {
		t.Fatalf("status %d, Content-Type %q, body %s", rec.Code, rec.Header().Get("Content-Type"), rec.Body)
	}
	if b := decodeJPEG(t, rec.Body.Bytes()).Bounds(); b.Dx() != ThumbSize || b.Dy() != ThumbSize/2 {
		t.Errorf("size %v, want %dx%d", b.Size(), ThumbSize, ThumbSize/2)
	}
	first := rec.Body.Bytes()

	// Kept in the hidden folder and served from there the next time.
	kept, err := filepath.Glob(filepath.Join(data, thumbDir, "*", "*.jpg"))
	if err != nil || len(kept) != 1 {
		t.Fatalf("thumbnails kept: %v, %v", kept, err)
	}
	again := get(h, "/api/thumb", "/weit.png", nil)
	if !bytes.Equal(again.Body.Bytes(), first) {
		t.Error("the second request got another thumbnail")
	}
	etag := again.Header().Get("ETag")
	if rec := get(h, "/api/thumb", "/weit.png", http.Header{"If-None-Match": {etag}}); rec.Code != http.StatusNotModified {
		t.Errorf("with ETag %s: status %d, want 304", etag, rec.Code)
	}

	// A small photo keeps its size.
	small := get(h, "/api/thumb", "/klein.png", nil)
	if b := decodeJPEG(t, small.Body.Bytes()).Bounds(); b.Dx() != 40 || b.Dy() != 30 {
		t.Errorf("small photo: size %v", b.Size())
	}

	// The thumbnails stay out of the listing, the media and downloads.
	for _, e := range listing(t, get(h, "/api/files", "/", nil)).Entries {
		if e.Name == thumbDir {
			t.Error("the thumbnail folder is listed")
		}
	}
	if rec := get(h, "/api/download", "/"+thumbDir+"/x.jpg", nil); rec.Code != http.StatusNotFound {
		t.Errorf("download from the thumbnail folder: status %d, want 404", rec.Code)
	}
}

func TestThumbNewAfterChange(t *testing.T) {
	data := t.TempDir()
	name := filepath.Join(data, "foto.png")
	writePNG(t, name, 600, 600)
	h := handler(t, data)
	first := get(h, "/api/thumb", "/foto.png", nil).Header().Get("ETag")
	writePNG(t, name, 600, 300)
	second := get(h, "/api/thumb", "/foto.png", nil)
	if second.Header().Get("ETag") == first {
		t.Error("a changed photo kept its old thumbnail")
	}
	if b := decodeJPEG(t, second.Body.Bytes()).Bounds(); b.Dy() != ThumbSize/2 {
		t.Errorf("size %v", b.Size())
	}
}

func TestThumbErrors(t *testing.T) {
	data := t.TempDir()
	for name, content := range map[string]string{"kaputt.jpg": "no jpeg", "iphone.heic": "x"} {
		if err := os.WriteFile(filepath.Join(data, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	h := handler(t, data)
	tests := []struct {
		path string
		want int
	}{
		{"/kaputt.jpg", http.StatusUnsupportedMediaType},
		{"/iphone.heic", http.StatusUnsupportedMediaType},
		{"/fehlt.jpg", http.StatusNotFound},
		{"/../x.jpg", http.StatusBadRequest},
		{"/" + thumbDir + "/x.jpg", http.StatusNotFound},
	}
	for _, tt := range tests {
		if rec := get(h, "/api/thumb", tt.path, nil); rec.Code != tt.want {
			t.Errorf("%q: status %d, want %d", tt.path, rec.Code, tt.want)
		}
	}
}

func TestThumbInListing(t *testing.T) {
	data := t.TempDir()
	writePNG(t, filepath.Join(data, "a.png"), 2, 2)
	if err := os.WriteFile(filepath.Join(data, "b.heic"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	entries := listing(t, get(handler(t, data), "/api/files", "/", nil)).Entries
	if !entries[0].Thumb || entries[1].Thumb {
		t.Errorf("entries %+v", entries)
	}
}

// exifJPEG is a JPEG of w x h pixels with an EXIF orientation.
func exifJPEG(t *testing.T, w, h int, orientation uint16, order binary.ByteOrder) []byte {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, w, h))
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	// The TIFF header, one directory with one entry, and the next directory's offset.
	const tiffLen = 8 + 2 + 12 + 4
	tiff := make([]byte, tiffLen)
	if order == binary.LittleEndian {
		copy(tiff, "II")
	} else {
		copy(tiff, "MM")
	}
	order.PutUint16(tiff[2:], 42)
	order.PutUint32(tiff[4:], 8)
	order.PutUint16(tiff[8:], 1)
	order.PutUint16(tiff[10:], 0x0112)
	order.PutUint16(tiff[12:], 3) // SHORT
	order.PutUint32(tiff[14:], 1)
	order.PutUint16(tiff[18:], orientation)
	segment := append([]byte("Exif\x00\x00"), tiff...)
	app1 := []byte{0xFF, 0xE1, 0, 0}
	binary.BigEndian.PutUint16(app1[2:], 2+6+tiffLen)
	jpg := buf.Bytes()
	return append(append(append([]byte{}, jpg[:2]...), append(app1, segment...)...), jpg[2:]...)
}

func TestExifOrientation(t *testing.T) {
	for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		if got := exifOrientation(bytes.NewReader(exifJPEG(t, 4, 2, 6, order))); got != 6 {
			t.Errorf("%v: orientation %d, want 6", order, got)
		}
	}
	var plain bytes.Buffer
	if err := jpeg.Encode(&plain, image.NewGray(image.Rect(0, 0, 2, 2)), nil); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{"no EXIF": plain.Bytes(), "not a JPEG": []byte("hello"), "cut off": {0xFF, 0xD8, 0xFF, 0xE1, 0xFF}} {
		if got := exifOrientation(bytes.NewReader(data)); got != 1 {
			t.Errorf("%s: orientation %d, want 1", name, got)
		}
	}
}

func TestThumbTurnsByExif(t *testing.T) {
	data := t.TempDir()
	// Stored 800 x 400, to be shown turned by 90°: 400 x 800.
	if err := os.WriteFile(filepath.Join(data, "hoch.jpg"), exifJPEG(t, 800, 400, 6, binary.BigEndian), 0o600); err != nil {
		t.Fatal(err)
	}
	rec := get(handler(t, data), "/api/thumb", "/hoch.jpg", nil)
	if b := decodeJPEG(t, rec.Body.Bytes()).Bounds(); b.Dx() != ThumbSize/2 || b.Dy() != ThumbSize {
		t.Errorf("size %v, want %dx%d", b.Size(), ThumbSize/2, ThumbSize)
	}
}

func TestOrient(t *testing.T) {
	// 2 x 1: red, blue.
	src := image.NewRGBA(image.Rect(0, 0, 2, 1))
	red, blue := color.RGBA{255, 0, 0, 255}, color.RGBA{0, 0, 255, 255}
	src.Set(0, 0, red)
	src.Set(1, 0, blue)
	tests := []struct {
		orientation int
		w, h        int
		redAt       image.Point
	}{
		{1, 2, 1, image.Pt(0, 0)},
		{2, 2, 1, image.Pt(1, 0)},
		{3, 2, 1, image.Pt(1, 0)},
		{6, 1, 2, image.Pt(0, 0)},
		{8, 1, 2, image.Pt(0, 1)},
	}
	for _, tt := range tests {
		dst := orient(src, tt.orientation)
		if b := dst.Bounds(); b.Dx() != tt.w || b.Dy() != tt.h {
			t.Errorf("%d: size %v", tt.orientation, b.Size())
			continue
		}
		if got := color.RGBAModel.Convert(dst.At(tt.redAt.X, tt.redAt.Y)); got != red {
			t.Errorf("%d: at %v %v, want red", tt.orientation, tt.redAt, got)
		}
	}
}
