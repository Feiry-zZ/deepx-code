package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

// TestHighlightSurvivesInnerReset 验证选中段套反色后,内部不再有 reset 打断反色。
// markdown / URL 渲染出来的段常带 \x1b[0m,会取消 \x1b[7m,导致选中文字不高亮。
func TestHighlightSurvivesInnerReset(t *testing.T) {
	width := 40
	// 一段带颜色 + 内部 reset 的文本(模拟 markdown 渲染)。
	line := "\x1b[38;5;12mhello\x1b[0m \x1b[38;5;9mworld\x1b[0m"
	out := applySelectionHighlight(line, cellPos{0, 0}, cellPos{width - 1, 0}, width)

	on := strings.Index(out, ansiReverseOn)
	off := strings.Index(out, ansiReverseOff)
	if on < 0 || off < 0 || off <= on {
		t.Fatalf("应有 reverseOn...reverseOff 包裹,got=%q", out)
	}
	reversed := out[on+len(ansiReverseOn) : off]
	// 反色区间内不应再出现全 reset(\x1b[0m / \x1b[m),否则反色会被取消。
	if strings.Contains(reversed, "\x1b[0m") || strings.Contains(reversed, "\x1b[m") {
		t.Fatalf("反色区间内不应含 reset,会取消高亮:%q", reversed)
	}
	// 选中的可见文字应还在(纯文本)。
	if !strings.Contains(reversed, "hello") || !strings.Contains(reversed, "world") {
		t.Fatalf("选中文字应保留:%q", reversed)
	}
}

// TestExtractSelectionURL 验证拖动选区能从带色条前缀 + ANSI 的渲染行里抠出干净的 URL。
// 模拟用户在 chat 区拖过含 URL 的一行,松手后写进剪贴板的应是纯文本 URL。
func TestExtractSelectionURL(t *testing.T) {
	width := 60
	url := "http://127.0.0.1:54321/?t=abc123"
	// 渲染一行:左侧色条前缀 "┃ " + 带样式(OSC8 超链接 + 颜色)的 URL。
	styled := lipgloss.NewStyle().Foreground(lipgloss.Color("12")).Underline(true).
		Hyperlink(url).Render(url)
	line := "┃ " + styled
	// pad 到 width,模拟 applyQuoteBar 后的整行。
	if cur := lipgloss.Width(line); cur < width {
		line += strings.Repeat(" ", width-cur)
	}

	// 整行选择:从 col 0 到行尾。
	a := cellPos{col: 0, line: 0}
	b := cellPos{col: width - 1, line: 0}
	got := extractSelectionText(line, a, b, width)
	if got != url {
		t.Fatalf("整行选择应抠出干净 URL\n got = %q\nwant = %q", got, url)
	}

	// 部分选择:只圈 URL 那一段(跳过 "┃ " 前缀的 2 列)。
	a2 := cellPos{col: 2, line: 0}
	b2 := cellPos{col: 2 + len(url) - 1, line: 0}
	got2 := extractSelectionText(line, a2, b2, width)
	if got2 != url {
		t.Fatalf("部分选择应抠出干净 URL\n got = %q\nwant = %q", got2, url)
	}
}
