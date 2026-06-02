package tui

import (
	"context"

	"deepx/agent"

	tea "charm.land/bubbletea/v2"
)

// 视觉能力探测的接入层(配合 meta.ModelCaps 缓存):
//
// **每次程序启动都重新探测一次**(不靠版本号、不"命中即跳过")—— 这样任何陈旧/错误的缓存值
// 都会被当场刷新、根除(早期 buggy 探针写下的错误结果不会永久粘住)。
//
//   - 启动时 loadVisionCaps 先用缓存里上次的值给当前会话垫一个初值(探针返回前的一两秒内,
//     粘图也能用上次已知结果,不必冷启动就 OCR);
//   - Init 同时对每个模型发 visionProbeCmds 后台重探,结果经 visionCapMsg **回灌当前会话**
//     (立刻生效,无需重启)并覆盖写回缓存(供下次启动垫初值)。

// visionCapMsg 是一次探测的结果,异步探针完成后回送 Update:更新当前会话 visionByModel + 落盘缓存。
type visionCapMsg struct {
	key    string
	vision bool
}

// modelCapKey 是能力缓存的 key:模型 + base_url 唯一确定一个"端点上的模型"。
func modelCapKey(e agent.ModelEntry) string {
	return e.Model + "@" + e.BaseURL
}

// distinctModelEntries 取 flash / pro 两个 entry 去重(常见两者同供应商,key 相同时只算一个)。
func distinctModelEntries(models agent.ModelConfig) []agent.ModelEntry {
	var out []agent.ModelEntry
	seen := map[string]bool{}
	for _, e := range []agent.ModelEntry{models.Flash, models.Pro} {
		if e.Model == "" {
			continue
		}
		k := modelCapKey(e)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, e)
	}
	return out
}

// loadVisionCaps 从缓存读出各模型上次探到的视觉能力,作为当前会话的初值(探针返回前先用)。
// 取值缺省即 false → 自然降级走 OCR。
func loadVisionCaps(models agent.ModelConfig) map[string]bool {
	caps := metaGet().ModelCaps
	out := map[string]bool{}
	for _, e := range distinctModelEntries(models) {
		if c, ok := caps[modelCapKey(e)]; ok {
			out[modelCapKey(e)] = c.Vision
		}
	}
	return out
}

// visionProbeCmds 对每个模型(flash/pro 去重)各返回一个探测命令——**每次启动都探,不看缓存**。
// 命令在后台跑 ProbeVision,完成回送 visionCapMsg。
func visionProbeCmds(models agent.ModelConfig) []tea.Cmd {
	var cmds []tea.Cmd
	for _, e := range distinctModelEntries(models) {
		entry := e // 闭包捕获
		cmds = append(cmds, func() tea.Msg {
			vision, err := agent.ProbeVision(context.Background(), entry)
			if err != nil {
				return nil // 瞬时错误 → 不更新、不覆盖缓存(保留上次值,下次启动再探)
			}
			return visionCapMsg{key: modelCapKey(entry), vision: vision}
		})
	}
	return cmds
}

// applyVisionCap 把一次探测结果落到当前会话(立刻生效)+ 覆盖写回缓存(供下次启动垫初值)。
func (m *model) applyVisionCap(msg visionCapMsg) {
	if m.visionByModel == nil {
		m.visionByModel = map[string]bool{}
	}
	m.visionByModel[msg.key] = msg.vision
	metaUpdate(func(mm *meta) {
		if mm.ModelCaps == nil {
			mm.ModelCaps = map[string]modelCaps{}
		}
		mm.ModelCaps[msg.key] = modelCaps{Vision: msg.vision}
	})
}
