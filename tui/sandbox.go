package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

// docker 镜像拉取的 TUI 侧:不显示百分比(非 TTY 下 docker 不吐字节进度,百分比会一直 0%),
// 只在对话区显示"🐳 拉取镜像 X ···"+循环动画点;拉取结束(成功/失败)由 dockerPullDoneMsg 通知。

type dockerPullDoneMsg struct{ err error } // 拉取结束:err==nil 即成功
type dockerPullTickMsg struct{}            // 动画 tick:推进省略号

// waitDockerPull 阻塞等拉取结果(只一条),转成 dockerPullDoneMsg。
func waitDockerPull(ch <-chan error) tea.Cmd {
	return func() tea.Msg { return dockerPullDoneMsg{err: <-ch} }
}

// dockerPullTickCmd 每 400ms 发一次 tick,驱动省略号动画。
func dockerPullTickCmd() tea.Cmd {
	return tea.Tick(400*time.Millisecond, func(time.Time) tea.Msg { return dockerPullTickMsg{} })
}

// dockerPullText 渲染"🐳 拉取镜像 X ···",点数随 dots 在 1..3 间循环。
func dockerPullText(image string, dots int) string {
	return fmt.Sprintf("🐳 "+T("sandbox.pulling")+" %s %s", image, strings.Repeat("·", 1+dots%3))
}
