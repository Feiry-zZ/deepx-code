package tui

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

// 版本相关常量。repo 写死指向上游,后续 fork 自己跑的话改这里。
const (
	githubRepoOwner = "itmisx"
	githubRepoName  = "deepx"
	updateCheckTTL  = 6 * time.Hour // 缓存 6 小时,避免频繁打 GitHub API
)

// updateCheckResult 是版本检查结果,goroutine 完成后通过 tea.Msg 发回主模型。
type updateCheckResult struct {
	LatestVersion string // 最新发布的 tag(去掉 v 前缀,如 "0.2.0")
	URL           string // release 页 URL,给"去查看"用
	Err           error  // 网络 / API 失败时非 nil,model 视为"未知"忽略掉
}

// updateCheckCache 是落盘缓存,避免每次启动都打 GitHub API。
// 走 ~/.deepx/update_check.json,可以读则不发请求,过期才重新探测。
type updateCheckCache struct {
	CheckedAt     time.Time `json:"checked_at"`
	LatestVersion string    `json:"latest_version"`
	URL           string    `json:"url"`
}

func cachePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".deepx", "update_check.json")
}

func readUpdateCheckCache() (*updateCheckCache, bool) {
	p := cachePath()
	if p == "" {
		return nil, false
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, false
	}
	var c updateCheckCache
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, false
	}
	if time.Since(c.CheckedAt) > updateCheckTTL {
		return nil, false
	}
	return &c, true
}

func writeUpdateCheckCache(c updateCheckCache) {
	p := cachePath()
	if p == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	data, err := json.Marshal(c)
	if err != nil {
		return
	}
	_ = os.WriteFile(p, data, 0o644)
}

// checkForUpdateCmd 返回一个 tea.Cmd 在后台异步检查新版本,完成后发 updateCheckResult。
// 命中本地缓存时跳过网络请求直接返回缓存;失败(timeout / 4xx / 5xx)静默,不弹错误。
func checkForUpdateCmd() tea.Cmd {
	return func() tea.Msg {
		// 缓存命中
		if c, ok := readUpdateCheckCache(); ok {
			return updateCheckResult{LatestVersion: c.LatestVersion, URL: c.URL}
		}
		ver, url, err := fetchLatestRelease()
		if err != nil {
			return updateCheckResult{Err: err}
		}
		writeUpdateCheckCache(updateCheckCache{
			CheckedAt:     time.Now(),
			LatestVersion: ver,
			URL:           url,
		})
		return updateCheckResult{LatestVersion: ver, URL: url}
	}
}

// fetchLatestRelease 打 GitHub Releases API 拿最新 tag。3s 超时避免拖累启动。
func fetchLatestRelease() (string, string, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", githubRepoOwner, githubRepoName)
	client := &http.Client{Timeout: 3 * time.Second}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "deepx-update-check")
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
