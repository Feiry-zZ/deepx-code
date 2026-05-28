package tui

import "testing"

// isExactSlashCommand 决定带参命令(如 /model flash)能否被正确分发:
// 首 token 精确命中命令名才转发完整输入,前缀(如 /au)不算。
func TestIsExactSlashCommand(t *testing.T) {
	exact := []string{
		"/model flash", "/model pro", "/model auto", "/model", // 带参 + 裸命令
		"/auto", "/plan", "/MODEL flash", "/model   pro", // 大小写 / 多空格
	}
	for _, s := range exact {
		if !isExactSlashCommand(s) {
			t.Errorf("isExactSlashCommand(%q) = false, want true", s)
		}
	}

	notExact := []string{
		"/au",            // 前缀,非精确
		"/mod flash",     // 前缀首 token,非精确命令名
		"hello /model",   // 不以命令开头
		"/unknown x",     // 未知命令
		"",               // 空
		"refactor 模块",  // 普通消息
	}
	for _, s := range notExact {
		if isExactSlashCommand(s) {
			t.Errorf("isExactSlashCommand(%q) = true, want false", s)
		}
	}
}
