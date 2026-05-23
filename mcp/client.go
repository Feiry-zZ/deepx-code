// Package mcp 是 deepx 的 Model Context Protocol 客户端:把外部 MCP server 暴露的工具接入
// deepx,转发给 LLM 调用。手写 JSON-RPC,不引 SDK。
//
// 支持两种传输:
//   - stdio:server 作为子进程,经 stdin/stdout 行分隔 JSON 通信(本地 server,npx/python -m)
//   - http :Streamable HTTP,POST JSON-RPC 到一个 URL,响应可能是 application/json 或
//     text/event-stream(SSE)。覆盖远程 / 独立运行的 MCP server。
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const protocolVersion = "2024-11-05"
const requestTimeout = 30 * time.Second

// ToolDef 是 MCP server 通过 tools/list 返回的一个工具定义。
type ToolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcNotification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("mcp rpc error %d: %s", e.Code, e.Message) }

// transport 抽象 stdio / http 两种传输:都提供同步请求 + 通知 + 关闭。
type transport interface {
	call(method string, params any) (json.RawMessage, error)
	notify(method string, params any) error
	close()
}

// Client 是一个 MCP server 连接(传输无关)。
type Client struct {
	t transport
}

// Connect 按配置选传输(URL 非空走 http,否则 stdio),建立连接并完成 MCP 握手。
func Connect(cfg ServerConfig) (*Client, error) {
	var t transport
	var err error
	if cfg.URL != "" {
		t = newHTTPTransport(cfg.URL, cfg.Headers)
	} else {
		t, err = newStdioTransport(cfg.Command, cfg.Args, cfg.Env)
	}
	if err != nil {
		return nil, err
	}
	c := &Client{t: t}
	initParams := map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "deepx", "version": "1"},
	}
	if _, err := t.call("initialize", initParams); err != nil {
		c.Close()
		return nil, fmt.Errorf("MCP 握手失败: %w", err)
	}
	if err := t.notify("notifications/initialized", nil); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

// ListTools 拉取 server 暴露的工具清单。
func (c *Client) ListTools() ([]ToolDef, error) {
	raw, err := c.t.call("tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var out struct {
		Tools []ToolDef `json:"tools"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out.Tools, nil
}

// CallTool 调用 server 上的某个工具,返回拼接后的文本结果。
func (c *Client) CallTool(tool string, args map[string]any) (string, error) {
	if args == nil {
		args = map[string]any{}
	}
	raw, err := c.t.call("tools/call", map[string]any{"name": tool, "arguments": args})
	if err != nil {
		return "", err
	}
	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", err
	}
	var sb []byte
	for _, part := range out.Content {
		if part.Text != "" {
			if len(sb) > 0 {
				sb = append(sb, '\n')
			}
			sb = append(sb, part.Text...)
		}
	}
	text := string(sb)
	if out.IsError {
		return text, fmt.Errorf("MCP 工具返回错误")
	}
	return text, nil
}

// Close 关闭连接。
func (c *Client) Close() {
	if c.t != nil {
		c.t.close()
	}
}

// ============ stdio transport ============

type stdioTransport struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	enc   *json.Encoder

	mu      sync.Mutex
	nextID  int64
	pending map[int64]chan rpcResponse
	closed  bool
}

func newStdioTransport(command string, args []string, env map[string]string) (*stdioTransport, error) {
	cmd := exec.Command(command, args...)
	if len(env) > 0 {
		cmd.Env = append([]string(nil), os.Environ()...)
		for k, v := range env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("启动 MCP server 失败: %w", err)
	}
	t := &stdioTransport{cmd: cmd, stdin: stdin, enc: json.NewEncoder(stdin), pending: map[int64]chan rpcResponse{}}
	go t.readLoop(stdout)
	return t, nil
}

func (t *stdioTransport) call(method string, params any) (json.RawMessage, error) {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil, fmt.Errorf("MCP 连接已关闭")
	}
	t.nextID++
	id := t.nextID
	ch := make(chan rpcResponse, 1)
	t.pending[id] = ch
	err := t.enc.Encode(rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params})
	t.mu.Unlock()
	if err != nil {
		t.mu.Lock()
		delete(t.pending, id)
		t.mu.Unlock()
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	select {
	case resp, ok := <-ch:
		if !ok {
			return nil, fmt.Errorf("MCP 连接中断")
		}
		if resp.Error != nil {
			return nil, resp.Error
		}
		return resp.Result, nil
	case <-ctx.Done():
		t.mu.Lock()
		delete(t.pending, id)
		t.mu.Unlock()
		return nil, fmt.Errorf("MCP 请求超时(%s)", method)
	}
}

func (t *stdioTransport) notify(method string, params any) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return fmt.Errorf("MCP 连接已关闭")
	}
	return t.enc.Encode(rpcNotification{JSONRPC: "2.0", Method: method, Params: params})
}

func (t *stdioTransport) close() {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return
	}
	t.closed = true
	for _, ch := range t.pending {
		close(ch)
	}
	t.pending = nil
	t.mu.Unlock()
	_ = t.stdin.Close()
	if t.cmd.Process != nil {
		_ = t.cmd.Process.Kill()
	}
	_ = t.cmd.Wait()
}

func (t *stdioTransport) readLoop(stdout io.Reader) {
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var resp rpcResponse
		if err := json.Unmarshal(line, &resp); err != nil || resp.ID == nil {
			continue
		}
		t.mu.Lock()
		ch := t.pending[*resp.ID]
		if ch != nil {
			delete(t.pending, *resp.ID)
		}
		t.mu.Unlock()
		if ch != nil {
			ch <- resp
		}
	}
	t.close()
}

// ============ http transport(Streamable HTTP)============

type httpTransport struct {
	url     string
	headers map[string]string
	client  *http.Client

	mu      sync.Mutex
	nextID  int64
	session string // initialize 返回的 Mcp-Session-Id,后续请求带上
}

func newHTTPTransport(url string, headers map[string]string) *httpTransport {
	return &httpTransport{url: url, headers: headers, client: &http.Client{Timeout: requestTimeout}}
}

func (h *httpTransport) post(body []byte) (*http.Response, error) {
	req, err := http.NewRequest("POST", h.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	h.mu.Lock()
	session := h.session
	h.mu.Unlock()
	if session != "" {
		req.Header.Set("Mcp-Session-Id", session)
	}
	for k, v := range h.headers {
		req.Header.Set(k, v)
	}
	return h.client.Do(req)
}

func (h *httpTransport) call(method string, params any) (json.RawMessage, error) {
	h.mu.Lock()
	h.nextID++
	id := h.nextID
	h.mu.Unlock()
	body, _ := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params})
	resp, err := h.post(body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		h.mu.Lock()
		h.session = sid
		h.mu.Unlock()
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		return readSSEResponse(resp.Body, id)
	}
	var r rpcResponse
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, err
	}
	if r.Error != nil {
		return nil, r.Error
	}
	return r.Result, nil
}

func (h *httpTransport) notify(method string, params any) error {
	body, _ := json.Marshal(rpcNotification{JSONRPC: "2.0", Method: method, Params: params})
	resp, err := h.post(body)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func (h *httpTransport) close() {}

// readSSEResponse 从 SSE 流里找出 id 匹配的 JSON-RPC 响应(data: 行)。
func readSSEResponse(r io.Reader, id int64) (json.RawMessage, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(line[len("data:"):])
		if data == "" {
			continue
		}
		var resp rpcResponse
		if json.Unmarshal([]byte(data), &resp) != nil || resp.ID == nil || *resp.ID != id {
			continue
		}
		if resp.Error != nil {
			return nil, resp.Error
		}
		return resp.Result, nil
	}
	return nil, fmt.Errorf("SSE 流未返回 id=%d 的响应", id)
}
