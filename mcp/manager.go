package mcp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"deepx/tools"
)

// ServerConfig 是一个 MCP server 的配置。URL 非空 = http(Streamable HTTP)传输;
// 否则 = stdio(用 Command/Args/Env 拉子进程)。Headers 给 http 传输带认证头等。
type ServerConfig struct {
	Name    string            `json:"name"`
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

// configFile 是 MCP server 配置的落盘位置:~/.deepx/mcp.json。
func configFile() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".deepx", "mcp.json"), nil
}

type configDoc struct {
	Servers []ServerConfig `json:"servers"`
}

// LoadConfig 读取 ~/.deepx/mcp.json;文件不存在返回空列表(非错误)。
func LoadConfig() ([]ServerConfig, error) {
	path, err := configFile()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var doc configDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("mcp.json 解析失败: %w", err)
	}
	return doc.Servers, nil
}

// SaveConfig 原子写回 ~/.deepx/mcp.json。
func SaveConfig(servers []ServerConfig) error {
	path, err := configFile()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(configDoc{Servers: servers}, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// AddServer 把一个 server 加进配置(同名覆盖)并保存。
func AddServer(s ServerConfig) error {
	servers, err := LoadConfig()
	if err != nil {
		return err
	}
	replaced := false
	for i := range servers {
		if servers[i].Name == s.Name {
			servers[i] = s
			replaced = true
			break
		}
	}
	if !replaced {
		servers = append(servers, s)
	}
	return SaveConfig(servers)
}

// DeleteServer 按名删除并保存;返回是否删到。
func DeleteServer(name string) (bool, error) {
	servers, err := LoadConfig()
	if err != nil {
		return false, err
	}
	out := servers[:0]
	found := false
	for _, s := range servers {
		if s.Name == name {
			found = true
			continue
		}
		out = append(out, s)
	}
	if !found {
		return false, nil
	}
	return true, SaveConfig(out)
}

// ServerStatus 是某个 server 的连接状态(给 /mcp-list 展示)。
type ServerStatus struct {
	Name      string
	Connected bool
	ToolCount int
	Err       string
}

// Manager 管理所有已连接的 MCP server,并把它们的工具汇成 []tools.Tool 注入给 LLM。
type Manager struct {
	mu      sync.Mutex
	clients map[string]*Client
	status  map[string]ServerStatus
}

// NewManager 新建管理器(尚未连接)。
func NewManager() *Manager {
	return &Manager{clients: map[string]*Client{}, status: map[string]ServerStatus{}}
}

// ConnectAll 后台连接配置里的所有 server,连完刷新注入给 LLM 的工具集。
// 不阻塞调用方;每个 server 独立连接,失败只记状态、不影响其它。
func (m *Manager) ConnectAll() {
	servers, err := LoadConfig()
	if err != nil || len(servers) == 0 {
		return
	}
	go func() {
		var wg sync.WaitGroup
		for _, s := range servers {
			wg.Add(1)
			go func(s ServerConfig) {
				defer wg.Done()
				m.connectOne(s)
			}(s)
		}
		wg.Wait()
		m.refreshTools()
	}()
}

// Connect 连接单个 server 并立即刷新工具集(供 mcp-add 之后即时生效)。
func (m *Manager) Connect(s ServerConfig) error {
	err := m.connectOne(s)
	m.refreshTools()
	return err
}

// Disconnect 断开并移除一个 server,刷新工具集(供 mcp-delete 用)。
func (m *Manager) Disconnect(name string) {
	m.mu.Lock()
	if c := m.clients[name]; c != nil {
		c.Close()
		delete(m.clients, name)
	}
	delete(m.status, name)
	m.mu.Unlock()
	m.refreshTools()
}

func (m *Manager) connectOne(s ServerConfig) error {
	c, err := Connect(s)
	if err != nil {
		m.mu.Lock()
		m.status[s.Name] = ServerStatus{Name: s.Name, Connected: false, Err: err.Error()}
		m.mu.Unlock()
		return err
	}
	defs, err := c.ListTools()
	if err != nil {
		c.Close()
		m.mu.Lock()
		m.status[s.Name] = ServerStatus{Name: s.Name, Connected: false, Err: "tools/list 失败: " + err.Error()}
		m.mu.Unlock()
		return err
	}
	m.mu.Lock()
	if old := m.clients[s.Name]; old != nil {
		old.Close()
	}
	m.clients[s.Name] = c
	m.status[s.Name] = ServerStatus{Name: s.Name, Connected: true, ToolCount: len(defs)}
	m.mu.Unlock()
	return nil
}

// Status 返回所有 server 的状态快照(已连接的 + 配置里连接失败的)。
func (m *Manager) Status() []ServerStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]ServerStatus, 0, len(m.status))
	for _, st := range m.status {
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// refreshTools 把所有已连接 server 的工具汇成 []tools.Tool 注入 tools 包。
// 工具名 mcp__<server>__<tool>,Executor 闭包转发到对应 client。
func (m *Manager) refreshTools() {
	m.mu.Lock()
	type entry struct {
		client *Client
		server string
		defs   []ToolDef
	}
	var entries []entry
	for name, c := range m.clients {
		defs, err := c.ListTools()
		if err != nil {
			continue
		}
		entries = append(entries, entry{c, name, defs})
	}
	m.mu.Unlock()

	var out []tools.Tool
	for _, e := range entries {
		for _, d := range e.defs {
			client := e.client
			server := e.server
			toolName := d.Name
			params := schemaToToolParam(d.InputSchema)
			out = append(out, tools.Tool{
				Name:        "mcp__" + server + "__" + toolName,
				Description: fmt.Sprintf("[MCP:%s] %s", server, d.Description),
				Parameters:  params,
				ReadOnly:    false, // MCP 工具行为未知,保守当作可写(review 模式会拦)
				Executor: func(args map[string]any) tools.ToolResult {
					text, err := client.CallTool(toolName, args)
					if err != nil {
						if text != "" {
							return tools.ToolResult{Output: text + "\n(" + err.Error() + ")", Success: false}
						}
						return tools.ToolResult{Output: "MCP 调用失败: " + err.Error(), Success: false}
					}
					return tools.ToolResult{Output: text, Success: true}
				},
			})
		}
	}
	tools.SetMCPTools(out)
}

// schemaToToolParam 把 MCP 的 JSON Schema(inputSchema)转成 deepx 的 ToolParam。
// 只取顶层 type/properties/required;复杂嵌套原样塞进 PropDef(LLM 能读 JSON Schema)。
func schemaToToolParam(schema map[string]any) tools.ToolParam {
	p := tools.ToolParam{Type: "object", Properties: map[string]tools.PropDef{}}
	if schema == nil {
		return p
	}
	if props, ok := schema["properties"].(map[string]any); ok {
		for name, raw := range props {
			pd := tools.PropDef{Type: "string"}
			if m, ok := raw.(map[string]any); ok {
				if t, ok := m["type"].(string); ok {
					pd.Type = t
				}
				if desc, ok := m["description"].(string); ok {
					pd.Description = desc
				}
				if items, ok := m["items"].(map[string]any); ok {
					pd.Items = items
				}
			}
			p.Properties[name] = pd
		}
	}
	if req, ok := schema["required"].([]any); ok {
		for _, r := range req {
			if s, ok := r.(string); ok {
				p.Required = append(p.Required, s)
			}
		}
	}
	return p
}
