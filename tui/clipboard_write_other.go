//go:build !darwin && !windows

package tui

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// 非 macOS / 非 Windows(即 Linux 等)写系统剪贴板:不靠猜环境,而是**写完立刻读回校验**,
// 自动挑一个"这台机器上真能用"的工具;都校验不过就返回 error,调用方退到 OSC52。
//
// 这解决了一堆挑环境的老问题:xclip 没持住选区、写到错的 selection、Wayland 不同步、
// SSH 误判等——统统由"读回是否一致"实时判定,而不是预先假设环境。
//
// Windows 单独走 clipboard_write_windows.go 的原生 API:clip.exe 按控制台代码页解码
// stdin,喂 UTF-8 会变 GBK 乱码(见 issue #74),故不在此处理。

// clipboardBackend 一个写剪贴板的后端。read 为空 = 无法读回,跳过校验。
type clipboardBackend struct {
	name  string
	write []string
	read  []string
}

func nativeClipboardBackends() []clipboardBackend {
	return []clipboardBackend{
		{"wl-copy", []string{"wl-copy"}, []string{"wl-paste", "--no-newline"}},
		{"xclip", []string{"xclip", "-selection", "clipboard"}, []string{"xclip", "-selection", "clipboard", "-o"}},
		{"xsel", []string{"xsel", "--clipboard", "--input"}, []string{"xsel", "--clipboard", "--output"}},
	}
}

// writeClipboardText 依次尝试本机可用的剪贴板工具:写入 → 立刻读回校验,
// 第一个"读回与原文一致"的即采用;全不通过返回 error(调用方退到 OSC52)。
func writeClipboardText(text string) error {
	var lastErr error
	tried := false
	for _, b := range nativeClipboardBackends() {
		if _, err := exec.LookPath(b.write[0]); err != nil {
			continue
		}
		tried = true
		w := exec.Command(b.write[0], b.write[1:]...)
		w.Stdin = strings.NewReader(text)
		if err := w.Run(); err != nil {
			lastErr = fmt.Errorf("%s 写入失败: %w", b.name, err)
			continue
		}
		if b.read == nil {
			return nil // 无法读回(clip.exe):写没报错即认为成功
		}
		if clipboardReadbackOK(b.read, text) {
			return nil // 读回一致 = 真写进去了
		}
		lastErr = fmt.Errorf("%s 写入未生效(读回不一致)", b.name)
	}
	if !tried {
		return errors.New("未找到可用剪贴板工具(需 xclip / xsel / wl-copy 之一)")
	}
	return lastErr
}

// clipboardReadbackOK 读回剪贴板内容与 want 比对。xclip 派生子进程接管选区有极短延迟,重试几次。
func clipboardReadbackOK(read []string, want string) bool {
	for i := 0; i < 3; i++ {
		out, err := exec.Command(read[0], read[1:]...).Output()
		if err == nil && strings.TrimRight(string(out), "\n") == strings.TrimRight(want, "\n") {
			return true
		}
		time.Sleep(40 * time.Millisecond)
	}
	return false
}
