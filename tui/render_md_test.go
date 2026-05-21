package tui

import (
	"deepx/agent"
	"deepx/session"
	"strings"
	"testing"
)

// TestRenderMarkdownTable 验证 GFM table 渲染:边框、对齐、cell 内 inline markdown。
func TestRenderMarkdownTable(t *testing.T) {
	sample := `**🐋 deepx**: 看下表

| 语法     | 渲染     | 备注      |
|:---------|:--------:|----------:|
| **bold** | 粗体     | 行首加粗  |
| ` + "`code`" + ` | 黄色 | inline    |
| *em*     | 斜体     | 单星号    |

over.`
	m := &model{}
	out := m.renderMarkdown(sample, 80)
	visible := strings.ReplaceAll(out, "\x1b", "ESC")
	t.Log("\n" + visible)
	if !strings.Contains(out, "┌") || !strings.Contains(out, "└") || !strings.Contains(out, "│") {
		t.Fatal("table borders missing")
	}
	if strings.Contains(out, "**bold**") {
		t.Fatal("bold marker still literal inside table cell")
	}
}

// TestRebuildChatFromHistorySegments 回归:恢复路径必须按消息逐段 Open,
// 而不是把全部历史塞进单段。否则首次新消息一开新段,整段历史会被 trim 当
// 最旧段整体丢掉(用户现象:重启看得到历史,发一条新消息历史全没了)。
func TestRebuildChatFromHistorySegments(t *testing.T) {
	// 构造 3 条历史消息,小预算确保新增段时一定触发裁剪。
	hist := []agent.ChatMessage{
		{Role: "user", Content: strings.Repeat("a", 40)},
		{Role: "assistant", Content: strings.Repeat("b", 40)},
		{Role: "user", Content: strings.Repeat("c", 40)},
	}

	cl := newChatLog(200) // 3 段各 ~60B = ~180B 全装得下,够留余地
	rebuildChatFromHistory(cl, hist)
	if got := len(cl.segments); got != 3 {
		t.Fatalf("restore segments = %d, want 3 (one Open per message)", got)
	}

	// 模拟用户发首条新消息 + deepx 起手 prefix —— 跟 model.go 入口路径一致。
	cl.Open("**👤 You**: 新消息\n\n")
	cl.Open("**🐋 deepx**: ")

	// 核心断言:最旧的 user 消息(40 个 'a')可能被 trim 丢掉,
	// 但**至少有一条**历史消息(40 个 'b' 或 'c')应当留下。
	// 旧实现(单段)在这里整个历史会消失。
	full := cl.String()
	if !strings.Contains(full, strings.Repeat("b", 40)) &&
		!strings.Contains(full, strings.Repeat("c", 40)) {
		t.Errorf("all history was trimmed after first new turn — regression of single-segment restore bug.\nfull=%q", full)
	}
	if !strings.Contains(full, "新消息") {
		t.Errorf("new user message missing from chat log:\n%q", full)
	}
}

// TestRenderMarkdownDiffFence 验证 ~~~diff 块按 -/+/@@ 前缀分别上色,
// 普通 ~~~ 块(无 infostring)仍然整体 dim,不应该出现红绿。
func TestRenderMarkdownDiffFence(t *testing.T) {
	const (
		// lipgloss 把 0–15 号色压成 ANSI 短形式 SGR(bright 区间 90–97),
		// 而 240 这种 >15 的色号才走 256-color 形式 `\x1b[38;5;Nm`。
		red   = "\x1b[91m"
		green = "\x1b[92m"
		cyan  = "\x1b[96m"
		dim   = "\x1b[38;5;240m"
	)

	sample := "before\n\n~~~diff\n- old line\n+ new line\n@@ hunk @@\n context line\n~~~\n\nafter"
	m := &model{}
	out := m.renderMarkdown(sample, 200)
	if !strings.Contains(out, red+"- old line") {
		t.Errorf("`- old line` not red:\n%q", out)
	}
	if !strings.Contains(out, green+"+ new line") {
		t.Errorf("`+ new line` not green:\n%q", out)
	}
	if !strings.Contains(out, cyan+"@@ hunk @@") {
		t.Errorf("`@@ hunk @@` not cyan:\n%q", out)
	}
	// context 行(无前缀)走 fallback dim
	if !strings.Contains(out, dim+" context line") {
		t.Errorf("context line not dim:\n%q", out)
	}
	// fence 行本身 dim,不参与上色
	if !strings.Contains(out, dim+"~~~diff") {
		t.Errorf("opening fence not dim:\n%q", out)
	}

	// 对照:普通 ~~~ 块(无 diff infostring)的 -/+ 应该走 dim,不是红绿
	plain := "~~~\n- not a diff\n+ also not\n~~~"
	plainOut := m.renderMarkdown(plain, 200)
	if strings.Contains(plainOut, red) || strings.Contains(plainOut, green) {
		t.Errorf("plain code fence should not get diff colors:\n%q", plainOut)
	}
}

// TestRenderMarkdownGobRestore 用真实 history.gob 跑全量渲染,验证 fence 不平衡时
// 后续 message 不再被卡在 code block 里(bold/italic/code 仍能正常渲染)。
func TestRenderMarkdownGobRestore(t *testing.T) {
	sess, err := session.New("/Users/solly/data/develop/github/deepx")
	if err != nil {
		t.Skipf("no session: %v", err)
	}
	var hist []agent.ChatMessage
	if err := sess.LoadGob("history.gob", &hist); err != nil || len(hist) == 0 {
		t.Skipf("no gob: %v", err)
	}
	// rebuildChatFromHistory 现在按消息逐段 Open;给测试一个无裁剪预算,拿到全量 raw。
	cl := newChatLog(0)
	rebuildChatFromHistory(cl, hist)
	raw := cl.String()

	m := &model{}
	rendered := m.renderMarkdown(raw, 170)

	if !strings.Contains(rendered, "\x1b[1m") {
		t.Fatalf("no bold ANSI in render output")
	}
	// 表格行不能整行被 dim — 至少要有一条 `| ` 起头的行,bold 标记被处理掉
	if strings.Contains(rendered, "| **") {
		t.Errorf("found literal '| **' in output — table row stuck in code block mode (fence reset not working)")
	}
}
