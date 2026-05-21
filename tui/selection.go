package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// cellPos 表示一个 chat 内部坐标:
//   - col  = 显示列 (0..viewport.Width-1)
//   - line = 经 ansi.Wrap 后的行号(在 chatContent 的总行集合内)
//
// 用"已 wrap 行号"而非"内容字节偏移"的好处:
//   - 用户在屏幕看到的就是 wrapped lines,鼠标拖拽方向跟它一一对应
//   - 内容只增不减(append-only),老的 line 号永远稳定
//   - 终端尺寸不变时,wrap 结果稳定;变了再用 WindowSizeMsg 清掉选区即可
type cellPos struct {
	col  int
	line int
}

// orderSel 把 anchor / end 按"流向"排好:先 line,后 col。
// 流式选择 = 文本编辑器式连续选区:从 start 一直到 end,跨多行时中间行整行入选。
// 不同于矩形/块选择 (那种是 col ∈ [min,max] × line ∈ [min,max])。
func orderSel(a, b cellPos) (start, end cellPos) {
	if a.line < b.line || (a.line == b.line && a.col <= b.col) {
		return a, b
	}
	return b, a
}

const (
	ansiReverseOn  = "\x1b[7m"
	ansiReverseOff = "\x1b[27m"
)

// selRange 计算第 i 行的高亮 / 抠字列区间 [left, right):
//   - 单行选择:[start.col, end.col+1)
//   - 首行:[start.col, width)
//   - 末行:[0, end.col+1)
//   - 中间行:[0, width)
func selRange(i int, start, end cellPos, width int) (left, right int) {
	switch {
	case i == start.line && i == end.line:
		left, right = start.col, end.col+1
	case i == start.line:
		left, right = start.col, width
	case i == end.line:
		left, right = 0, end.col+1
	default:
		left, right = 0, width
	}
	if right > width {
		right = width
	}
	if left < 0 {
		left = 0
	}
	return
}

// applySelectionHighlight 在已渲染的 chat 内容上画流式选区反色。
// width 必须等于 viewport.Width,否则 col 坐标对不上。
func applySelectionHighlight(wrapped string, a, b cellPos, width int) string {
	if width <= 0 {
		return wrapped
	}
	start, end := orderSel(a, b)

	lines := strings.Split(wrapped, "\n")
	for i := start.line; i <= end.line && i < len(lines); i++ {
		if i < 0 {
			continue
		}
		left, right := selRange(i, start, end, width)
		if left >= right {
			continue
		}
		line := lines[i]
		// pad 短行,让矩形高亮在空白处也可见(整行连续段尤其需要)
		if cur := ansi.StringWidth(line); cur < width {
			line += strings.Repeat(" ", width-cur)
		}
		pre := ansi.Cut(line, 0, left)
		mid := ansi.Cut(line, left, right)
		post := ansi.Cut(line, right, width)
		lines[i] = pre + ansiReverseOn + mid + ansiReverseOff + post
	}
	return strings.Join(lines, "\n")
}

// extractSelectionText 按流式选区抠纯文本(去 ANSI、去左侧色条前缀、trim 行尾空格)。
// 选区 left==0 时,说明这一行从首列入选,等价于"选了整行",剥掉左侧的 "┃ " / "  │ " 色条前缀
// 让剪贴板里得到纯净对话文本而不是带引用前缀的乱码。
func extractSelectionText(wrapped string, a, b cellPos, width int) string {
	if width <= 0 {
		return ""
	}
	start, end := orderSel(a, b)
	if start == end {
		return ""
	}

	lines := strings.Split(wrapped, "\n")
	var out []string
	for i := start.line; i <= end.line && i < len(lines); i++ {
		if i < 0 {
			out = append(out, "")
			continue
		}
		left, right := selRange(i, start, end, width)
		if left >= right {
			out = append(out, "")
			continue
		}
		seg := ansi.Cut(lines[i], left, right)
		seg = ansi.Strip(seg)
		if left == 0 {
			seg = stripQuoteBarPrefix(seg)
		}
		seg = strings.TrimRight(seg, " ")
		out = append(out, seg)
	}
	return strings.Join(out, "\n")
}

// stripQuoteBarPrefix 移除 applyQuoteBar 加的左侧色条前缀。
// 一级 (user/assistant/system): "┃ ";二级 (tools):"  │ "。
// 顺序很重要 — 先匹配长前缀("  │ "),再匹配 "┃ ",避免缩进二级被一级吃掉。
func stripQuoteBarPrefix(s string) string {
	for _, prefix := range []string{"  │ ", "┃ "} {
		if strings.HasPrefix(s, prefix) {
			return s[len(prefix):]
		}
	}
	return s
}
