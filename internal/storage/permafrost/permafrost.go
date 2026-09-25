// Package permafrost is a storage backend for the hosted Permafrost service.
// The HTTP API it speaks is documented in docs/PERMAFROST.md.
package permafrost

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/rhymeswithlimo/frost/internal/storage"
)

const (
	maxAttempts  = 4
	firstBackoff = 500 * time.Millisecond
)

// Backend talks to a Permafrost server.
type Backend struct {
	base   *url.URL
	token  string
	client *http.Client
	// sleep is swapped out in tests.
	sleep func(context.Context, time.Duration) error
}

// New returns a client for the server at baseURL.
func New(baseURL, token string) (*Backend, error) {
	u, err := url.Parse(strings.TrimSuffix(baseURL, "/"))
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("permafrost: invalid url %q", baseURL)
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && isLocal(u.Hostname())) {
		return nil, fmt.Errorf("permafrost: url must use https")
	}
	if token == "" {
		return nil, errors.New("permafrost: token is required")
	}
	return &Backend{
		base:   u,
		token:  token,
		client: &http.Client{Timeout: 5 * time.Minute},
		sleep:  sleepCtx,
	}, nil
}

func isLocal(host string) bool {
	return host == "localhost" || net.ParseIP(host).IsLoopback()
}

// APIError is an error response from the server.
type APIError struct {
	Status  int
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *APIError) Error() string {
	return fmt.Sprintf("permafrost: %s (%d %s)", e.Message, e.Status, e.Code)
}

func (b *Backend) objectURL(key string) string {
	return b.base.String() + "/v1/objects/" + key
}

func (b *Backend) Put(ctx context.Context, key string, data []byte) error {
	sum := sha256.Sum256(data)
	_, err := b.do(ctx, http.MethodPut, b.objectURL(key), data, map[string]string{
		"Content-Type":     "application/octet-stream",
		"X-Content-SHA256": hex.EncodeToString(sum[:]),
	})
	return err
}

func (b *Backend) Get(ctx context.Context, key string) ([]byte, error) {
	resp, err := b.do(ctx, http.MethodGet, b.objectURL(key), nil, nil)
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
			return nil, storage.ErrNotFound
		}
		return nil, err
	}
	if want := resp.header.Get("X-Content-SHA256"); want != "" {
		sum := sha256.Sum256(resp.body)
		if hex.EncodeToString(sum[:]) != want {
			return nil, fmt.Errorf("permafrost get %s: body doesn't match its checksum", key)
		}
	}
	return resp.body, nil
}

func (b *Backend) List(ctx context.Context, prefix string) ([]string, error) {
	var keys []string
	cursor := ""
	for {
		q := url.Values{"prefix": {prefix}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		resp, err := b.do(ctx, http.MethodGet, b.base.String()+"/v1/objects?"+q.Encode(), nil, nil)
		if err != nil {
			return nil, err
		}
		var page struct {
			Keys       []string `json:"keys"`
			NextCursor string   `json:"next_cursor"`
		}
		if err := json.Unmarshal(resp.body, &page); err != nil {
			return nil, fmt.Errorf("permafrost list: %w", err)
		}
		keys = append(keys, page.Keys...)
		if page.NextCursor == "" {
			return keys, nil
		}
		cursor = page.NextCursor
	}
}

func (b *Backend) Delete(ctx context.Context, key string) error {
	_, err := b.do(ctx, http.MethodDelete, b.objectURL(key), nil, nil)
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
		return nil
	}
	return err
}

func (b *Backend) String() string { return "permafrost:" + b.base.Host }

type response struct {
	header http.Header
	body   []byte
}

// do sends a request, retrying rate limits, server errors and network errors.
func (b *Backend) do(ctx context.Context, method, u string, body []byte, headers map[string]string) (*response, error) {
	backoff := firstBackoff
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.ContentLength = int64(len(body))
		req.Header.Set("Authorization", "Bearer "+b.token)
		req.Header.Set("User-Agent", "frost")
		for k, v := range headers {
			req.Header.Set(k, v)
		}

		wait := backoff
		resp, err := b.client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			lastErr = fmt.Errorf("permafrost: %w", err)
		} else {
			data, readErr := io.ReadAll(resp.Body)
			resp.Body.Close()
			switch {
			case readErr != nil:
				lastErr = fmt.Errorf("permafrost: reading response: %w", readErr)
			case resp.StatusCode < 300:
				return &response{header: resp.Header, body: data}, nil
			default:
				apiErr := &APIError{Status: resp.StatusCode, Code: "unknown", Message: http.StatusText(resp.StatusCode)}
				var wrapped struct {
					Error *APIError `json:"error"`
				}
				if json.Unmarshal(data, &wrapped) == nil && wrapped.Error != nil {
					apiErr.Code, apiErr.Message = wrapped.Error.Code, wrapped.Error.Message
				}
				if !retryable(resp.StatusCode) {
					return nil, apiErr
				}
				lastErr = apiErr
				if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s >= 0 {
					wait = time.Duration(s) * time.Second
				}
			}
		}
		if attempt < maxAttempts {
			if err := b.sleep(ctx, wait); err != nil {
				return nil, err
			}
			backoff *= 2
		}
	}
	return nil, lastErr
}

func retryable(status int) bool {
	return status == http.StatusTooManyRequests || (status >= 500 && status != http.StatusInsufficientStorage)
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
