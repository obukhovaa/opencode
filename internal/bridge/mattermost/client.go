// Package mattermost is the bridge's Mattermost adapter. It connects to a
// Mattermost server via WebSocket for inbound message dispatch and via REST
// API v4 for outbound message delivery.
//
// The implementation hand-rolls both the WebSocket framing and the REST
// calls rather than depending on github.com/mattermost/mattermost/server/public/model:
// the lib pulls ~272 transitive deps (grpc, protobuf, mlog, msgpack) for a
// protocol that is essentially "send JSON over websocket and POST JSON to
// /api/v4/posts". The TS bridge in the openwork repo makes the same choice
// for the same reasons.
package mattermost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// User is the subset of /api/v4/users/me opencode cares about.
type User struct {
	ID       string `json:"id"`
	Username string `json:"username"`
}

// Post mirrors the WebSocket "posted" event's nested post payload and the
// REST /api/v4/posts response.
type Post struct {
	ID        string         `json:"id"`
	ChannelID string         `json:"channel_id"`
	UserID    string         `json:"user_id"`
	RootID    string         `json:"root_id"`
	Message   string         `json:"message"`
	FileIDs   []string       `json:"file_ids,omitempty"`
	Props     map[string]any `json:"props,omitempty"`
}

// FileInfo mirrors the response from POST /api/v4/files.
type FileInfo struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	MimeType string `json:"mime_type"`
}

// fileUploadResponse mirrors the envelope returned by /api/v4/files.
type fileUploadResponse struct {
	FileInfos []FileInfo `json:"file_infos"`
}

// directChannelRequest is the body for POST /api/v4/channels/direct.
type directChannelResponse struct {
	ID string `json:"id"`
}

// Client is a thin Mattermost REST wrapper. It is safe for concurrent use.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// NewClient builds a REST client against serverURL ("https://mm.example.com",
// no trailing slash required) authenticated with the supplied bearer token.
// The provided http.Client is used for all REST calls; pass nil to use a
// sensible default with a 30s timeout.
func NewClient(serverURL, accessToken string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{
		baseURL: strings.TrimRight(serverURL, "/"),
		token:   accessToken,
		http:    httpClient,
	}
}

// BaseURL returns the normalized base URL (no trailing slash). Used by tests
// to construct expected request paths.
func (c *Client) BaseURL() string { return c.baseURL }

// AuthToken returns the access token; used by the WebSocket auth challenge.
func (c *Client) AuthToken() string { return c.token }

// HTTPClient returns the underlying http.Client (exposed for tests that
// want to inspect the transport).
func (c *Client) HTTPClient() *http.Client { return c.http }

// FileURL returns the canonical download URL for a Mattermost file ID.
func (c *Client) FileURL(fileID string) string {
	return c.baseURL + "/api/v4/files/" + url.PathEscape(fileID)
}

// WebSocketURL returns the wss:// URL for the auth-required event stream.
// HTTPS upgrades to WSS; HTTP downgrades to WS (test-only).
func (c *Client) WebSocketURL() string {
	wsURL := c.baseURL
	wsURL = strings.Replace(wsURL, "https://", "wss://", 1)
	wsURL = strings.Replace(wsURL, "http://", "ws://", 1)
	return wsURL + "/api/v4/websocket"
}

// GetMe calls GET /api/v4/users/me.
func (c *Client) GetMe(ctx context.Context) (User, error) {
	var u User
	if err := c.do(ctx, http.MethodGet, "/api/v4/users/me", nil, &u); err != nil {
		return User{}, err
	}
	return u, nil
}

// CreatePostInput captures the body of POST /api/v4/posts.
type CreatePostInput struct {
	ChannelID string
	Message   string
	RootID    string // optional thread root
	FileIDs   []string
}

// CreatePost calls POST /api/v4/posts and returns the resulting Post.
// rootID and fileIDs are optional — pass empty/nil to omit.
func (c *Client) CreatePost(ctx context.Context, in CreatePostInput) (Post, error) {
	body := map[string]any{
		"channel_id": in.ChannelID,
		"message":    in.Message,
	}
	if in.RootID != "" {
		body["root_id"] = in.RootID
	}
	if len(in.FileIDs) > 0 {
		body["file_ids"] = in.FileIDs
	}
	var p Post
	if err := c.do(ctx, http.MethodPost, "/api/v4/posts", body, &p); err != nil {
		return Post{}, err
	}
	return p, nil
}

// FileUpload is one file to upload via POST /api/v4/files.
type FileUpload struct {
	Filename string
	Data     []byte
}

// UploadFiles calls POST /api/v4/files (multipart) and returns the resulting
// file_info entries. The Mattermost API binds the upload to channelID — files
// can later be attached to a post in that channel via CreatePost.FileIDs.
func (c *Client) UploadFiles(ctx context.Context, channelID string, files []FileUpload) ([]FileInfo, error) {
	if len(files) == 0 {
		return nil, nil
	}
	buf := &bytes.Buffer{}
	mw := multipart.NewWriter(buf)
	if err := mw.WriteField("channel_id", channelID); err != nil {
		return nil, err
	}
	for _, f := range files {
		w, err := mw.CreateFormFile("files", f.Filename)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(f.Data); err != nil {
			return nil, err
		}
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/v4/files", buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("mattermost UploadFiles: %d %s", resp.StatusCode, string(body))
	}
	var out fileUploadResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("mattermost UploadFiles: decode: %w", err)
	}
	return out.FileInfos, nil
}

// UserTyping POSTs /users/{userID}/typing announcing the bot is typing in
// channelID. Best-effort — Mattermost broadcasts but a failure is not fatal.
func (c *Client) UserTyping(ctx context.Context, userID, channelID string) error {
	body := map[string]any{"channel_id": channelID}
	return c.do(ctx, http.MethodPost, "/api/v4/users/"+url.PathEscape(userID)+"/typing", body, nil)
}

// CreateDirectChannel calls POST /api/v4/channels/direct with the two user IDs
// and returns the resulting channel ID. Used by ResolveUserToDM to translate
// a Mattermost user ID into the DM channel form used by binding rows.
func (c *Client) CreateDirectChannel(ctx context.Context, botUserID, userID string) (string, error) {
	body := []string{botUserID, userID}
	var ch directChannelResponse
	if err := c.do(ctx, http.MethodPost, "/api/v4/channels/direct", body, &ch); err != nil {
		return "", err
	}
	return ch.ID, nil
}

// IsUser reports whether the given 26-char base32 ID corresponds to a
// real Mattermost user. Used to distinguish a user ID (which needs
// channels/direct resolution) from a channel ID (which is already in
// its final form for binding). Mattermost shares the 26-char alphabet
// between users and channels, so a pure-shape regex cannot tell them
// apart; a HEAD request against /users/{id} is the cheapest probe
// available. Returns false on any non-200 (including 404).
func (c *Client) IsUser(ctx context.Context, id string) bool {
	if id == "" {
		return false
	}
	if err := c.do(ctx, http.MethodGet, "/api/v4/users/"+url.PathEscape(id), nil, nil); err != nil {
		return false
	}
	return true
}

// DownloadFile fetches GET /api/v4/files/{id}. The returned ReadCloser must
// be closed by the caller. Used for inbound file persistence into the media
// store.
func (c *Client) DownloadFile(ctx context.Context, fileID string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.FileURL(fileID), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, fmt.Errorf("mattermost DownloadFile: %d %s", resp.StatusCode, string(body))
	}
	return resp.Body, nil
}

// HTTPError is returned by Client when the server reports a non-2xx response.
type HTTPError struct {
	Status int
	Body   string
	Path   string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("mattermost: %s: %d %s", e.Path, e.Status, e.Body)
}

// do executes a JSON-bodied REST call against /api/v4/<path>. If out is
// non-nil and the response is 2xx, the response body is decoded into it.
func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("mattermost: marshal %s: %w", path, err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		buf, _ := io.ReadAll(resp.Body)
		return &HTTPError{Status: resp.StatusCode, Body: string(buf), Path: method + " " + path}
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("mattermost: decode %s: %w", path, err)
	}
	return nil
}
