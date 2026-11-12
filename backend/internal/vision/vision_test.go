package vision

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// fakeService answers like Immich's machine learning service. The texts get
// embeddings along their own axis; the photo points along the dog's.
func fakeService(t *testing.T, photos *atomic.Int32) *httptest.Server {
	t.Helper()
	axis := func(i int) string {
		v := make([]float32, len(Labels))
		v[i] = 3 // not of length 1, as some models answer
		data, _ := json.Marshal(v)
		return string(data)
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/predict" {
			http.NotFound(w, r)
			return
		}
		var entries map[string]map[string]slot
		if err := json.Unmarshal([]byte(r.FormValue("entries")), &entries); err != nil {
			t.Errorf("entries: %v", err)
		}
		if text := r.FormValue("text"); text != "" {
			if entries["clip"]["textual"].ModelName != DefaultClipModel {
				t.Errorf("text entries %v", entries)
			}
			for i, l := range Labels {
				if l.Prompt == text {
					_ = json.NewEncoder(w).Encode(map[string]string{"clip": axis(i)})
					return
				}
			}
			t.Errorf("unknown text %q", text)
			return
		}
		file, _, err := r.FormFile("image")
		if err != nil {
			t.Errorf("no image: %v", err)
			return
		}
		data, _ := io.ReadAll(file)
		if string(data) != "JPEG" {
			t.Errorf("image %q", data)
		}
		fr := entries["facial-recognition"]
		if fr["detection"].ModelName != "buffalo_l" || fr["recognition"].ModelName != "buffalo_l" || entries["clip"]["visual"].ModelName != DefaultClipModel {
			t.Errorf("image entries %v", entries)
		}
		photos.Add(1)
		_, _ = fmt.Fprintf(w, `{"clip": %q, "facial-recognition": [{"boundingBox": {"x1": 10, "y1": 20, "x2": 50, "y2": 80}, "embedding": "[0, 2]", "score": 0.9}], "imageHeight": 100, "imageWidth": 200}`, axis(0))
	}))
}

func TestRecognize(t *testing.T) {
	var photos atomic.Int32
	srv := fakeService(t, &photos)
	defer srv.Close()
	c := New(Config{URL: srv.URL})
	for range 2 {
		got, err := c.Recognize(context.Background(), []byte("JPEG"))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Join(got.Labels, ",") != "Hunde" {
			t.Errorf("labels %v", got.Labels)
		}
		if len(got.Faces) != 1 || got.Faces[0].Box != [4]float64{0.05, 0.2, 0.25, 0.8} {
			t.Fatalf("faces %+v", got.Faces)
		}
		if e := got.Faces[0].Embedding; e[0] != 0 || e[1] != 1 {
			t.Errorf("embedding %v, want length 1", e)
		}
	}
	if photos.Load() != 2 {
		t.Errorf("%d photos asked for", photos.Load())
	}
}

func TestRecognizeFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "model not found", http.StatusInternalServerError)
	}))
	defer srv.Close()
	_, err := New(Config{URL: srv.URL}).Recognize(context.Background(), []byte("JPEG"))
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Errorf("error %v", err)
	}
}

func TestLabelsOf(t *testing.T) {
	texts := make([][]float32, len(Labels))
	for i := range texts {
		texts[i] = make([]float32, len(Labels))
		texts[i][i] = 1
	}
	// Between dog and cat: both.
	mixed := Normalize(make([]float32, len(Labels)))
	mixed[0], mixed[1] = 0.6, 0.598
	if got := labelsOf(Normalize(mixed), texts); strings.Join(got, ",") != "Hunde,Katzen" {
		t.Errorf("dog and cat: %v", got)
	}
	// Closest to "a photo": nothing.
	plain := make([]float32, len(Labels))
	plain[len(Labels)-4] = 1
	if got := labelsOf(plain, texts); len(got) != 0 {
		t.Errorf("plain photo: %v", got)
	}
	// Close to all alike: nothing is sure enough.
	even := make([]float32, len(Labels))
	for i := range even {
		even[i] = 1
	}
	if got := labelsOf(Normalize(even), texts); len(got) != 0 {
		t.Errorf("unsure: %v", got)
	}
}

func TestSimilarity(t *testing.T) {
	a := Normalize([]float32{3, 4})
	if math.Abs(Similarity(a, a)-1) > 1e-6 || Similarity(a, []float32{1}) != 0 {
		t.Error("similarity")
	}
	if New(Config{}).Version() == New(Config{ClipModel: "other"}).Version() {
		t.Error("the version does not name the model")
	}
}
