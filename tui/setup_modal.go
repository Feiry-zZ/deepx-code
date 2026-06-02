package tui

import (
	"deepx/agent"
	"deepx/config"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// overlayCentered 把 fg(modal)叠在 bg(主 UI)上居中显示。
// 实现:
//  1. 拆 bg 和 fg 成行;算出 fg 的最大显示宽度(以 ansi.StringWidth 测,跟终端实际渲染一致)
//  2. 居中位置:startY = (height - fgHeight)/2, startX = (width - fgWidth)/2
//  3. 对每一行 fg,用 ansi.Cut 把对应 bg 行的 [startX, startX+fgW) 区间挖掉换成 fg 内容
//  4. 重新 join 输出
//
// bg 太短(行数少于 startY+fgH)时,缺失行不补,modal 会被截断。这种情况下终端高度不够,
// 不在 modal 区也没什么意义。
func overlayCentered(bg, fg string, width, height int) string {
	fgLines := strings.Split(strings.TrimRight(fg, "\n"), "\n")
	fgH := len(fgLines)
	fgW := 0
	for _, ln := range fgLines {
		if w := ansi.StringWidth(ln); w > fgW {
			fgW = w
		}
	}

	startY := (height - fgH) / 2
	startX := (width - fgW) / 2
	if startY < 0 {
		startY = 0
	}
	if startX < 0 {
		startX = 0
	}

	bgLines := strings.Split(bg, "\n")
	for i, fgLine := range fgLines {
		y := startY + i
		if y < 0 || y >= len(bgLines) {
			continue
		}
		bgLines[y] = spliceLineCells(bgLines[y], fgLine, startX, fgW)
	}
	return strings.Join(bgLines, "\n")
}

// spliceLineCells 把 fg 的所有 cell 拼到 bg 的 [atCol, atCol+fgW) 区间,
// 保留 bg 在该区间前后的内容(连同 ANSI 转义)。
// 用 ansi.Cut 处理 ANSI 边界,避免 bg 的 SGR 状态污染 fg 或 fg 之后内容。
func spliceLineCells(bg, fg string, atCol, fgW int) string {
	pre := ansi.Cut(bg, 0, atCol)
	// bg 在 atCol 之前太短 → 补空格到 atCol 列,保证 fg 起始位置对齐
	if preW := ansi.StringWidth(pre); preW < atCol {
		pre += strings.Repeat(" ", atCol-preW)
	}
	post := ""
	if bgW := ansi.StringWidth(bg); atCol+fgW < bgW {
		post = ansi.Cut(bg, atCol+fgW, bgW)
	}
	return pre + fg + post
}

// setupModalBlock 只渲染 modal 本身(不放置),供 overlay 使用。
// View() 时把这个 block 叠在 mainUI 上,所以这里不能调 lipgloss.Place 占满屏。
func (m model) setupModalBlock() string {
	title := lipgloss.NewStyle().
		Bold(true).
		Foreground(highlightColor).
		Render(T("setup.title"))

	var hint string
	if m.setupRequired {
		hint = T("setup.hint.first_run")
	} else {
		hint = T("setup.hint.reconfig")
	}
	hintBlock := lipgloss.NewStyle().Foreground(subtleColor).Render(hint)

	// 模型供应商选择器:横向列出 config.ProviderOptions,选中项高亮,←/→ 切换。
	providerLabel := lipgloss.NewStyle().Foreground(dimColor).Render(T("setup.provider_label"))
	opts := make([]string, 0, len(config.ProviderOptions))
	for i, p := range config.ProviderOptions {
		if i == m.setupProviderIdx {
			opts = append(opts, lipgloss.NewStyle().Foreground(highlightColor).Bold(true).Render("‹ "+p+" ›"))
		} else {
			opts = append(opts, lipgloss.NewStyle().Foreground(subtleColor).Render("  "+p+"  "))
		}
	}
	providerBlock := providerLabel + "\n  " + strings.Join(opts, " ")

	inputLabel := lipgloss.NewStyle().Foreground(dimColor).Render(T("setup.input_label"))
	inputBlock := inputLabel + "\n  " + m.setupInput.View()

	var errBlock string
	if m.setupErr != "" {
		errBlock = lipgloss.NewStyle().
			Foreground(lipgloss.Color("9")).
			Render("✗ " + m.setupErr)
	}

	var footer string
	if m.setupRequired {
		footer = lipgloss.NewStyle().Foreground(dimColor).Render(T("setup.footer.first_run"))
	} else {
		footer = lipgloss.NewStyle().Foreground(dimColor).Render(T("setup.footer.reconfig"))
	}

	parts := []string{title, "", hintBlock, "", providerBlock, "", inputBlock}
	if errBlock != "" {
		parts = append(parts, "", errBlock)
	}
	parts = append(parts, "", footer)

	content := lipgloss.JoinVertical(lipgloss.Left, parts...)

	modalWidth := 62
	if maxW := m.width - 4; modalWidth > maxW {
		modalWidth = maxW
	}
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(highlightColor).
		Padding(1, 2).
		Width(modalWidth).
		Render(content)
}

// submitSetup 处理 modal 内 Enter 的提交逻辑:
//   - 校验输入非空
//   - 按选中的供应商用 config.DefaultFor 构造 yaml
//   - 落盘
//   - 重新 Load(保证内存版本和磁盘一致)
//   - 把 model 内的 m.models 替换为新配置
//   - 关闭 modal,把焦点交回主输入框
//
// 失败时设置 setupErr,modal 留着等用户重试。
func (m *model) submitSetup() tea.Cmd {
	val := strings.TrimSpace(m.setupInput.Value())
	if val == "" {
		m.setupErr = T("setup.error.empty")
		return nil
	}
	provider := ""
	if m.setupProviderIdx >= 0 && m.setupProviderIdx < len(config.ProviderOptions) {
		provider = config.ProviderOptions[m.setupProviderIdx]
	}
	cfg := config.DefaultFor(provider, val) // 空/未知供应商 DefaultFor 会回退 deepseek
	if err := config.Save(cfg); err != nil {
		m.setupErr = fmt.Sprintf(T("setup.error.save"), err)
		return nil
	}
	loaded, err := config.Load()
	if err != nil {
		m.setupErr = fmt.Sprintf(T("setup.error.reload"), err)
		return nil
	}
	m.models = agent.ModelConfig{
		Flash: agent.ModelEntry(loaded.Flash),
		Pro:   agent.ModelEntry(loaded.Pro),
	}
	m.activeModelRole = "flash"
	m.activeModelID = m.models.Flash.Model
	if m.activeModelID == "" {
		m.activeModelRole = "pro"
		m.activeModelID = m.models.Pro.Model
	}
	// 模型换了:视觉能力可能变,重置 —— 先用新模型的缓存值垫初值,下面返回探测命令立刻重探。
	m.visionByModel = loadVisionCaps(m.models)
	// 重置 modal 状态
	m.showSetup = false
	m.setupRequired = false
	m.setupErr = ""
	m.setupInput.Reset()
	m.setupInput.Blur()
	m.input.Focus()

	path, _ := config.Path()
	// 反斜杠转义已在 renderMarkdown 渲染层统一处理(见 backslashSentinel),这里不必再包反引号。
	m.appendChat("System", T("setup.saved_to")+path)
	// 对新配置的模型重探视觉能力(结果经 visionCapMsg 回灌当前会话 + 覆盖缓存)。
	return tea.Batch(visionProbeCmds(m.models)...)
}

// openSetupModal 给 /config 命令用:把当前面板切到 modal,允许 Esc 取消。
func (m *model) openSetupModal() {
	m.showSetup = true
	m.setupRequired = false
	m.setupErr = ""
	m.setupInput.SetValue("")
	m.setupInput.Focus()
	m.input.Blur()
}
