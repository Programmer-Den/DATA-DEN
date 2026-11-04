// Package desktop keeps a folder on a PC and a folder in DATA-DEN the same,
// both ways, for the sync client cmd/data-den-sync.
package desktop

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// chunkSize is how much one PUT of an upload carries; the server takes up
// to 32 MiB.
const chunkSize = 8 << 20

// ErrChanged means the server refused because the file changed there
// meanwhile, or a new one appeared; the next round sees the change.
var ErrChanged = errors.New("changed on the server meanwhile")

// ErrLogin means the server did not accept the app password.
var ErrLogin = errors.New("the server did not accept the app password")

// Client talks to the DATA-DEN API.
type Client struct {
	// Server is the address, such as "https://nas.example.org".
	Server string
	// Token is the app password, sent as a bearer token.
	Token string
	HTTP  *http.Client
}

// Remote is one file or folder on the server, as /api/sync/tree lists it.
type Remote struct {
	Path   string `json:"path"`
	Dir    bool   `json:"dir"`
	Size   int64  `json:"size"`
	Mod    int64  `json:"mod"`
	SHA256 string `json:"sha256"`
}

func (c *Client) do(ctx context.Context, method, endpoint string, query url.Values, contentType string, body io.Reader, header http.Header) (*http.Response, error) {
	target := strings.TrimRight(c.Server, "/") + endpoint
	if query != nil {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("User-Agent", "data-den-sync")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for k, v := range header {
		req.Header[k] = v
	}
	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		_ = resp.Body.Close()
		return nil, ErrLogin
	}
	return resp, nil
}

// postJSON sends v and decodes the answer into out, which may be nil.
func (c *Client) postJSON(ctx context.Context, endpoint string, v, out any) (int, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return 0, err
	}
	resp, err := c.do(ctx, http.MethodPost, endpoint, nil, "application/json", bytes.NewReader(data), nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		return resp.StatusCode, answerError(resp)
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return resp.StatusCode, err
		}
	}
	return resp.StatusCode, nil
}

// answerError turns an error answer into an error, ErrChanged for 409
// and 412.
func answerError(resp *http.Response) error {
	if resp.StatusCode == http.StatusConflict || resp.StatusCode == http.StatusPreconditionFailed {
		return ErrChanged
	}
	var body struct {
		Error string `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&body)
	if body.Error == "" {
		body.Error = resp.Status
	}
	return fmt.Errorf("server: %s (%d)", body.Error, resp.StatusCode)
}

// Tree lists everything below the folder dir, such as "/" or "/Dokumente".
// A folder that does not exist yet is created.
func (c *Client) Tree(ctx context.Context, dir string) ([]Remote, error) {
	resp, err := c.do(ctx, http.MethodGet, "/api/sync/tree", url.Values{"path": {dir}}, "", nil, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound && dir != "/" {
		_ = resp.Body.Close()
		if err := c.MkdirAll(ctx, dir); err != nil {
			return nil, err
		}
		return []Remote{}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, answerError(resp)
	}
	var answer struct {
		Entries []Remote `json:"entries"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&answer); err != nil {
		return nil, err
	}
	return answer.Entries, nil
}

// Mkdir creates the folder p; one that exists already is fine.
func (c *Client) Mkdir(ctx context.Context, p string) error {
	_, err := c.postJSON(ctx, "/api/folders", map[string]string{"path": p}, nil)
	if errors.Is(err, ErrChanged) {
		return nil
	}
	return err
}

// MkdirAll creates the folder p and those above it.
func (c *Client) MkdirAll(ctx context.Context, p string) error {
	parts := strings.Split(strings.Trim(p, "/"), "/")
	for i := range parts {
		if err := c.Mkdir(ctx, "/"+strings.Join(parts[:i+1], "/")); err != nil {
			return err
		}
	}
	return nil
}

// Delete moves p into the recycle bin. With state, only a file that is
// still so is deleted; otherwise the error is ErrChanged.
func (c *Client) Delete(ctx context.Context, p string, state *Remote) error {
	body := map[string]any{"path": p}
	if state != nil {
		body["size"] = state.Size
		body["mod"] = state.Mod
	}
	status, err := c.postJSON(ctx, "/api/delete", body, nil)
	if status == http.StatusNotFound {
		return nil
	}
	return err
}

// Download writes the file p to w.
func (c *Client) Download(ctx context.Context, p string, w io.Writer) error {
	resp, err := c.do(ctx, http.MethodGet, "/api/download", url.Values{"path": {p}}, "", nil, nil)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return ErrChanged
	}
	if resp.StatusCode != http.StatusOK {
		return answerError(resp)
	}
	_, err = io.Copy(w, resp.Body)
	return err
}

// Upload sends the local file name to p, size bytes long, and returns the
// time the server gave it and the checksum of what was sent. With replace,
// it replaces the file there if that is still as replace says; otherwise,
// and when a file appeared at p, the error is ErrChanged.
func (c *Client) Upload(ctx context.Context, name, p string, size int64, replace *Remote) (int64, string, error) {
	file, err := os.Open(name) //nolint:gosec // a file of the synced folder
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = file.Close() }()
	body := map[string]any{"path": p, "size": size}
	if replace != nil {
		body["replace"] = true
		body["expect"] = map[string]int64{"size": replace.Size, "mod": replace.Mod}
	}
	var started struct {
		ID  string `json:"id"`
		Mod int64  `json:"mod"`
	}
	if _, err := c.postJSON(ctx, "/api/uploads", body, &started); err != nil {
		return 0, "", err
	}
	hash := sha256.New()
	if size == 0 {
		return started.Mod, hex.EncodeToString(hash.Sum(nil)), nil
	}
	buf := make([]byte, chunkSize)
	var offset int64
	failures := 0
	for {
		n, err := io.ReadFull(io.NewSectionReader(file, offset, size-offset), buf[:min(int64(len(buf)), size-offset)])
		if err != nil {
			return 0, "", fmt.Errorf("reading %s: %w", name, err)
		}
		next, mod, err := c.putChunk(ctx, started.ID, offset, buf[:n])
		if err != nil {
			failures++
			if failures > 3 || errors.Is(err, ErrChanged) || errors.Is(err, ErrLogin) || ctx.Err() != nil {
				return 0, "", err
			}
			time.Sleep(time.Duration(failures) * 2 * time.Second)
			if next, err = c.uploadOffset(ctx, started.ID); err != nil {
				return 0, "", err
			}
		} else {
			failures = 0
		}
		if next > offset {
			// What the server took is final, so it goes into the checksum.
			hash.Write(buf[:next-offset])
		}
		offset = next
		if offset == size {
			return mod, hex.EncodeToString(hash.Sum(nil)), nil
		}
	}
}

// putChunk sends data at offset and returns the server's new offset, and
// the file's time once the upload is complete.
func (c *Client) putChunk(ctx context.Context, id string, offset int64, data []byte) (int64, int64, error) {
	resp, err := c.do(ctx, http.MethodPut, "/api/uploads/"+url.PathEscape(id), nil, "application/octet-stream",
		bytes.NewReader(data), http.Header{"Upload-Offset": {strconv.FormatInt(offset, 10)}})
	if err != nil {
		return offset, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusPreconditionFailed || resp.StatusCode == http.StatusNotFound {
		return offset, 0, ErrChanged
	}
	if resp.StatusCode != http.StatusOK {
		return offset, 0, fmt.Errorf("upload: %s", resp.Status)
	}
	var answer struct {
		Offset int64 `json:"offset"`
		Mod    int64 `json:"mod"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&answer); err != nil {
		return offset, 0, err
	}
	return answer.Offset, answer.Mod, nil
}

func (c *Client) uploadOffset(ctx context.Context, id string) (int64, error) {
	resp, err := c.do(ctx, http.MethodGet, "/api/uploads/"+url.PathEscape(id), nil, "", nil, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		// The server restarted and dropped the upload: the next round
		// starts it again.
		return 0, ErrChanged
	}
	var answer struct {
		Offset int64 `json:"offset"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&answer); err != nil {
		return 0, err
	}
	return answer.Offset, nil
}
