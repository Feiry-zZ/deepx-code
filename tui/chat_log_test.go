package tui

import (
	"strings"
	"testing"
)

// TestChatLogTrim 验证字节预算裁剪:超过 maxBytes 时从头部丢段,
// 永远保留最后一段(即使单段大于预算)。
func TestChatLogTrim(t *testing.T) {
	cl := newChatLog(100)

	// 5 段各 30 字节 = 150 > 100,应丢前 2 段保留后 3 段(共 90 字节)。
	for range 5 {
		cl.Open(strings.Repeat("x", 30))
	}
	if got, want := len(cl.segments), 3; got != want {
		t.Errorf("segments after trim = %d, want %d", got, want)
	}
	if got := cl.Len(); got != 90 {
		t.Errorf("totalBytes = %d, want 90", got)
	}

	// 即使新段 200 字节(>预算),仍要保留这一段 — 永不裁尾。
	cl.Open(strings.Repeat("y", 200))
	if got, want := len(cl.segments), 1; got != want {
		t.Errorf("segments after huge open = %d, want %d (only the huge one kept)", got, want)
	}
	if cl.Len() != 200 {
		t.Errorf("totalBytes = %d, want 200", cl.Len())
	}
}

// TestChatLogAppendInvalidatesTailCache 验证 Append 到最后一段时清掉 ansi 缓存,
// 而前面段的缓存仍然命中。这是流式渲染只重渲尾部的关键保证。
func TestChatLogAppendInvalidatesTailCache(t *testing.T) {
	cl := newChatLog(0) // 不限预算
	cl.Open("first ")
	cl.Open("second ")

	renderCalls := 0
	render := func(raw string, _ int) string {
		renderCalls++
		return "[" + raw + "]"
	}

	if got := cl.Render(80, render); got != "[first ][second ]" {
		t.Fatalf("initial render = %q", got)
	}
	if renderCalls != 2 {
		t.Errorf("initial render calls = %d, want 2", renderCalls)
	}

	// 不变 → 全部命中缓存,render 不再被调用。
	cl.Render(80, render)
	if renderCalls != 2 {
		t.Errorf("second render calls = %d, want 2 (cache hit)", renderCalls)
	}

	// Append 只动最后一段 → 只重渲它,第一段缓存仍复用。
	cl.Append("extra")
	if got := cl.Render(80, render); got != "[first ][second extra]" {
		t.Fatalf("post-append render = %q", got)
	}
	if renderCalls != 3 {
		t.Errorf("post-append render calls = %d, want 3 (only tail re-rendered)", renderCalls)
	}

	// 宽度变化 → 全部 ansi 缓存失效,所有段重渲。
	cl.Render(40, render)
	if renderCalls != 5 {
		t.Errorf("width-change render calls = %d, want 5 (both segments re-rendered)", renderCalls)
	}
}

// TestChatLogEndsWithNewline 替代旧 strings.HasSuffix(content, "\n") 的边界判断。
func TestChatLogEndsWithNewline(t *testing.T) {
	cl := newChatLog(0)
	if cl.EndsWithNewline() {
		t.Error("empty log should not report trailing newline")
	}
	cl.Open("abc")
	if cl.EndsWithNewline() {
		t.Error("non-newline-ending segment should not report trailing newline")
	}
	cl.Append("\n")
	if !cl.EndsWithNewline() {
		t.Error("after Append(\"\\n\") should report trailing newline")
	}
}
