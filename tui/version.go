package tui

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

// 版本相关常量。repo 写死指向上游,后续 fork 自己跑的话改这里。
const (
	githubRepoOwner = "itmisx"
	githubRepoName  = "deepx-code"
	upgradeCheckTTL = 6 * time.Hour // 缓存 6 小时,避免频繁打 GitHub API
)

// upgradeCheckResult 是版本检查结果,goroutine 完成后通过 tea.Msg 发回主模型。
type upgradeCheckResult struct {
	LatestVersion string // 最新发布的 tag(去掉 v 前缀,如 "0.2.0")
	URL           string // release 页 URL,给"去查看"用
	Err           error  // 网络 / API 失败时非 nil,model 视为"未知"忽略掉
}

// checkForUpgradeCmd 返回一个 tea.Cmd 在后台异步检查新版本,完成后发 upgradeCheckResult。
// 缓存(~/.deepx/meta.json)在 TTL 内且 latest 仍比当前新时直接复用,否则打 GitHub API。
// 失败(timeout / 4xx / 5xx)静默,不弹错误。
//
// "缓存 latest ≤ currentVersion" 视同过期重拉 —— 既然当前已经 ≥ 缓存里那个,缓存给不了
// "是不是有更新版"的信息;不重拉的话发了新版用户重启也不会被提醒,得等 6 小时 TTL。
func checkForUpgradeCmd(currentVersion string) tea.Cmd {
	return func() tea.Msg {
		m := metaGet()
		fresh := !m.UpgradeCheckedAt.IsZero() && time.Since(m.UpgradeCheckedAt) <= upgradeCheckTTL
		if fresh && versionNewer(m.LatestVersion, currentVersion) {
			return upgradeCheckResult{LatestVersion: m.LatestVersion, URL: m.UpgradeURL}
		}
		ver, url, err := fetchLatestRelease()
		if err != nil {
			return upgradeCheckResult{Err: err}
		}
		metaUpdate(func(m *meta) {
			m.UpgradeCheckedAt = time.Now()
			m.LatestVersion = ver
			m.UpgradeURL = url
		})
		return upgradeCheckResult{LatestVersion: ver, URL: url}
	}
}

// upgradeSource 返回安装来源:"gitee" 或 "github"(默认)。决定升级检查打哪个 API、
// `deepx upgrade` 重跑哪个源的安装脚本。来源由安装脚本(install.sh / install.ps1)在安装
// 时写入 ~/.deepx/.upgrade_source(纯文本一行)—— 用独立标记文件而非塞进 meta.json,是为了
// 避免在 shell 里合并 JSON(没 jq 也能 echo 一行)。读不到或非法值一律按 github。
func upgradeSource() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "github"
	}
	data, err := os.ReadFile(filepath.Join(home, ".deepx", ".upgrade_source"))
	if err != nil {
		return "github"
	}
	if strings.TrimSpace(string(data)) == "gitee" {
		return "gitee"
	}
	return "github"
}

// fetchLatestRelease 拿最新 tag,按安装来源走 GitHub 或 Gitee 的 Releases API。
func fetchLatestRelease() (string, string, error) {
	if upgradeSource() == "gitee" {
		return fetchLatestReleaseGitee()
	}
	return fetchLatestReleaseGitHub()
}

// fetchLatestReleaseGitHub 打 GitHub Releases API 拿最新 tag。3s 超时避免拖累启动。
func fetchLatestReleaseGitHub() (string, string, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", githubRepoOwner, githubRepoName)
	client := &http.Client{Timeout: 3 * time.Second}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "deepx-upgrade-check")
	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", "", fmt.Errorf("github api status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return "", "", err
	}
	var rel struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
	}
	if err := json.Unmarshal(body, &rel); err != nil {
		return "", "", err
	}
	return strings.TrimPrefix(rel.TagName, "v"), rel.HTMLURL, nil
}

// fetchLatestReleaseGitee 打 Gitee v5 OpenAPI 拿最新 tag。Gitee 响应不带 html_url,
// release 页 URL 自行按 tag 拼(与 install.sh 的 RELEASE_PAGE/tag/ 格式一致)。
func fetchLatestReleaseGitee() (string, string, error) {
	url := fmt.Sprintf("https://gitee.com/api/v5/repos/%s/%s/releases/latest", githubRepoOwner, githubRepoName)
	client := &http.Client{Timeout: 3 * time.Second}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "deepx-upgrade-check")
	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", "", fmt.Errorf("gitee api status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return "", "", err
	}
	var rel struct {
		TagName string `json:"tag_name"`
	}
	if err := json.Unmarshal(body, &rel); err != nil {
		return "", "", err
	}
	pageURL := fmt.Sprintf("https://gitee.com/%s/%s/releases/tag/%s", githubRepoOwner, githubRepoName, rel.TagName)
	return strings.TrimPrefix(rel.TagName, "v"), pageURL, nil
}

// upgradeCommand 返回当前平台的一键升级指令。升级方式就是重跑安装脚本 —— 它拉最新预编译
// 二进制覆盖安装。按安装来源选脚本源:gitee 装的回 Gitee(raw + SOURCE=gitee,让脚本仍从
// Gitee 下载二进制),否则回 GitHub。Windows 走 PowerShell(install.ps1),其余走 bash。
func upgradeCommand() string {
	if upgradeSource() == "gitee" {
		base := fmt.Sprintf("https://gitee.com/%s/%s/raw/main/scripts", githubRepoOwner, githubRepoName)
		if runtime.GOOS == "windows" {
			return fmt.Sprintf("$env:SOURCE='gitee'; irm %s/install.ps1 | iex", base)
		}
		return fmt.Sprintf("curl -fsSL %s/install.sh | SOURCE=gitee bash", base)
	}
	base := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/main/scripts", githubRepoOwner, githubRepoName)
	if runtime.GOOS == "windows" {
		return fmt.Sprintf("irm %s/install.ps1 | iex", base)
	}
	return fmt.Sprintf("curl -fsSL %s/install.sh | bash", base)
}

// UpgradeHint 是提醒里展示给用户的升级指令 —— 短到不会折行,拖选粘贴一次就对。
// 实际干活的是 `deepx upgrade` 子命令(见 RunUpgrade)。
const UpgradeHint = "deepx upgrade"

// RunUpgrade 执行 `deepx upgrade`:重跑平台对应的安装脚本(从 GitHub Releases 拉最新
// 二进制覆盖安装)。输出直通终端。供 main.go 在 `deepx upgrade` 子命令里调用。
func RunUpgrade() error {
	script := upgradeCommand()
	fmt.Printf("→ %s\n\n", script)
	var c *exec.Cmd
	if runtime.GOOS == "windows" {
		c = exec.Command("powershell", "-NoProfile", "-Command", script)
	} else {
		c = exec.Command("sh", "-c", script)
	}
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	c.Stdin = os.Stdin
	return c.Run()
}

// versionNewer 比较两个语义化版本字符串(已去 v 前缀,允许后缀 -rc1 / -beta 等)。
// latest > current 返回 true。pre-release 后缀走字符串比较,够用即可,不严格遵循 semver。
func versionNewer(latest, current string) bool {
	if latest == "" || current == "" || current == "dev" {
		return false
	}
	la, lpre := splitVersion(latest)
	ca, cpre := splitVersion(current)
	for i := 0; i < 3; i++ {
		var lv, cv int
		if i < len(la) {
			lv = la[i]
		}
		if i < len(ca) {
			cv = ca[i]
		}
		if lv != cv {
			return lv > cv
		}
	}
	// 主版本相同:无 pre-release 视为更"新"(0.1.0 > 0.1.0-rc1)
	if lpre == "" && cpre != "" {
		return true
	}
	if lpre != "" && cpre == "" {
		return false
	}
	return lpre > cpre
}

func splitVersion(v string) (nums []int, pre string) {
	if idx := strings.IndexAny(v, "-+"); idx >= 0 {
		pre = v[idx+1:]
		v = v[:idx]
	}
	for _, s := range strings.Split(v, ".") {
		n, _ := strconv.Atoi(s)
		nums = append(nums, n)
	}
	return
}
