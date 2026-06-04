package tools

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Docker 沙箱后端:命令在常驻容器里跑(workspace 绑定挂载到 /workspace),真隔离。
// 容器按 workspace 命名复用;首次用时拉镜像 + 起容器;deepx 退出时删容器。
// 跨三平台靠 docker CLI(mac/win 用 Docker Desktop,linux 原生)。

const dockerMount = "/workspace" // 容器内挂载点

var (
	sbWorkspace atomic_string // 宿主 workspace 绝对路径(SetSandboxWorkspace 注入)
	sbImage     atomic_string // 容器镜像(默认 ubuntu:24.04,/sandbox docker <image> 可换)
	dockerMu    sync.Mutex    // 串行化容器生命周期操作
)

// atomic_string 是个极简的并发安全字符串(避免引入 atomic.Value 的类型断言样板)。
type atomic_string struct {
	mu sync.RWMutex
	v  string
}

func (a *atomic_string) Store(s string) { a.mu.Lock(); a.v = s; a.mu.Unlock() }
func (a *atomic_string) Load() string   { a.mu.RLock(); defer a.mu.RUnlock(); return a.v }

// SetSandboxWorkspace 注入 workspace 绝对路径(docker 挂载 + cwd 换算用)。启动时调,同 SetCodeGraphRoot。
func SetSandboxWorkspace(dir string) {
	if abs, err := filepath.Abs(dir); err == nil {
		sbWorkspace.Store(abs)
	} else {
		sbWorkspace.Store(dir)
	}
}

// SandboxDockerImage 返回当前容器镜像(空则默认 ubuntu:24.04)。
func SandboxDockerImage() string {
	if v := sbImage.Load(); v != "" {
		return v
	}
	return "ubuntu:24.04"
}

// SetSandboxDockerImage 设置容器镜像。换镜像意味着下次用容器要重建(由调用方决定时机)。
func SetSandboxDockerImage(image string) { sbImage.Store(strings.TrimSpace(image)) }

// DockerAvailable 探测 docker 是否可用(已装且 daemon 在跑)。3s 超时,不挂死。
func DockerAvailable() error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := exec.LookPath("docker"); err != nil {
		return fmt.Errorf("未找到 docker 命令(请安装 Docker)")
	}
	if out, err := exec.CommandContext(ctx, "docker", "info", "--format", "{{.ServerVersion}}").CombinedOutput(); err != nil {
		return fmt.Errorf("Docker daemon 未运行或不可达(请启动 Docker):%s", strings.TrimSpace(string(out)))
	}
	return nil
}

// sandboxContainerName 按 workspace 路径派生稳定容器名,便于复用。
func sandboxContainerName() string {
	h := sha1.Sum([]byte(sbWorkspace.Load()))
	return "deepx-sbx-" + hex.EncodeToString(h[:])[:12]
}

// EnsureDockerContainer 保证容器在跑(没有则建、停了则启),返回容器名。串行化,避免并发重复建。
func EnsureDockerContainer() (string, error) {
	dockerMu.Lock()
	defer dockerMu.Unlock()

	ws := sbWorkspace.Load()
	if ws == "" {
		return "", fmt.Errorf("workspace 未设置,无法挂载")
	}
	name := sandboxContainerName()

	// 已在跑 → 直接用
	if running, _ := containerRunning(name); running {
		return name, nil
	}
	// 存在但停了 → 启动;否则新建
	if exists, _ := containerExists(name); exists {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if out, err := exec.CommandContext(ctx, "docker", "start", name).CombinedOutput(); err != nil {
			return "", fmt.Errorf("启动已有容器失败:%s", strings.TrimSpace(string(out)))
		}
		return name, nil
	}

	// 新建:挂载 workspace、保活(sleep infinity)、网络默认开(bridge)。首次拉镜像可能较慢。
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	args := []string{
		"run", "-d", "--name", name,
		"--label", "deepx-sandbox=1",
		"-v", ws + ":" + dockerMount,
		"-w", dockerMount,
		SandboxDockerImage(),
		"sleep", "infinity",
	}
	if out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput(); err != nil {
		return "", fmt.Errorf("创建容器失败(镜像 %s):%s", SandboxDockerImage(), strings.TrimSpace(string(out)))
	}
	return name, nil
}

func containerRunning(name string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "inspect", "-f", "{{.State.Running}}", name).Output()
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(out)) == "true", nil
}

func containerExists(name string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := exec.CommandContext(ctx, "docker", "inspect", name).Run()
	return err == nil, err
}

// StopSandboxContainer 强删沙箱容器(deepx 退出时调)。best-effort,失败静默。
func StopSandboxContainer() {
	ws := sbWorkspace.Load()
	if ws == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = exec.CommandContext(ctx, "docker", "rm", "-f", sandboxContainerName()).Run()
}

// containerWorkdir 把宿主 cwd 换算成容器内路径(挂载点下)。cwd 为空或不在 workspace 内 → /workspace。
func containerWorkdir(cwd string) string {
	ws := sbWorkspace.Load()
	if cwd == "" || ws == "" {
		return dockerMount
	}
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return dockerMount
	}
	rel, err := filepath.Rel(ws, abs)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return dockerMount
	}
	return dockerMount + "/" + filepath.ToSlash(rel)
}

// ImagePresent 判断镜像是否本地已有(有则无需拉取)。
func ImagePresent(image string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "docker", "image", "inspect", image).Run() == nil
}

// PullImage 异步 `docker pull <image>`:拉完把结果发到 channel(nil=成功)再关闭;镜像已存在直接发 nil。
// 不解析进度——非 TTY 下 docker 不吐字节进度(进度条会一直 0%),UI 改用"拉取中…"动画,这里只关心成败。
func PullImage(ctx context.Context, image string) <-chan error {
	ch := make(chan error, 1)
	go func() {
		defer close(ch)
		if ImagePresent(image) {
			ch <- nil
			return
		}
		out, err := exec.CommandContext(ctx, "docker", "pull", image).CombinedOutput()
		if err != nil {
			if s := strings.TrimSpace(string(out)); s != "" {
				if i := strings.LastIndexByte(s, '\n'); i >= 0 {
					s = strings.TrimSpace(s[i+1:]) // 取最后一行,最贴近真正的报错
				}
				err = fmt.Errorf("%s", s)
			}
		}
		ch <- err
	}()
	return ch
}

// dockerExecCmd 构造"在容器里跑命令"的 exec.Cmd:确保容器在跑,再 docker exec。
func dockerExecCmd(command, cwd string) (*exec.Cmd, error) {
	name, err := EnsureDockerContainer()
	if err != nil {
		return nil, err
	}
	return exec.Command("docker", "exec", "-w", containerWorkdir(cwd), name, "sh", "-c", command), nil
}
