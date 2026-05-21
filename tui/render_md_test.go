package tui

import (
	"deepx/agent"
	"deepx/session"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// TestRenderMarkdownTable 验证 GFM table 经 glamour 渲染:
// - 有 ANSI 转义(说明 glamour 起作用了,不是 raw passthrough)
// - 表头/单元格内容字面出现
// - 至少一个 box-drawing 字符出现(glamour table 用 │ ─ ┼ 等)
func TestRenderMarkdownTable(t *testing.T) {
	sample := `| 语法     | 渲染     | 备注      |
|:---------|:--------:|----------:|
| **bold** | 粗体     | 行首加粗  |
| ` + "`code`" + ` | 黄色 | inline    |
| *em*     | 斜体     | 单星号    |

over.`
	m := &model{}
	out := m.renderMarkdown(sample, 80)
	if !strings.Contains(out, "\x1b[") {
		t.Fatal("no ANSI escape in glamour output")
	}
	plain := ansi.Strip(out)
	for _, want := range []string{"语法", "渲染", "粗体", "斜体", "over."} {
		if !strings.Contains(plain, want) {
			t.Errorf("plain output missing %q:\n%s", want, plain)
		}
	}
	hasBox := false
	for _, c := range []string{"│", "─", "┼", "┌", "└"} {
		if strings.Contains(plain, c) {
			hasBox = true
			break
		}
	}
	if !hasBox {
		t.Errorf("table missing any box-drawing char:\n%s", plain)
	}
}

// TestRebuildChatFromHistorySegments 回归:恢复路径必须按消息逐段 Open,
// 而不是把全部历史塞进单段。
func TestRebuildChatFromHistorySegments(t *testing.T) {
	hist := []agent.ChatMessage{
		{Role: "user", Content: strings.Repeat("a", 40)},
		{Role: "assistant", Content: strings.Repeat("b", 40)},
		{Role: "user", Content: strings.Repeat("c", 40)},
	}

	cl := newChatLog(200)
	rebuildChatFromHistory(cl, hist)
	if got := len(cl.segments); got != 3 {
		t.Fatalf("restore segments = %d, want 3 (one Open per message)", got)
	}

	cl.Open(kindUser, "新消息\n")
	cl.Open(kindAssistant, "")

	full := cl.String()
	if !strings.Contains(full, strings.Repeat("b", 40)) &&
		!strings.Contains(full, strings.Repeat("c", 40)) {
		t.Errorf("all history was trimmed after first new turn — regression of single-segment restore bug.\nfull=%q", full)
	}
	if !strings.Contains(full, "新消息") {
		t.Errorf("new user message missing from chat log:\n%q", full)
	}
}

// TestRebuildChatFromHistorySkipsToolResults 回归:tool role 消息必须跳过。
func TestRebuildChatFromHistorySkipsToolResults(t *testing.T) {
	hist := []agent.ChatMessage{
		{Role: "assistant", ToolCalls: []agent.ToolCall{
			{Function: agent.ToolCallFunc{Name: "Bash", Arguments: `{"command":"ls"}`}},
		}},
		{Role: "tool", Name: "Bash", Content: "file1\nfile2"},
	}

	cl := newChatLog(0)
	rebuildChatFromHistory(cl, hist)

	out := cl.String()
	if !strings.Contains(out, "Bash (ls)") {
		t.Errorf("missing call line %q:\n%s", "Bash (ls)", out)
	}
	if strings.Contains(out, "✓ Bash") {
		t.Errorf("found redundant '✓ Bash' result line — restore should skip tool messages:\n%s", out)
	}
}

// TestRenderMarkdownDiff 验证 diff fence 经 glamour + chroma 处理后有色。
// 不再写死 ANSI 序列(主题切换会变),只要求 strip 后含 diff 行字面 + 有 ANSI 转义。
func TestRenderMarkdownDiff(t *testing.T) {
	sample := "~~~diff\n- old line\n+ new line\n@@ hunk @@\n context line\n~~~\n"
	m := &model{}
	out := m.renderMarkdown(sample, 200)
	if !strings.Contains(out, "\x1b[") {
		t.Errorf("no ANSI escape in diff fence output")
	}
	plain := ansi.Strip(out)
	for _, want := range []string{"old line", "new line", "hunk", "context line"} {
		if !strings.Contains(plain, want) {
			t.Errorf("diff output missing %q:\n%s", want, plain)
		}
	}
}

// TestRenderMarkdownGobRestore 用真实 history.gob 跑全量渲染,验证段内独立渲染时
// 未闭合 fence 不会跨段污染(chatLog.Render 已经按段独立调 renderMarkdown,自然天隔)。
func TestRenderMarkdownGobRestore(t *testing.T) {
	sess, err := session.New("/Users/solly/data/develop/github/deepx")
	if err != nil {
		t.Skipf("no session: %v", err)
	}
	var hist []agent.ChatMessage
	if err := sess.LoadGob("history.gob", &hist); err != nil || len(hist) == 0 {
		t.Skipf("no gob: %v", err)
	}
	cl := newChatLog(0)
	rebuildChatFromHistory(cl, hist)

	m := &model{}
	for _, s := range cl.segments {
		out := m.renderMarkdown(s.raw, 170)
		// 不强求每段都有 ANSI(工具调用行可能只是文本无 markdown 标记),
		// 但渲染不能 panic 且不能返回空。
		if out == "" && s.raw != "" {
			t.Errorf("non-empty segment rendered empty: kind=%s raw=%q", s.kind, s.raw[:min(len(s.raw), 60)])
		}
	}
}

