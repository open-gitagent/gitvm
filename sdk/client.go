package sdk

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// Client talks to the gitvm API server.
type Client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

// NewClient creates a new gitvm API client.
func NewClient(serverURL, apiKey string) *Client {
	return &Client{
		baseURL:    serverURL,
		apiKey:     apiKey,
		httpClient: &http.Client{},
	}
}

// CreateVM creates a new VM and returns its info.
func (c *Client) CreateVM(ctx context.Context, req map[string]interface{}) (map[string]interface{}, error) {
	return c.postJSON(ctx, "/v1/vms", req)
}

// DeleteVM deletes a VM by ID.
func (c *Client) DeleteVM(ctx context.Context, id string) error {
	_, err := c.doRequest(ctx, "DELETE", "/v1/vms/"+id, nil)
	return err
}

// GetVM returns VM info by ID.
func (c *Client) GetVM(ctx context.Context, id string) (map[string]interface{}, error) {
	return c.getJSON(ctx, "/v1/vms/"+id)
}

// Exec runs a command in a VM and returns the result.
func (c *Client) Exec(ctx context.Context, vmID string, command string, opts *ExecuteOptions) (*ExecutionResult, error) {
	body := map[string]interface{}{
		"command": command,
	}
	if opts != nil {
		if opts.Cwd != "" {
			body["cwd"] = opts.Cwd
		}
		if opts.Env != nil {
			body["env"] = opts.Env
		}
		if opts.Timeout > 0 {
			body["timeout"] = opts.Timeout
		}
	}

	respData, err := c.postJSON(ctx, "/v1/vms/"+vmID+"/exec", body)
	if err != nil {
		return nil, err
	}

	result := &ExecutionResult{}
	if v, ok := respData["exitCode"].(float64); ok {
		result.ExitCode = int(v)
	}
	if v, ok := respData["stdout"].(string); ok {
		result.Stdout = v
	}
	if v, ok := respData["stderr"].(string); ok {
		result.Stderr = v
	}
	return result, nil
}

// ReadFile reads a file from a VM.
func (c *Client) ReadFile(ctx context.Context, vmID string, path string) (string, error) {
	resp, err := c.doRequest(ctx, "GET", "/v1/vms/"+vmID+"/files?path="+url.QueryEscape(path), nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// WriteFile writes content to a file in a VM.
func (c *Client) WriteFile(ctx context.Context, vmID string, path string, content []byte) error {
	resp, err := c.doRequest(ctx, "PUT", "/v1/vms/"+vmID+"/files?path="+url.QueryEscape(path), bytes.NewReader(content))
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// ListVMs returns all VMs.
func (c *Client) ListVMs(ctx context.Context) ([]map[string]interface{}, error) {
	resp, err := c.doRequest(ctx, "GET", "/v1/vms", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result []map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return result, nil
}

// ListFiles lists files in a directory in a VM.
func (c *Client) ListFiles(ctx context.Context, vmID string, path string) ([]string, error) {
	resp, err := c.doRequest(ctx, "GET", "/v1/vms/"+vmID+"/files/list?path="+url.QueryEscape(path), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var entries []struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name
	}
	return names, nil
}

// --- HTTP helpers ---

func (c *Client) postJSON(ctx context.Context, path string, body interface{}) (map[string]interface{}, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	resp, err := c.doRequest(ctx, "POST", path, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return result, nil
}

func (c *Client) getJSON(ctx context.Context, path string) (map[string]interface{}, error) {
	resp, err := c.doRequest(ctx, "GET", path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return result, nil
}

func (c *Client) doRequest(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, err
	}

	if body != nil && method != "GET" {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.apiKey != "" {
		req.Header.Set("X-API-KEY", c.apiKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, path, err)
	}

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("%s %s returned %d: %s", method, path, resp.StatusCode, string(body))
	}

	return resp, nil
}
