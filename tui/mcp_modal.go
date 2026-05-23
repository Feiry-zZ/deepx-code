package tui

import (
	"fmt"
	"strings"

	"deepx/mcp"

	"charm.land/lipgloss/v2"
)

// mcpListMessage 给 /mcp-list:列出配置里的 MCP server 及连接状态(已连接/工具数/错误)。
func (m *model) mcpListMessage() string {
	servers, err := mcp.LoadConfig()
	if err != nil {
		return "读取 MCP 配置失败: " + err.Error()
	}
	if len(servers) == 0 {
		return "未配置任何 MCP server。用 `/mcp-add` 添加。"
	}
	statusByName := map[string]mcp.ServerStatus{}
	if m.mcpMgr != nil {
		for _, st := range m.mcpMgr.Status() {
			statusByName[st.Name] = st
		}
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "MCP server(%d):\n\n", len(servers))
	for _, s := range servers {
		cmd := s.URL // http 传输显示 URL
		if cmd == "" {
			cmd = s.Command
			if len(s.Args) > 0 {
				cmd += " " + strings.Join(s.Args, " ")
			}
		}
		st, ok := statusByName[s.Name]
		state := "连接中…"
		switch {
		case ok && st.Connected:
			state = fmt.Sprintf("✓ 已连接(%d 个工具)", st.ToolCount)
		case ok && st.Err != "":
			state = "✗ " + st.Err
		}
		fmt.Fprintf(&sb, "- %s  %s\n  %s\n", s.Name, state, cmd)
	}
	return sb.String()
}

// openMcpAddModal 给 /mcp-add:弹单行输入框(格式 "名称 命令 [参数...]")。
func (m *model) openMcpAddModal() {
	m.showMcpAdd = true
	m.mcpAddErr = ""
	m.mcpAddInput.SetValue("")
	m.mcpAddInput.Focus()
	m.input.Blur()
}

// submitMcpAdd 解析输入、保存配置、后台连接。成功关弹窗;失败留 mcpAddErr 等重试。
func (m *model) submitMcpAdd() {
	fields := strings.Fields(m.mcpAddInput.Value())
	if len(fields) < 2 {
		m.mcpAddErr = "格式:名称 命令 [参数...] 或 名称 https://...(至少两段)"
		return
	}
	cfg := mcp.ServerConfig{Name: fields[0]}
	if strings.HasPrefix(fields[1], "http://") || strings.HasPrefix(fields[1], "https://") {
		cfg.URL = fields[1] // http(Streamable HTTP)传输
	} else {
		cfg.Command = fields[1] // stdio 传输
		cfg.Args = fields[2:]
	}
	if err := mcp.AddServer(cfg); err != nil {
		m.mcpAddErr = "保存失败: " + err.Error()
		return
	}
	// 后台连接(握手可能耗时,别阻塞 UI);结果可用 /mcp-list 查看。
	if m.mcpMgr != nil {
		go m.mcpMgr.Connect(cfg)
	}
	m.showMcpAdd = false
	m.mcpAddErr = ""
	m.mcpAddInput.Blur()
	m.input.Focus()
	m.appendChat("System", fmt.Sprintf("已添加 MCP server「%s」,后台连接中。用 /mcp-list 查看状态。", cfg.Name))
}

// openMcpDeleteModal 给 /mcp-delete:载入 server 名列表,弹选择删除。无配置则直接提示。
func (m *model) openMcpDeleteModal() {
	servers, err := mcp.LoadConfig()
	if err != nil {
		m.appendChat("System", "读取 MCP 配置失败: "+err.Error())
		return
	}
	if len(servers) == 0 {
		m.appendChat("System", "没有可删除的 MCP server。")
		return
	}
	m.mcpDelNames = m.mcpDelNames[:0]
	for _, s := range servers {
		m.mcpDelNames = append(m.mcpDelNames, s.Name)
	}
	m.mcpDelIdx = 0
	m.showMcpDelete = true
	m.input.Blur()
}

// submitMcpDelete 删除当前选中的 server(配置 + 断开连接)。
func (m *model) submitMcpDelete() {
	if m.mcpDelIdx < 0 || m.mcpDelIdx >= len(m.mcpDelNames) {
		m.showMcpDelete = false
		return
	}
	name := m.mcpDelNames[m.mcpDelIdx]
	if _, err := mcp.DeleteServer(name); err != nil {
		m.appendChat("System", "删除失败: "+err.Error())
	} else {
		if m.mcpMgr != nil {
			m.mcpMgr.Disconnect(name)
		}
		m.appendChat("System", fmt.Sprintf("已删除 MCP server「%s」。", name))
	}
	m.showMcpDelete = false
	m.input.Focus()
}

// mcpAddModalBlock 渲染添加弹窗。
func (m model) mcpAddModalBlock() string {
	title := lipgloss.NewStyle().Bold(true).Foreground(highlightColor).Render("添加 MCP Server")
	hint := lipgloss.NewStyle().Foreground(subtleColor).Render(
		"本地(stdio):名称 命令 [参数...]\n  例:fs npx -y @modelcontextprotocol/server-filesystem /path\n远程(http):名称 URL\n  例:remote https://example.com/mcp")
	inputBlock := lipgloss.NewStyle().Foreground(dimColor).Render("配置:") + "\n  " + m.mcpAddInput.View()

	parts := []string{title, "", hint, "", inputBlock}
	if m.mcpAddErr != "" {
		parts = append(parts, "", lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Render("✗ "+m.mcpAddErr))
	}
	parts = append(parts, "", lipgloss.NewStyle().Foreground(dimColor).Render("Enter 保存并连接 · Esc 取消"))
	content := lipgloss.JoinVertical(lipgloss.Left, parts...)

	w := 64
	if maxW := m.width - 4; w > maxW {
		w = maxW
	}
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(highlightColor).Padding(1, 2).Width(w).Render(content)
}

// mcpDeleteModalBlock 渲染删除选择弹窗。
func (m model) mcpDeleteModalBlock() string {
	title := lipgloss.NewStyle().Bold(true).Foreground(highlightColor).Render("删除 MCP Server")
	rows := make([]string, 0, len(m.mcpDelNames))
	for i, name := range m.mcpDelNames {
		marker := "  "
		style := lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
		if i == m.mcpDelIdx {
			marker = "▸ "
			style = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("9")).Background(lipgloss.Color("236"))
		}
		rows = append(rows, style.Render(marker+name))
	}
	footer := lipgloss.NewStyle().Foreground(dimColor).Render("↑/↓ 选择 · Enter 删除 · Esc 取消")
	parts := append([]string{title, ""}, rows...)
	parts = append(parts, "", footer)
	content := lipgloss.JoinVertical(lipgloss.Left, parts...)

	w := 50
	if maxW := m.width - 4; w > maxW {
		w = maxW
	}
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(highlightColor).Padding(1, 2).Width(w).Render(content)
}
