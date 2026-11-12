// Package vision finds faces and things in photos, on the NAS itself: it
// asks Immich's machine learning service, a container next to DATA-DEN that
// runs the models on the VM's CPU, so no photo leaves the NAS.
//
// Each photo goes in one request with two tasks: facial recognition
// (InsightFace's buffalo_l by default), which finds the faces and an
// embedding for each, and CLIP (ViT-B-32__openai by default), whose image
// embedding is compared with the embeddings of a short text for each Label.
package vision

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"sync"
	"time"
)

const (
	// DefaultFaceModel finds and recognises faces.
	DefaultFaceModel = "buffalo_l"
	// DefaultClipModel describes photos and texts in the same space.
	DefaultClipModel = "ViT-B-32__openai"
	// minFaceScore is how sure the detector must be that it is a face.
	minFaceScore = 0.7
	// maxAnswer is the largest answer read: embeddings of a few dozen faces.
	maxAnswer = 8 << 20
)

// Config is where the service runs and which models it uses.
type Config struct {
	// URL is the machine learning service, such as http://127.0.0.1:3003.
	URL string
	// FaceModel and ClipModel are the models; "" for the defaults.
	FaceModel string
	ClipModel string
	// Client sends the requests; nil uses one with a long timeout, as the
	// service downloads its models on first use.
	Client *http.Client
}

// Face is a face found in a photo.
type Face struct {
	// Box is where the face is, as fractions of the photo's width and
	// height: left, top, right, bottom.
	Box [4]float64
	// Embedding describes the face, of length 1; the faces of one person
	// point the same way.
	Embedding []float32
}

// Result is what was found in a photo.
type Result struct {
	Faces []Face
	// Labels are the names of the Labels seen, the surest first.
	Labels []string
}

// Client asks the service, one photo at a time, so it never takes all of
// the VM's CPU.
type Client struct {
	cfg  Config
	busy sync.Mutex
	// texts are the embeddings of Labels' prompts, nil until asked for.
	texts [][]float32
}

// New returns a Client for cfg.
func New(cfg Config) *Client {
	if cfg.FaceModel == "" {
		cfg.FaceModel = DefaultFaceModel
	}
	if cfg.ClipModel == "" {
		cfg.ClipModel = DefaultClipModel
	}
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: 10 * time.Minute}
	}
	return &Client{cfg: cfg}
}

// Version names the models and the labels; a photo recognised with another
// version is recognised again.
func (c *Client) Version() string {
	return c.cfg.FaceModel + "+" + c.cfg.ClipModel + "+" + labelsVersion()
}

type slot struct {
	ModelName string         `json:"modelName"`
	Options   map[string]any `json:"options,omitempty"`
}

// answer is the service's answer; the embeddings are JSON arrays in strings.
type answer struct {
	Clip   string `json:"clip"`
	Faces  []face `json:"facial-recognition"`
	Height int    `json:"imageHeight"`
	Width  int    `json:"imageWidth"`
}

type face struct {
	BoundingBox struct {
		X1, Y1, X2, Y2 float64
	} `json:"boundingBox"`
	Embedding string `json:"embedding"`
}

// Recognize finds the faces and labels in photo, a JPEG.
func (c *Client) Recognize(ctx context.Context, photo []byte) (Result, error) {
	c.busy.Lock()
	defer c.busy.Unlock()
	if c.texts == nil {
		texts, err := c.textEmbeddings(ctx)
		if err != nil {
			return Result{}, err
		}
		c.texts = texts
	}
	entries := map[string]any{
		"clip": map[string]slot{"visual": {ModelName: c.cfg.ClipModel}},
		"facial-recognition": map[string]slot{
			"detection":   {ModelName: c.cfg.FaceModel, Options: map[string]any{"minScore": minFaceScore}},
			"recognition": {ModelName: c.cfg.FaceModel},
		},
	}
	var a answer
	if err := c.predict(ctx, entries, "image", photo, &a); err != nil {
		return Result{}, err
	}
	if a.Width <= 0 || a.Height <= 0 {
		return Result{}, errors.New("vision: the answer has no image size")
	}
	image, err := vector(a.Clip)
	if err != nil {
		return Result{}, fmt.Errorf("vision: the photo's embedding: %w", err)
	}
	result := Result{Labels: labelsOf(image, c.texts)}
	for _, f := range a.Faces {
		embedding, err := vector(f.Embedding)
		if err != nil {
			return Result{}, fmt.Errorf("vision: a face's embedding: %w", err)
		}
		b := f.BoundingBox
		w, h := float64(a.Width), float64(a.Height)
		result.Faces = append(result.Faces, Face{
			Box:       [4]float64{clamp(b.X1 / w), clamp(b.Y1 / h), clamp(b.X2 / w), clamp(b.Y2 / h)},
			Embedding: embedding,
		})
	}
	return result, nil
}

// textEmbeddings asks for the embedding of each label's prompt.
func (c *Client) textEmbeddings(ctx context.Context) ([][]float32, error) {
	entries := map[string]any{"clip": map[string]slot{"textual": {ModelName: c.cfg.ClipModel}}}
	texts := make([][]float32, len(Labels))
	for i, label := range Labels {
		var a answer
		if err := c.predict(ctx, entries, "text", []byte(label.Prompt), &a); err != nil {
			return nil, err
		}
		v, err := vector(a.Clip)
		if err != nil {
			return nil, fmt.Errorf("vision: the embedding of %q: %w", label.Prompt, err)
		}
		texts[i] = v
	}
	return texts, nil
}

// predict posts entries and input, the image or the text, to /predict.
func (c *Client) predict(ctx context.Context, entries map[string]any, field string, input []byte, v any) error {
	tasks, err := json.Marshal(entries)
	if err != nil {
		return err
	}
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	if err := form.WriteField("entries", string(tasks)); err != nil {
		return err
	}
	if field == "image" {
		part, err := form.CreateFormFile("image", "photo.jpg")
		if err != nil {
			return err
		}
		if _, err := part.Write(input); err != nil {
			return err
		}
	} else if err := form.WriteField(field, string(input)); err != nil {
		return err
	}
	if err := form.Close(); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.URL+"/predict", &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", form.FormDataContentType())
	resp, err := c.cfg.Client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswer))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("vision: status %d: %.200s", resp.StatusCode, data)
	}
	return json.Unmarshal(data, v)
}

// vector reads an embedding, a JSON array of numbers, and scales it to
// length 1.
func vector(s string) ([]float32, error) {
	var v []float32
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return nil, err
	}
	if len(v) == 0 {
		return nil, errors.New("empty")
	}
	return Normalize(v), nil
}

// Normalize scales v to length 1, in place, and returns it.
func Normalize(v []float32) []float32 {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if sum == 0 {
		return v
	}
	n := float32(1 / math.Sqrt(sum))
	for i := range v {
		v[i] *= n
	}
	return v
}

// Similarity is the cosine of a and b, which have length 1: 1 for the same
// direction; 0 when they differ in length.
func Similarity(a, b []float32) float64 {
	if len(a) != len(b) {
		return 0
	}
	var sum float64
	for i, x := range a {
		sum += float64(x) * float64(b[i]) //nolint:gosec // b is as long as a
	}
	return sum
}

func clamp(v float64) float64 {
	return math.Max(0, math.Min(1, v))
}
