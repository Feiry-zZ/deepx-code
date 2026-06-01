//go:build !windows

package tools

import (
	"os/exec"
	"syscall"
)

// setPgid 让子进程自成一个进程组,这样 killProc 能用负 PID 把整棵进程树一起杀掉
// —— 否则 `sh -c "npx vite"` 派生出的 node 子进程会成孤儿继续占着端口。
func setPgid(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProc 杀掉子进程所在的整个进程组。对"进程已退出/组已空"幂等(返回 nil)。
//
// pgid 直接取启动时的主进程 pid:setPgid 让子进程自成进程组(组 leader = 主进程),
// 所以 pgid == cmd.Process.Pid,且这个值不随进程退出而变。
//
// 这里**不再用 syscall.Getpgid(pid) 现查 pgid** —— 那是 issue #20 里 KillBash 漏杀的根因:
// 主进程(leader)用 `&` 派生后台子进程后自己先退出,在被 reap 前是僵尸,Getpgid 对僵尸返回
// ESRCH,旧逻辑据此误判"进程已没了"直接返回成功,放过了进程组里仍存活的后台子进程。
// 进程组只要还有存活成员就一直存在,kill(-pgid) 照样能打到它们;组已空则 ESRCH,视为已达成。
func killProc(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	pgid := cmd.Process.Pid
	if kerr := syscall.Kill(-pgid, syscall.SIGKILL); kerr != nil && kerr != syscall.ESRCH {
		return kerr // 负号 = 整组;组已消失(ESRCH)视为已达成
	}
	return nil
}
