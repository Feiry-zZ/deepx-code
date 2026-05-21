package main

import (
	"deepx/agent"
	"deepx/config"
	"deepx/tui"
	"fmt"
	"os"
)

// 由 goreleaser 在 build 时通过 -ldflags "-X main.version=..." 注入。
// dev 构建(直接 go build .)保留默认值,便于在本地区分。
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "-v" || os.Args[1] == "version") {
		fmt.Printf("deepx %s (commit %s, built %s)\n", version, commit, date)
		return
	}
	cfg, needsSetup, err := loadOrEmptyConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "错误:", err)
		os.Exit(1)
	}
	if err := tui.Run(toAgentConfig(cfg), needsSetup, version); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

// loadOrEmptyConfig:
//   - 如果 ~/.deepx/model.yaml 存在 → 读取并返回 (cfg, false, nil)
//   - 不存在 → 返回 (空 cfg, true, nil),TUI 起来后会弹 modal 引导
//
// 读取出错(yaml 格式坏)→ 直接返回 err 让程序退出,不静默吞。
func loadOrEmptyConfig() (*config.Config, bool, error) {
	if !config.Exists() {
		return &config.Config{}, true, nil
	}
	c, err := config.Load()
	if err != nil {
		return nil, false, err
	}
	return c, false, nil
}

// toAgentConfig 把 config.Config 转成 agent.ModelConfig。
// 空 cfg(首次启动)转出来也是空,TUI 在 modal 关闭前不会发起任何 LLM 调用。
func toAgentConfig(c *config.Config) agent.ModelConfig {
	return agent.ModelConfig{
		Flash: agent.ModelEntry(c.Flash),
		Pro:   agent.ModelEntry(c.Pro),
	}
}
