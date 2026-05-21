package tui

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// === deepx 文字 banner(给右栏顶部用)===
//
// 5 行布局:
//   - 顶 `/` 装饰条
//   - 3 行 5×3 block art "deepx",每个字母粉→紫渐变
//   - 底 `/` 装饰条
//
// 之前还有一行 "deepx code™" 标签,跟 chat 区右栏顶部的文字 logo 重复,这里只留 art。
const (
	bannerArtRows  = 3
	bannerArtWidth = 3*5 + 4 // 5 字母 × 3 列 + 4 字母间空格 = 19
	bannerMinWidth = bannerArtWidth
)

// deepxLetters 5 个字母的 3×3 像素艺术。
var deepxLetters = [5][bannerArtRows]string{
	{"█▀▄", "█ █", "▀▀ "}, // d
	{"█▀▀", "█▀▀", "▀▀▀"}, // e
	{"█▀▀", "█▀▀", "▀▀▀"}, // e
	{"█▀▄", "█▀▀", "█  "}, // p
	{"█ █", "▀▄▀", "▀ ▀"}, // x
}

// deepxLetterColors 每个字母一色,组成粉→紫渐变。ANSI 256 调色板等距取色,跨终端稳。
var deepxLetterColors = [5]color.Color{
	lipgloss.Color("213"), // 亮粉
	lipgloss.Color("177"), // 粉紫
	lipgloss.Color("141"), // 中紫
	lipgloss.Color("105"), // 蓝紫
	lipgloss.Color("99"),  // 深紫
}

// bannerDecoColor 上下 `/` 修饰条颜色。
var bannerDecoColor color.Color = lipgloss.Color("63")

// renderBanner 返回 5 行 × width 列的 banner。width < bannerMinWidth 时返回空。
func renderBanner(width int) string {
	if width < bannerMinWidth {
		return ""
	}

	deco := lipgloss.NewStyle().
		Foreground(bannerDecoColor).
		Render(strings.Repeat("/", width))

	leftPad := (width - bannerArtWidth) / 2
	if leftPad < 0 {
		leftPad = 0
	}
	padStr := strings.Repeat(" ", leftPad)

	rows := make([]string, 0, 5)
	rows = append(rows, deco)
	for r := range bannerArtRows {
		var sb strings.Builder
		sb.WriteString(padStr)
		for i, letter := range deepxLetters {
			if i > 0 {
				sb.WriteByte(' ')
			}
			sb.WriteString(lipgloss.NewStyle().
				Foreground(deepxLetterColors[i]).
				Render(letter[r]))
		}
		raw := sb.String()
		if cur := ansi.StringWidth(raw); cur < width {
			raw += strings.Repeat(" ", width-cur)
		}
		rows = append(rows, raw)
	}
	rows = append(rows, deco)
	return strings.Join(rows, "\n")
}
