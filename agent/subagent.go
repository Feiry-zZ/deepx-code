package agent

import (
	"context"
	"deepx/tools"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// subAgentToolDenylist 列出子 agent 不应该看到的工具名。
//
// 准则:子 agent 是"被分派一个 plan 节点执行"的受控上下文,工具集应窄于主对话。
// 当前排除两类:
//   - SwitchModel:子 agent 角色由 CreatePlan 节点的 model 字段静态指定,不应该
//     运行时再切。这工具的 Executor 也是 nil(主循环里拦截改 entry/role),子 agent
//     循环没对应分支,真调到会段错误。
//   - CreatePlan:DAG 调度只在主 agent 循环里实现,子 agent 调用也产生不了真 DAG,
//     反而可能让模型自我递归("我要再拆个子 plan")。即使不递归也只是个无效占位。
//
// 把白名单/黑名单放在这里(而不是 tools/tools.go 的 Roles 字段),是因为这是
// **子 agent 自己的策略**,跟 subagent 的 system prompt 同地维护更直观:同一个文件
// 看到"我是什么角色 + 我用什么工具 + 我不该干什么"。
var subAgentToolDenylist = map[string]bool{
	"SwitchModel": true,
	"CreatePlan":  true,
}

// buildSubAgentToolSpecs 子 agent 工具白名单。
// 在 RoleSubAgent 角色过滤之上,再剔除 subAgentToolDenylist 里的工具。
func buildSubAgentToolSpecs(mode AgentMode) []tools.OpenAIToolSpec {
	base := buildToolSpecs(mode, tools.RoleSubAgent)
	out := make([]tools.OpenAIToolSpec, 0, len(base))
	for _, t := range base {
		if subAgentToolDenylist[t.Function.Name] {
			continue
		}
		out = append(out, t)
	}
	return out
}

// subAgentInput 是一次子 agent 调用的全部依赖。
// 由 runDAG 的 exec 回调按节点上下文构造,主 agent 不直接调用。
type subAgentInput struct {
	Models       ModelConfig // 整套配置,留作扩展用(目前不直接消费)
	Entry        ModelEntry  // 本节点选定的连接参数 (BaseURL/Model/APIKey)
	NodeID       string
	NodeTitle    string
	UserTask     string            // 用户原始消息,作为背景给子 agent
	Predecessors map[string]string // 已完成上游节点的 summary
	Workspace    string
	MaxTokens    int
	Mode         AgentMode
}

// subAgentResult 子 agent 完成后的产物。
type subAgentResult struct {
	Summary string
	Err     error
}

// 子 agent 的轮数上限。比主 agent 紧一点(主 100, 子 50),
// 因为子 agent 任务粒度更小;做不完直接 fail,scheduler 会把该节点标 failed 而不影响其他节点。
const subAgentMaxRounds = 50

// runSubAgent 执行单个 plan/task 节点。
//
// 行为:
//   - 独立 history,只含 system prompt + 用户原始任务 + 节点 title
//   - 工具白名单按 RoleSubAgent 过滤 (看不到 CreatePlan,避免递归 plan)
//   - UpdatePlanStatus 调用被吞掉,scheduler 才是状态真实来源
//   - 不向 TUI 发 TokenMsg / ToolCallStartMsg 等可见事件,子 agent 中间过程完全隐藏
//   - 最终 assistant content 作为 Summary 返回;失败 → Err
func runSubAgent(ctx context.Context, in subAgentInput) subAgentResult {
	// 构造系统提示。子 agent 看到的上下文就这几行,简短紧凑。
	var sb strings.Builder
	sb.WriteString("你是 deepx 中的子 agent,只负责完成一个被分派的 plan 项,不要再 SwitchModel、也不要 CreatePlan。\n")
	sb.WriteString("工作目录: ")
	sb.WriteString(in.Workspace)
	sb.WriteString("\n用户的原始任务背景: ")
	sb.WriteString(in.UserTask)
	sb.WriteString("\n你这一项的具体目标: ")
	sb.WriteString(in.NodeTitle)
	if len(in.Predecessors) > 0 {
		sb.WriteString("\n\n上游已完成节点的产出 (作为上下文使用):")
		for id, sum := range in.Predecessors {
			sb.WriteString("\n- [")
			sb.WriteString(id)
			sb.WriteString("] ")
			sb.WriteString(sum)
		}
	}
	sb.WriteString("\n\n完成后只输出一段简短(<200 字)的结果总结。不要写多余的客套。")

	convo := []ChatMessage{
		{Role: "system", Content: sb.String()},
		{Role: "user", Content: in.NodeTitle},
	}

	toolSpecs := buildSubAgentToolSpecs(in.Mode)

	// 静默 channel:streamOnce 的 TokenMsg 不进 UI,内部 drain 掉
	silent := make(chan tea.Msg, 64)
	drained := make(chan struct{})
	go func() {
		for range silent {
		}
		close(drained)
	}()

	for round := 0; round < subAgentMaxRounds; round++ {
		// 检查 context 是否取消(ESC/退出)
		if ctx.Err() != nil {
			close(silent)
			<-drained
			return subAgentResult{Err: ctx.Err()}
		}
		// 不主动 strip reasoning:本轮锁定模型,thinking 模型仍正常回传,
		// 非 thinking 模型忽略 history 里的 reasoning_content 字段(omitempty 已处理空值)。
		content, reasoning, toolCalls, err := streamOnce(
			ctx,
			in.Entry.APIKey, in.Entry.BaseURL, in.Entry.Model,
			convo, in.MaxTokens, toolSpecs, silent,
		)
		if err != nil {
			close(silent)
			<-drained
			return subAgentResult{Err: err}
		}

		// 必须把 reasoning_content 存进 history,thinking 模型下一轮要求原样回传。
		// 之前丢这个字段是 sub-agent 400 "reasoning_content must be passed back" 的根因。
		convo = append(convo, ChatMessage{
			Role:             "assistant",
			Content:          content,
			ReasoningContent: reasoning,
			ToolCalls:        toolCalls,
		})

		if len(toolCalls) == 0 {
			// 子 agent 完成,返回最后一段 assistant 文本作为 summary
			close(silent)
			<-drained
			summary := strings.TrimSpace(content)
			if summary == "" {
				summary = "(子 agent 未给出明确结论)"
			}
			return subAgentResult{Summary: summary}
		}

		for _, tc := range toolCalls {
			var result tools.ToolResult
			switch tc.Function.Name {
			case "UpdatePlanStatus":
				// 子 agent 想自报状态,吞掉给 OK。scheduler 才是状态来源。
				result = tools.ToolResult{Output: "已记录", Success: true}
			default:
				result = executeTool(tc, in.Mode, tools.RoleSubAgent)
			}
			convo = append(convo, ChatMessage{
				Role:       "tool",
				ToolCallID: tc.ID,
				Name:       tc.Function.Name,
				Content:    result.Output,
			})
		}
	}

	close(silent)
	<-drained
	return subAgentResult{Err: fmt.Errorf("子 agent [%s] 超过 %d 轮工具调用上限", in.NodeID, subAgentMaxRounds)}
}

// resolveModelEntry 把 plan/task 里 "flash" / "pro" 字符串映射到 ModelConfig 里的完整 entry。
// roleHint 解析:
//   - "pro" / "Pro" → 返回 cfg.Pro(若有 model id)
//   - "flash" / "" / 其他 → 返回 cfg.Flash(若有 model id),否则退到 cfg.Pro
//
// 兜底逻辑保证不会返回空 entry,即使节点的 model 字段误填也能跑。
func resolveModelEntry(roleHint string, cfg ModelConfig) ModelEntry {
	switch strings.ToLower(strings.TrimSpace(roleHint)) {
	case "pro":
		if cfg.Pro.Model != "" {
			return cfg.Pro
		}
	case "flash", "":
		// 走默认
	}
	if cfg.Flash.Model != "" {
		return cfg.Flash
	}
	return cfg.Pro
}
