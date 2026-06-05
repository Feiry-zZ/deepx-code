package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// 对话(conversation)层:在一个 workspace session 内部支持多条对话 + 列表/切换/新建。
//
// 零迁移设计:**默认对话就是 rootDir 本身**(老数据原地不动、旧版本也能打开),
// 只有 /new 出来的新对话才落到 rootDir/conversations/{id}/。
// current 指针文件记录当前激活的对话 id("default" 或时间戳 id)。
const (
	currentFile      = "current"       // rootDir 下,内容是当前对话 id
	conversationsDir = "conversations" // rootDir 下,装非默认对话
	defaultConvID    = "default"       // 默认对话(= rootDir)的虚拟 id
	convMetaFile     = "conv.json"     // 每条对话目录下的元信息
	historyGob       = "history.gob"   // 对话历史文件名(与 tui 调用 SaveGob 的一致)
)

// ConvInfo 是列给 UI 的单条对话信息。
type ConvInfo struct {
	ID         string
	Title      string
	CreatedAt  time.Time
	LastSeenAt time.Time
	Active     bool // 是否当前对话
}

// convMeta 是 conv.json 的结构。标题由 TUI 设置(取首条用户消息),session 不解码 history.gob。
type convMeta struct {
	Title      string    `json:"title,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	LastSeenAt time.Time `json:"last_seen_at"`
}

// CurrentConversation 返回当前对话 id;无 current 文件 / 空 → "default"。
func (m *Manager) CurrentConversation() string {
	data, err := os.ReadFile(filepath.Join(m.rootDir, currentFile))
	if err != nil {
		return defaultConvID
	}
	if id := strings.TrimSpace(string(data)); id != "" {
		return id
	}
	return defaultConvID
}

// OnDefaultConversation 当前是否为默认对话(= rootDir,升级前的老会话)。
// 用途:只有默认对话才可用 workspace 级 JSONL 做"无 gob 兜底恢复";/new 出来的新对话
// 没有自己的 history.gob 时应显示空,绝不能去捞共享 JSONL 里别的对话的内容(否则新会话显示旧内容)。
func (m *Manager) OnDefaultConversation() bool {
	return m.CurrentConversation() == defaultConvID
}

// resolveConvDir 按 current 指针把 convDir 定位到对应对话目录。
// "default" / 空 / 指针失效(目录不存在)→ 一律回退 rootDir(默认对话,绝不丢老数据)。
func (m *Manager) resolveConvDir() string {
	id := m.CurrentConversation()
	if id == defaultConvID {
		return m.rootDir
	}
	dir := filepath.Join(m.rootDir, conversationsDir, id)
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return m.rootDir
	}
	return dir
}

// writeCurrent 原子写 current 指针(rootDir 级)。失败静默。
func (m *Manager) writeCurrent(id string) {
	path := filepath.Join(m.rootDir, currentFile)
	tmp := path + ".tmp"
	if os.WriteFile(tmp, []byte(id), 0o644) == nil {
		_ = os.Rename(tmp, path)
	}
}

// NewConversation 新建一条对话:建目录、写 conv.json、把 current 指过去、convDir 切到它。
// 返回新对话 id。
func (m *Manager) NewConversation() (string, error) {
	base := time.Now().Format("20060102-150405")
	id := base
	dir := filepath.Join(m.rootDir, conversationsDir, id)
	for i := 1; dirExists(dir); i++ { // 同秒撞名兜底
		id = fmt.Sprintf("%s-%d", base, i)
		dir = filepath.Join(m.rootDir, conversationsDir, id)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir conversation: %w", err)
	}
	m.convDir = dir
	now := time.Now()
	m.writeConvMeta(convMeta{CreatedAt: now, LastSeenAt: now})
	m.writeCurrent(id)
	return id, nil
}

// SwitchConversation 切换当前对话。id="default"/"" → 默认对话(rootDir)。
// 目录不存在返回错误,且不改动当前状态。
func (m *Manager) SwitchConversation(id string) error {
	if id == "" || id == defaultConvID {
		m.convDir = m.rootDir
		m.writeCurrent(defaultConvID)
		return nil
	}
	dir := filepath.Join(m.rootDir, conversationsDir, id)
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return fmt.Errorf("conversation %q not found", id)
	}
	m.convDir = dir
	m.writeCurrent(id)
	return nil
}

// convDirByID 按 id 定位对话目录("default"/空 → rootDir,其余 → conversations/<id>)。
func (m *Manager) convDirByID(id string) string {
	if id == "" || id == defaultConvID {
		return m.rootDir
	}
	return filepath.Join(m.rootDir, conversationsDir, id)
}

// RenameConversation 按 id 改某条对话的标题(不切换当前对话)。目录不存在返回错误。
// 只改 title,保留 created_at / last_seen,使按创建时间排序稳定。
func (m *Manager) RenameConversation(id, title string) error {
	dir := m.convDirByID(id)
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return fmt.Errorf("conversation %q not found", id)
	}
	meta := readConvMeta(dir)
	if meta.CreatedAt.IsZero() {
		meta.CreatedAt = time.Now()
	}
	meta.Title = strings.TrimSpace(title)
	m.writeConvMetaTo(dir, meta)
	return nil
}

// DeleteConversation 按 id 删除一条对话(连目录一起删)。默认对话(rootDir)不可删。
// 若删的是当前对话,先切回默认对话再删,避免 convDir 悬空。
func (m *Manager) DeleteConversation(id string) error {
	if id == "" || id == defaultConvID {
		return fmt.Errorf("default conversation cannot be deleted")
	}
	dir := filepath.Join(m.rootDir, conversationsDir, id)
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return fmt.Errorf("conversation %q not found", id)
	}
	if m.CurrentConversation() == id {
		m.convDir = m.rootDir
		m.writeCurrent(defaultConvID)
	}
	return os.RemoveAll(dir)
}

// SetConvTitle 设置/刷新当前对话标题(由 TUI 取首条用户消息设),并更新 last_seen。
func (m *Manager) SetConvTitle(title string) {
	meta := readConvMeta(m.convDir)
	if meta.CreatedAt.IsZero() {
		meta.CreatedAt = time.Now()
	}
	meta.Title = title
	meta.LastSeenAt = time.Now()
	m.writeConvMeta(meta)
}

// TouchConv 更新当前对话的 last_seen(每轮结束时调,用于列表按最近排序)。
func (m *Manager) TouchConv() {
	meta := readConvMeta(m.convDir)
	if meta.CreatedAt.IsZero() {
		meta.CreatedAt = time.Now()
	}
	meta.LastSeenAt = time.Now()
	m.writeConvMeta(meta)
}

// ConvTitle 返回当前对话已存的标题(空串表示未设)。
func (m *Manager) ConvTitle() string { return readConvMeta(m.convDir).Title }

// ListConversations 列出本 workspace 的所有对话:默认对话(rootDir 有 history.gob 才算)+
// conversations/* 各条。按 last_seen 倒序(最近在前)。
func (m *Manager) ListConversations() []ConvInfo {
	cur := m.CurrentConversation()
	var out []ConvInfo

	// 默认对话:rootDir 下有 history.gob 才纳入
	if fileExists(filepath.Join(m.rootDir, historyGob)) {
		out = append(out, m.convInfoFor(defaultConvID, m.rootDir, cur))
	}
	// 非默认对话
	entries, _ := os.ReadDir(filepath.Join(m.rootDir, conversationsDir))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		id := e.Name()
		out = append(out, m.convInfoFor(id, filepath.Join(m.rootDir, conversationsDir, id), cur))
	}

	sort.Slice(out, func(i, j int) bool { return out[i].LastSeenAt.After(out[j].LastSeenAt) })
	return out
}

// convInfoFor 从对话目录 dir 拼出 ConvInfo;时间缺失时用 history.gob / 目录 mtime 兜底。
func (m *Manager) convInfoFor(id, dir, cur string) ConvInfo {
	meta := readConvMeta(dir)
	ci := ConvInfo{ID: id, Title: meta.Title, CreatedAt: meta.CreatedAt, LastSeenAt: meta.LastSeenAt, Active: id == cur}
	if ci.LastSeenAt.IsZero() {
		if fi, err := os.Stat(filepath.Join(dir, historyGob)); err == nil {
			ci.LastSeenAt = fi.ModTime()
		} else if fi, err := os.Stat(dir); err == nil {
			ci.LastSeenAt = fi.ModTime()
		}
	}
	// CreatedAt 缺失(老默认会话没存 conv.json)→ 用历史文件 / 目录 mtime 兜底,保证按创建时间排序稳定。
	if ci.CreatedAt.IsZero() {
		if fi, err := os.Stat(filepath.Join(dir, historyGob)); err == nil {
			ci.CreatedAt = fi.ModTime()
		} else if fi, err := os.Stat(dir); err == nil {
			ci.CreatedAt = fi.ModTime()
		}
	}
	return ci
}

// writeConvMeta 原子写当前 convDir 的 conv.json。
func (m *Manager) writeConvMeta(meta convMeta) { m.writeConvMetaTo(m.convDir, meta) }

// writeConvMetaTo 原子写指定目录的 conv.json(供按 id 重命名等不切当前对话的操作)。
func (m *Manager) writeConvMetaTo(dir string, meta convMeta) {
	data, _ := json.MarshalIndent(meta, "", "  ")
	path := filepath.Join(dir, convMetaFile)
	tmp := path + ".tmp"
	if os.WriteFile(tmp, data, 0o644) == nil {
		_ = os.Rename(tmp, path)
	}
}

func readConvMeta(dir string) convMeta {
	var meta convMeta
	if data, err := os.ReadFile(filepath.Join(dir, convMetaFile)); err == nil {
		_ = json.Unmarshal(data, &meta)
	}
	return meta
}

func fileExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir()
}

func dirExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}
