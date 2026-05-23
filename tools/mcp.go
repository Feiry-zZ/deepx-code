package tools

import "sync"

// MCP 工具是运行时由 mcp 包动态注入的(连接到外部 MCP server 后),跟静态的 Tools 分开存,
// 用读写锁保护(连接/断开在后台 goroutine 改,agent 读)。
var (
	mcpMu    sync.RWMutex
	mcpTools []Tool
)

// SetMCPTools 替换当前 MCP 注入的工具集(MCP manager 连接/断开/刷新后调用)。
func SetMCPTools(ts []Tool) {
	mcpMu.Lock()
	mcpTools = ts
	mcpMu.Unlock()
}

// MCPTools 返回当前 MCP 工具快照(给 agent 拼工具表用)。
func MCPTools() []Tool {
	mcpMu.RLock()
	defer mcpMu.RUnlock()
	return append([]Tool(nil), mcpTools...)
}

// findMCPTool 按名查 MCP 工具,找不到返回 nil。供 Find 兜底。
func findMCPTool(name string) *Tool {
	mcpMu.RLock()
	defer mcpMu.RUnlock()
	for i := range mcpTools {
		if mcpTools[i].Name == name {
			t := mcpTools[i]
			return &t
		}
	}
	return nil
}
