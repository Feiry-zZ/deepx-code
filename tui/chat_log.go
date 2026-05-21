package tui

import "strings"

// maxChatBytes 是 chat 显示缓冲的字节预算(只控制显示用 raw,不影响 m.history)。
// 超过后从最旧的 segment 整段丢弃,保留尾部 segment(永不裁尾 — 当前轮内容必须留住)。
// 16KB ≈ 5K 中文字符 / 16K ASCII 字符,约 3-5 屏滚动回看。
// 真正完整的上下文在 m.history(走 LLM)和 session.gob(走重启恢复),
// 这里只管"你眼睛能滚回去看几屏",超出的旧消息按 rebuildChatFromHistory 的段粒度裁。
const maxChatBytes = 16 * 1024

// chatSegment 是 chatLog 的一格,raw 是 markdown 源,ansi 是按 ansiWidth 渲染后的缓存。
// 任何对 raw 的修改都要把 ansi 置空以触发重渲。
type chatSegment struct {
	raw       string
	ansi      string
	ansiWidth int
}

// chatLog 是显示区的分段日志:替代单一 strings.Builder。
// 设计要点:
//  1. 头部按字节预算裁剪 — O(段数),不复制尾部
//  2. 渲染按 segment 缓存 ANSI — 流式期间只重渲最后一段
//  3. 段边界天然隔开 markdown 上下文 (fence/列表/表格不跨段)
type chatLog struct {
	segments   []*chatSegment
	totalBytes int
	maxBytes   int
}

func newChatLog(maxBytes int) *chatLog {
	return &chatLog{maxBytes: maxBytes}
}

// Open 起一段新 segment,把 initial 作为初始内容。后续 Append 会继续追加到这段尾部,
// 直到下一次 Open。返回不需要使用,主要语义是"段边界"。
func (cl *chatLog) Open(initial string) {
	cl.segments = append(cl.segments, &chatSegment{raw: initial})
	cl.totalBytes += len(initial)
	cl.trim()
}

// Append 追加到最后一段。如果还没有任何段(冷启动),自动 Open 一段。
// 任何 Append 都会清掉这段的 ansi 缓存,下次 Render 触发重渲。
func (cl *chatLog) Append(text string) {
	if text == "" {
		return
	}
	if len(cl.segments) == 0 {
		cl.Open(text)
		return
	}
	seg := cl.segments[len(cl.segments)-1]
	seg.raw += text
	seg.ansi = ""
	cl.totalBytes += len(text)
	cl.trim()
}

// trim 从头部丢弃 segment 直到 totalBytes <= maxBytes。
// 永远保留至少一段 — 否则在写大消息时尾部会被自己挤掉。
func (cl *chatLog) trim() {
	if cl.maxBytes <= 0 {
		return
	}
	for len(cl.segments) > 1 && cl.totalBytes > cl.maxBytes {
		seg := cl.segments[0]
		cl.totalBytes -= len(seg.raw)
		cl.segments = cl.segments[1:]
	}
}

// Len 返回所有段的 raw 字节总和。
func (cl *chatLog) Len() int { return cl.totalBytes }

// String 拼接所有段的 raw,用于选区抠文本之类需要原文的场景。
func (cl *chatLog) String() string {
	if len(cl.segments) == 0 {
		return ""
	}
	var b strings.Builder
	b.Grow(cl.totalBytes)
	for _, s := range cl.segments {
		b.WriteString(s.raw)
	}
	return b.String()
}

// Reset 清空所有段(用于会话压缩或重置场景)。
func (cl *chatLog) Reset() {
	cl.segments = nil
	cl.totalBytes = 0
}

// EndsWithNewline 报告最后一段是否以 \n 结尾。
// 用于 ToolCallStart 那种"决定要不要补一个换行"的边界判断,避免每次 String() 拷贝全量。
func (cl *chatLog) EndsWithNewline() bool {
	if len(cl.segments) == 0 {
		return false
	}
	raw := cl.segments[len(cl.segments)-1].raw
	return len(raw) > 0 && raw[len(raw)-1] == '\n'
}

// Render 把所有段渲染为 ANSI 并拼接。render 是单段渲染函数(width 同步传入),
// 命中 ansiWidth==width 的缓存直接复用,否则调 render 后写回缓存。
func (cl *chatLog) Render(width int, render func(string, int) string) string {
	if width <= 0 || len(cl.segments) == 0 {
		return ""
	}
	var b strings.Builder
	for _, s := range cl.segments {
		if s.ansi == "" || s.ansiWidth != width {
			s.ansi = render(s.raw, width)
			s.ansiWidth = width
		}
		b.WriteString(s.ansi)
	}
	return b.String()
}
