package desktop

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/Programmer-Den/DATA-DEN/backend/internal/vault"
)

// The vault (Tresor) API. Every path is made of sealed names, such as
// "/<sealed>/<sealed>"; the server never sees a plain name or file.

// VaultEntry is one file or folder in the vault, under its sealed name.
type VaultEntry struct {
	Name     string `json:"name"`
	Dir      bool   `json:"dir"`
	Size     int64  `json:"size"`
	Modified string `json:"modified"`
}

// VaultKeys returns the wrapped vault keys; nil when the account has no
// vault yet.
func (c *Client) VaultKeys(ctx context.Context) (*vault.Keys, error) {
	resp, err := c.do(ctx, http.MethodGet, "/api/vault", nil, "", nil, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, answerError(resp)
	}
	var answer struct {
		Keys *vault.Keys `json:"keys"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&answer); err != nil {
		return nil, err
	}
	return answer.Keys, nil
}

// VaultList lists the vault folder dir; "/" is the vault itself.
func (c *Client) VaultList(ctx context.Context, dir string) ([]VaultEntry, error) {
	resp, err := c.do(ctx, http.MethodGet, "/api/vault/files", url.Values{"path": {dir}}, "", nil, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, answerError(resp)
	}
	var answer struct {
		Entries []VaultEntry `json:"entries"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&answer); err != nil {
		return nil, err
	}
	return answer.Entries, nil
}

// VaultMkdir creates the vault folder p.
func (c *Client) VaultMkdir(ctx context.Context, p string) error {
	_, err := c.postJSON(ctx, "/api/vault/folders", map[string]string{"path": p}, nil)
	return err
}

// VaultMove renames or moves from to to.
func (c *Client) VaultMove(ctx context.Context, from, to string) error {
	_, err := c.postJSON(ctx, "/api/vault/move", map[string]string{"from": from, "to": to}, nil)
	return err
}

// VaultDelete deletes p for good, a folder with everything in it.
func (c *Client) VaultDelete(ctx context.Context, p string) error {
	_, err := c.postJSON(ctx, "/api/vault/delete", map[string]string{"path": p}, nil)
	return err
}

// VaultUpload stores the sealed file of size bytes from body at p; ErrChanged
// when something is there already.
func (c *Client) VaultUpload(ctx context.Context, p string, body io.Reader, size int64) error {
	target := strings.TrimRight(c.Server, "/") + "/api/vault/file?" + url.Values{"path": {p}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, target, body)
	if err != nil {
		return err
	}
	req.ContentLength = size
	resp, err := c.send(req, "application/octet-stream")
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		return answerError(resp)
	}
	return nil
}

// VaultDownload writes the sealed file p to w.
func (c *Client) VaultDownload(ctx context.Context, p string, w io.Writer) error {
	resp, err := c.do(ctx, http.MethodGet, "/api/vault/file", url.Values{"path": {p}}, "", nil, nil)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return answerError(resp)
	}
	_, err = io.Copy(w, io.LimitReader(resp.Body, vault.SealedSize(vault.MaxPlain)+1))
	return err
}
