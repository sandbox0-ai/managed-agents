package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"testing"
	"time"
)

type apiClient struct {
	baseURL string
	token   string
	beta    string
	http    *http.Client
}

func newClient(cfg testConfig) *apiClient {
	return &apiClient{
		baseURL: cfg.BaseURL,
		token:   cfg.Token,
		beta:    cfg.Beta,
		http:    &http.Client{Timeout: 10 * time.Minute},
	}
}

func (c *apiClient) get(ctx context.Context, path string) (map[string]any, int, error) {
	return c.do(ctx, http.MethodGet, path, nil)
}

func (c *apiClient) post(ctx context.Context, path string, body any) (map[string]any, int, error) {
	return c.do(ctx, http.MethodPost, path, body)
}

func (c *apiClient) postMultipart(ctx context.Context, path string, fields map[string]string, files map[string]string) (map[string]any, int, error) {
	var payload bytes.Buffer
	writer := multipart.NewWriter(&payload)
	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			return nil, 0, fmt.Errorf("write multipart field: %w", err)
		}
	}
	for filename, content := range files {
		part, err := writer.CreateFormFile("files", filename)
		if err != nil {
			return nil, 0, fmt.Errorf("create multipart file: %w", err)
		}
		if _, err := part.Write([]byte(content)); err != nil {
			return nil, 0, fmt.Errorf("write multipart file: %w", err)
		}
	}
	if err := writer.Close(); err != nil {
		return nil, 0, fmt.Errorf("close multipart writer: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, &payload)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Anthropic-Beta", c.beta)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return c.doRequest(req)
}

func (c *apiClient) put(ctx context.Context, path string, body any) (map[string]any, int, error) {
	return c.do(ctx, http.MethodPut, path, body)
}

func (c *apiClient) delete(ctx context.Context, path string) (map[string]any, int, error) {
	return c.do(ctx, http.MethodDelete, path, nil)
}

func (c *apiClient) do(ctx context.Context, method, path string, body any) (map[string]any, int, error) {
	var payload io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, 0, fmt.Errorf("marshal request: %w", err)
		}
		payload = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, payload)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Anthropic-Beta", c.beta)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	return c.readResponse(req.Method, path, resp, err)
}

func (c *apiClient) doRequest(req *http.Request) (map[string]any, int, error) {
	resp, err := c.http.Do(req)
	return c.readResponse(req.Method, req.URL.Path, resp, err)
}

func (c *apiClient) readResponse(method, path string, resp *http.Response, err error) (map[string]any, int, error) {
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if resp.StatusCode >= 400 {
		return nil, resp.StatusCode, fmt.Errorf("%s %s failed with %d: %s", method, path, resp.StatusCode, string(raw))
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return map[string]any{}, resp.StatusCode, nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, resp.StatusCode, fmt.Errorf("decode response %s %s: %w: %s", method, path, err, string(raw))
	}
	return out, resp.StatusCode, nil
}

func requireString(t *testing.T, obj map[string]any, key string) string {
	t.Helper()
	value, ok := obj[key].(string)
	if !ok || value == "" {
		t.Fatalf("response missing string %q: %#v", key, obj)
	}
	return value
}

func listData(obj map[string]any) []any {
	items, _ := obj["data"].([]any)
	return items
}

func eventually(t *testing.T, timeout, interval time.Duration, fn func() error) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		if err := fn(); err == nil {
			return
		} else {
			lastErr = err
		}
		time.Sleep(interval)
	}
	t.Fatalf("condition not met after %s: %v", timeout, lastErr)
}
