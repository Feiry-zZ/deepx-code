package tui

import (
	"deepx/agent"
	"os"

	tea "charm.land/bubbletea/v2"
)

// Run 启动 TUI 主循环,直到用户退出或发生错误。
// needsSetup=true 时(磁盘上没 model.yaml),TUI 起来后立即弹配置 modal,
// 用户在 modal 里填 api key,deepx 落盘后无缝转入正常聊天。
// version 是 build 时通过 ldflags 注入的版本号,显示在右栏并用作升级检查的当前版本。
func Run(models agent.ModelConfig, needsSetup bool, version string) error {
	// 启动前清空当前终端的 scrollback (\x1b[3J),避免 deepx 启动时 Terminal.app
	// 把之前 shell 的输出留在 alt-screen 的 scrollback 缓冲里,用户拖滚动条会看到。
	// 注意:bubbletea 默认不发这个 escape (它只发 \x1b[2J 清可见区)。
	// 运行中产生的帧 Terminal.app 仍会缓存到自己的 scrollback,无解(同 vim/less)。
	os.Stderr.Write([]byte("\x1b[3J"))

	// v2 把 alt-screen / mouse mode 从 Program option 移到了 View 结构体字段。
	// 见 tui/view.go 的 wrapView(): 每帧返回的 tea.View 里设 AltScreen=true + MouseMode。
	// 这里 NewProgram 不再带这些 option。
	p := tea.NewProgram(initialModel(models, needsSetup, version))
	_, err := p.Run()
	return err
}
