package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// TestQuoteBarVisual 模拟一段典型对话流(user / assistant / tools 组 / assistant),
// 输出整段渲染结果给开发者目视检查色条形状是否正确。
// 不做严格断言 — 视觉调整阶段用,定型后可考虑改成 golden file 对比。
func TestQuoteBarVisual(t *testing.T) {
	cl := newChatLog(0)

	cl.Open(kindUser, "帮我重构一下 model.go,把工具调用归到独立段。")

	cl.Open(kindAssistant, "好的,我先看一下当前结构。\n\n这个文件里现在 ToolCallStartMsg 直接 Append 到 assistant 段,需要切到独立的 tools 段。")

	cl.Open(kindTools, "📄 Read (model.go)\n🔍 Grep (refreshViewport)\n📝 Update (model.go)\n")

	cl.Open(kindAssistant, "重构已完成。主要做了三件事:\n- 拆出 messageGroup 类型\n- Update 走 dispatch 表\n- 渲染按 segment 缓存")

	cl.Open(kindSystem, "_(已恢复完整会话)_")

	m := &model{}
	const viewportW = 60
	rendered := cl.Render(viewportW, func(raw, kind string, width int) string {
		var inner string
		if kind == kindTools {
			inner = raw
		} else {
			inner = m.renderMarkdown(raw, barInnerWidth(width, kind))
		}
		inner = strings.TrimRight(inner, "\n")
		return applyQuoteBar(inner, kind)
	})

	// 替换 ANSI 转义成可见字符,方便在测试日志里看
	visible := strings.ReplaceAll(rendered, "\x1b", "\\e")
	t.Logf("\n=== 渲染结果 (width=%d) ===\n%s\n=== 实际带色版 ===\n%s\n=== 结束 ===\n",
		viewportW, visible, rendered)

	// 基本不变量:一级 ┃ + 二级 │ 都得出现
	stripped := ansi.Strip(rendered)
	for _, want := range []string{"┃", "│"} {
		if !strings.Contains(stripped, want) {
			t.Errorf("missing bar char %q", want)
		}
	}
	// tools 段必须缩进(strip ANSI 之后行内出现 "  │ ")
	if !strings.Contains(stripped, "  │ ") {
		t.Errorf("tools segment should be indented with 2 spaces before │")
	}
}
