package tui

import (
	"context"
	"deepx/agent"
	"deepx/session"
	"deepx/skill"
	"deepx/tools"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// imagePlaceholderRe 匹配输入框里 [Image #N] 形式的图片占位符。
var imagePlaceholderRe = regexp.MustCompile(`\[Image #(\d+)\]`)

type model struct {
	width  int
	height int

	chatViewport viewport.Model
	chatContent  *chatLog

	input textinput.Model

	// 整套连接配置 (per-role BaseURL/Model/APIKey),从 ~/.deepx/model.yaml 读取后传入。
	// 旧的 apiKey/baseURL 单独字段已合并到 models 里,通过 models.Flash.XX / models.Pro.XX 访问。
	models agent.ModelConfig

	// 配置 modal 状态:
	//   - showSetup       = true 时 View() 弹模态,Update() 路由按键给 setupInput
	//   - setupRequired   = true 表示无有效 yaml(首次启动);不允许 Esc 关闭,Ctrl+C 才退出
	//   - setupInput      = api key 输入框
	//   - setupErr        = 错误回显(保存失败 / 输入为空等)
	showSetup     bool
	setupRequired bool
	setupInput    textinput.Model
	setupErr      string

	// activeModelRole 是上一轮 / 当前流的实际生效角色 ("flash" / "pro")。
	// 每条新用户消息默认从 flash 起手;agent.ModelSwitchMsg 到达时更新为 pro。
	activeModelRole string
	activeModelID   string

	mode    agent.AgentMode
	history []agent.ChatMessage

	streamCh <-chan tea.Msg

	status    string
	streaming bool
	tokens    int

	currentReply *strings.Builder

	// 待发送的图片已落盘文件路径,Ctrl+V 累加,enter 后清空。
	// 不在内存里囤 PNG 字节是为了:(a) 让 img_ocr 工具能凭路径读;
	// (b) 历史 ChatMessage 之后再次发回 API 时不带巨大的 base64。
	attachedImagePaths []string

	// 当前活跃规划。nil = 无规划。pro 调用 CreatePlan 时初始化,
	// UpdatePlanStatus 通过 TaskStatusMsg 增量更新。每次新用户消息发起前清空。
	plan *planState

	// 鼠标 chat 矩形选区。selecting=true 表示左键在 chat 区按下后还没松开;
	// selAnchor / selEnd 是选区两端 (cellPos: 显示列 + wrapped 行号)。
	// 松开左键时:抠出选区内文本写到系统剪贴板,然后 selecting=false 高亮自动消失。
	selecting bool
	selAnchor cellPos
	selEnd    cellPos

	// 输入框全选态(Ctrl+A 触发)。true 时输入框 value 整段反色显示,
	// 下一次按键消费"全选"语义:输入字符 / 删除键 → 清空 value 后再 process;
	// Esc / 方向键 → 仅取消选择,不动 value;
	// Ctrl+A → 维持。
	inputAllSelected bool

	// 命令 palette 选中索引。是否打开不存字段 — 由 filterSlashCommands(input value)
	// 实时计算,避免状态同步问题。idx 越界时(value 一变 matches 变短)由消费方 clamp。
	commandPaletteIdx int

	// lastInputClickAt 记录最近一次落在输入框那一行的左键 click 时间戳。
	// 用来手动检测双击:两次 click 间隔 < 400ms 即视为双击,切换 inputAllSelected。
	// bubbletea v2 的 MouseClickMsg 不带 Clicks 计数,只能自己算。
	lastInputClickAt time.Time

	// 思考动画。streaming=true 且当前没在接收 content tokens 时,在 chat 末尾追加 spinner 帧。
	// reasoning_content / tool_call 阶段都视为"思考中",content token 一到就停;
	// 下一轮工具结果或新 reasoning 到来时再次开启,实现"多轮思考折叠成一个动画"。
	spinner  spinner.Model
	thinking bool

	// scrollbarDragging: 左键在滚动条列按下且未松开。该状态下 motion 事件 → SetYOffset。
	scrollbarDragging bool

	// session 是当前 workspace 的持久化句柄。启动时建/打开 ~/.deepx/sessions/{sid}/,
	// 写时机:user enter 后 + assistant 流结束(StreamDoneMsg)时各 append 一行。
	// Memory 工具通过 tools.SetMemorySession 拿到同一句柄,扫 jsonl 命中关键词。
	session *session.Manager

	// skill 系统:启动时扫 ~/.deepx/skills/ + session.SkillsDir() 两个目录的 SKILL.md。
	//   - skillLoader 注入 tools.LoadSkill,LLM 调工具时按名取正文
	//   - skillCatalog 在 system prompt 里挂"name + description"摘要,引导 LLM 决定要不要 LoadSkill
	// 进程内不刷新 — 用户增删 skill 后重启 deepx。
	skillLoader  *skill.Loader
	skillCatalog string

	// review 模式审核状态
	reviewPending  bool
	reviewCh       chan bool
	reviewToolName string
	reviewToolArgs string
	reviewYesNo    bool // true=YES, false=NO

	// 右栏仪表盘字段
	workspace       string        // os.Getwd() at startup,展示当前工作目录
	turnStartedAt   time.Time     // 本轮 Enter 时刻,用于实时计算 elapsed
	turnElapsed     time.Duration // 上一轮总耗时,streaming=false 时显示这个
	turnInputChars  int           // 本轮 user 发送时的 history 总字符数(快照)
	turnOutputChars int           // 本轮 assistant content 累计字符数(只算 content,跳过 reasoning)

	// cancelAgent 取消后台 agent 的 context。ESC 中断时调用,真正终止 HTTP 请求和工具调用。
	cancelAgent context.CancelFunc

	// lastUsage 上一轮主 agent 的 API token 用量,含缓存命中信息。
	lastUsage *agent.UsageInfo
}

// reviewResultMsg 审核完成后从 goroutine 发回,恢复流监听。
type reviewResultMsg struct{}

// compressionResultMsg 会话压缩完成后的结果,由异步 tea.Cmd 发回 Update。
type compressionResultMsg struct {
	summary         string
	cutIdx          int // 从 snapshot 算出的截断位置
	compressedTurns int // 本次压缩的 user 轮数
	err             error
}

func initialModel(models agent.ModelConfig, needsSetup bool) model {
	vp := viewport.New()

	sp := spinner.New()
	sp.Spinner = spinner.MiniDot

	ti := textinput.New()
	ti.Placeholder = "Type a message... (Enter to send, Esc to interrupt)"
	ti.CharLimit = 4000

	si := textinput.New()
	si.Placeholder = "sk-..."
	si.CharLimit = 256
	si.SetWidth(50)

	// 起手角色 = flash (若 flash 未配置则退化到 pro)
	role := "flash"
	activeID := models.Flash.Model
	if activeID == "" {
		role = "pro"
		activeID = models.Pro.Model
	}

	wd, _ := os.Getwd()

	// 当前 workspace 的会话持久化。建/打开 ~/.deepx/sessions/{sha1(wd)[:16]}/。
	// 失败(权限/磁盘满)不致命 —— sess=nil 时 appendChat 跳过持久化,Memory 工具返回禁用提示。
	sess, sessErr := session.New(wd)
	if sessErr == nil {
		tools.SetMemorySession(sess)
	}

	// Skill 加载器:多来源发现(兼容 Claude Code / opencode / cursor / agents 生态)。
	// workspace 优先于 global,同名覆盖。任一目录不存在不报错。
	// 用户没建任何 skill 时 catalog 为空,system prompt 不挂"Available Skills"段。
	home, _ := os.UserHomeDir()
	skill.ExtractBuiltins(home) // 解压内嵌 skill 到 ~/.deepx/skills/
	workspaceSkillDirs := []string{
		filepath.Join(wd, ".deepx", "skills"),
	}
	globalSkillDirs := []string{
		filepath.Join(home, ".agents", "skills"),
		filepath.Join(home, ".claude", "skills"),
		filepath.Join(home, ".deepx", "skills"),
	}
	loader := skill.New(workspaceSkillDirs, globalSkillDirs)
	tools.SetSkillLoader(loader)
	skillCatalog := buildSkillCatalog(loader)

	m := model{
		chatContent:     newChatLog(maxChatBytes),
		currentReply:    &strings.Builder{},
		chatViewport:    vp,
		input:           ti,
		models:          models,
		activeModelRole: role,
		activeModelID:   activeID,
		mode:            agent.AgentMode_Review,
		status:          "idle",
		spinner:         sp,
		workspace:       wd,
		setupInput:      si,
		session:         sess,
		skillLoader:     loader,
		skillCatalog:    skillCatalog,
	}

	// 回填上轮 token 用量,Usage section 启动后立刻显示真实数字而非 "—"。
	if sess != nil {
		if u := sess.LoadUsage(); u != nil {
			m.lastUsage = &agent.UsageInfo{
				PromptTokens:          u.PromptTokens,
				CompletionTokens:      u.CompletionTokens,
				PromptCacheHitTokens:  u.PromptCacheHitTokens,
				PromptCacheMissTokens: u.PromptCacheMissTokens,
			}
		}
	}

	// 恢复会话历史:优先尝试二进制 gob 文件(含 tool_calls/tool_results/reasoning),
	// 失败时回退 JSONL 文本恢复(仅 user/assistant content)。
	if sess != nil {
		var gobOK bool
		var gobHistory []agent.ChatMessage
		if err := sess.LoadGob("history.gob", &gobHistory); err == nil && len(gobHistory) > 0 {
			gobOK = true
			// 如果首条是旧 system prompt,替换为当前版本,确保
			// system prompt 文本或 skill 目录变化后 LLM 仍看到最新内容。
			if gobHistory[0].Role == "system" {
				gobHistory[0].Content = agent.BuildSystemPrompt(m.workspace, m.skillCatalog)
			}
			m.history = gobHistory
			rebuildChatFromHistory(m.chatContent, gobHistory)
			// 提示行独立成段 —— chatLog 预算紧张时优先丢更旧的内容,这条标签反正是装饰。
			m.chatContent.Open("---\n_(已恢复完整会话)_\n\n")
			// 如果有会话压缩摘要,更新显示用 totalTurns
			if len(gobHistory) > 0 && gobHistory[0].Role == "assistant" &&
				strings.HasPrefix(gobHistory[0].Content, "## 会话摘要") {
				// summary 已在内,不用额外处理
			}
		}

		if !gobOK {
			summary, totalTurns := sess.LoadSummary()
			if summary != "" && totalTurns > 0 {
				// totalTurns 是压缩后保留的轮数，直接用它加载。
				entries := sess.LoadRecentTurns(totalTurns)

				// 摘要作为第一条 assistant 消息
				summaryMsg := "## 会话摘要\n" + summary
				m.history = append(m.history, agent.ChatMessage{Role: "assistant", Content: summaryMsg})
				m.chatContent.Open(rolePrefix("assistant") + summaryMsg + "\n\n---\n\n")

				for _, e := range entries {
					m.history = append(m.history, agent.ChatMessage{
						Role:    e.Role,
						Content: e.Content,
					})
					role := "deepx"
					if e.Role == "user" {
						role = "You"

					}
					m.chatContent.Open(rolePrefix(role) + e.Content + "\n\n")
				}
				if len(entries) > 0 {
					m.chatContent.Open("---\n_(以上为历史对话,共 " +
						strconv.Itoa(len(entries)) + " 条)_\n\n")
				}
			} else {
				// 无压缩摘要:按对加载
				const resumeTurns = 20
				entries := sess.LoadRecentTurns(resumeTurns)
				for _, e := range entries {
					m.history = append(m.history, agent.ChatMessage{
						Role:    e.Role,
						Content: e.Content,
					})
					role := "deepx"
					if e.Role == "user" {
						role = "You"

					}
					m.chatContent.Open(rolePrefix(role) + e.Content + "\n\n")
				}
				if len(entries) > 0 {
					m.chatContent.Open("---\n_(以上为历史对话,共 " +
						strconv.Itoa(len(entries)) + " 条)_\n\n")
				}
			}

			// 声明当前模式,通知 LLM 当前状态。模式始终从 review 起步。
			// 注意:gob 恢复时跳过此步骤 — 历史已包含之前的 mode notification,
			// 重复追加会在每次重启时累积,污染 LLM 上下文。
			msg := modeNotification(m.mode, m.activeModelRole)
			m.history = append(m.history, agent.ChatMessage{Role: "assistant", Content: msg})
			m.appendChat("assistant", msg)
			if m.session != nil {
				_ = m.session.Append("assistant", msg)
			}
		}
	}

	// 首次启动:强制弹 modal,焦点在 setup 输入框
	if needsSetup {
		m.showSetup = true
		m.setupRequired = true
		m.setupInput.Focus()
	} else {
		m.input.Focus()
	}

	// endpoint / 模型 / 模式信息全部移到右栏(rightPanelView 直接读 m.models / m.baseURL),
	// chat 区不再发开场 System 消息,保持干净
	return m
}

func (m model) Init() tea.Cmd {
	return textinput.Blink
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		leftW, vpH := m.layout()
		m.chatViewport.SetWidth(leftW)
		m.chatViewport.SetHeight(vpH)
		m.input.SetWidth(leftW - 4)
		// 窗口尺寸变了 → wrap 重算 → 老 line 号失效,必须清选区
		m.selecting = false
		m.refreshViewport()

	case tea.MouseWheelMsg:
		// modal 期间忽略
		if m.showSetup {
			return m, nil
		}
		// 滚轮: 转给 viewport,顺便取消选区
		if m.selecting {
			m.selecting = false
		}
		var c tea.Cmd
		m.chatViewport, c = m.chatViewport.Update(msg)
		return m, c

	case tea.MouseClickMsg:
		if m.showSetup {
			return m, nil
		}
		if msg.Button != tea.MouseLeft {
			return m, nil
		}
		leftW, vpH := m.layout()
		chatLeft, chatTop := 1, 3
		chatRight := chatLeft + leftW
		chatBottom := chatTop + vpH
		scrollbarX := chatRight
		inputRow := chatTop + vpH // 输入框那一行 (chat 高 vpH,紧贴下面)
		inChat := msg.X >= chatLeft && msg.X < chatRight &&
			msg.Y >= chatTop && msg.Y < chatBottom
		onScrollbar := msg.X == scrollbarX &&
			msg.Y >= chatTop && msg.Y < chatBottom
		inInputRow := msg.Y == inputRow && msg.X >= chatLeft && msg.X <= chatRight

		// 输入框双击切换全选(模拟 GUI 双击全选词,但因为输入框单行,这里直接全选整行)。
		// 第一次 click 记下时间戳;400ms 内第二次 click 在同一行 → toggle inputAllSelected。
		if inInputRow {
			now := time.Now()
			if !m.lastInputClickAt.IsZero() && now.Sub(m.lastInputClickAt) < 400*time.Millisecond {
				if m.input.Value() != "" {
					m.inputAllSelected = !m.inputAllSelected
				}
				m.lastInputClickAt = time.Time{} // 清零,避免三击当成第二次双击
			} else {
				m.lastInputClickAt = now
				// 单击进入输入框区域时,若有 chat 区选区高亮 → 清掉,焦点回输入框
				if m.selecting {
					m.selecting = false
					m.refreshViewport()
				}
			}
			return m, nil
		}

		if onScrollbar {
			m.scrollbarDragging = true
			m.scrollbarSeek(msg.Y, chatTop, vpH)
			m.refreshViewport()
		} else if inChat {
			col := msg.X - chatLeft
			line := m.chatViewport.YOffset() + (msg.Y - chatTop)
			m.selAnchor = cellPos{col: col, line: line}
			m.selEnd = m.selAnchor
			m.selecting = true
			m.refreshViewport()
		} else if m.selecting {
			m.selecting = false
			m.refreshViewport()
		}
		return m, nil

	case tea.MouseMotionMsg:
		if m.showSetup {
			return m, nil
		}
		if msg.Button != tea.MouseLeft {
			return m, nil
		}
		leftW, vpH := m.layout()
		chatLeft, chatTop := 1, 3
		chatRight := chatLeft + leftW
		chatBottom := chatTop + vpH

		if m.scrollbarDragging {
			m.scrollbarSeek(msg.Y, chatTop, vpH)
			m.refreshViewport()
		} else if m.selecting {
			scrolled := false
			if msg.Y < chatTop && !m.chatViewport.AtTop() {
				m.chatViewport.ScrollUp(1)
				scrolled = true
			} else if msg.Y >= chatBottom && !m.chatViewport.AtBottom() {
				m.chatViewport.ScrollDown(1)
				scrolled = true
			}
			cx := msg.X
			if cx < chatLeft {
				cx = chatLeft
			}
			if cx >= chatRight {
				cx = chatRight - 1
			}
			cy := msg.Y
			if cy < chatTop {
				cy = chatTop
			}
			if cy >= chatBottom {
				cy = chatBottom - 1
			}
			col := cx - chatLeft
			line := m.chatViewport.YOffset() + (cy - chatTop)
			if scrolled || m.selEnd.col != col || m.selEnd.line != line {
				m.selEnd = cellPos{col: col, line: line}
				m.refreshViewport()
			}
		}
		return m, nil

	case tea.MouseReleaseMsg:
		if m.showSetup {
			return m, nil
		}
		if msg.Button != tea.MouseLeft {
			return m, nil
		}
		if m.scrollbarDragging {
			m.scrollbarDragging = false
		} else if m.selecting {
			text := m.collectSelectionText()
			if text != "" {
				_ = writeClipboardText(text)
			}
			// 不清 selecting:保留高亮,直到用户点别处 / 滚轮 / 改尺寸 / 开始新选择
		}
		return m, nil

	case tea.PasteMsg:
		// 配置 modal 期间,转发给 setupInput(允许粘贴 API key)
		if m.showSetup {
			var c tea.Cmd
			m.setupInput, c = m.setupInput.Update(msg)
			return m, c
		}
		// 空 paste 内容 = 终端有 paste 事件但 PTY 里没文本 → 大概率剪贴板只有图片,主动读 PNG。
		// 非空 paste 内容 = 普通文本粘贴,放掉让 textinput 自己接(它有 PasteMsg 处理)。
		if msg.Content == "" {
			if m.inputAllSelected {
				m.input.SetValue("")
				m.attachedImagePaths = nil
				m.inputAllSelected = false
			}
			if data, err := readClipboardImage(); err == nil {
				path, ferr := saveAttachedImage(data, len(m.attachedImagePaths)+1)
				if ferr != nil {
					m.appendChat("System", "保存粘贴图片失败: "+ferr.Error())
					return m, nil
				}
				m.attachedImagePaths = append(m.attachedImagePaths, path)
				m.insertImagePlaceholder(len(m.attachedImagePaths))
			}
			return m, nil
		}
		// 文本粘贴:让 textinput 处理
		var c tea.Cmd
		m.input, c = m.input.Update(msg)
		return m, c

	case tea.KeyPressMsg:
		// 配置 modal 处于活动状态时,按键全部路由到 setupInput,绕过主界面所有处理。
		if m.showSetup {
			switch msg.String() {
			case "ctrl+c":
				return m, tea.Quit
			case "enter":
				cmd := m.submitSetup()
				return m, cmd
			case "esc":
				// 首次启动 modal 不允许 Esc 关闭(还没有配置可用);
				// 否则正常关闭返回主界面。
				if m.setupRequired {
					m.setupErr = "需先填入 API key 才能继续 (Ctrl+C 退出 deepx)"
					return m, nil
				}
				m.showSetup = false
				m.setupErr = ""
				m.setupInput.Reset()
				m.setupInput.Blur()
				m.input.Focus()
				return m, nil
			}
			var c tea.Cmd
			m.setupInput, c = m.setupInput.Update(msg)
			return m, c
		}

		// review 审核态:↑/↓ 切换 YES/NO, Enter 确认, Esc 拒绝
		if m.reviewPending {
			switch msg.String() {
			case "up", "down":
				m.reviewYesNo = !m.reviewYesNo
				m.refreshViewport()
				return m, nil
			case "enter":
				m.reviewCh <- m.reviewYesNo
				m.reviewPending = false
				m.refreshViewport()
				return m, func() tea.Msg {
					return reviewResultMsg{}
				}
			case "esc", "ctrl+c":
				m.reviewCh <- false
				m.reviewPending = false
				m.refreshViewport()
				return m, func() tea.Msg {
					return reviewResultMsg{}
				}
			}
			return m, nil
		}

		// 命令 palette 导航键拦截。palette 由 input value 的 "/" 前缀触发,
		// 这里只在 palette 打开时消费 ↑/↓/Tab,其他键继续往下走交给 textinput。
		if matches := filterSlashCommands(m.input.Value()); len(matches) > 0 {
			// clamp idx 避免 value 缩短后越界
			if m.commandPaletteIdx >= len(matches) {
				m.commandPaletteIdx = len(matches) - 1
			}
			if m.commandPaletteIdx < 0 {
				m.commandPaletteIdx = 0
			}
			switch msg.String() {
			case "up":
				if m.commandPaletteIdx > 0 {
					m.commandPaletteIdx--
				}
				return m, nil
			case "down":
				if m.commandPaletteIdx < len(matches)-1 {
					m.commandPaletteIdx++
				}
				return m, nil
			case "tab":
				// 用选中命令替换 input value;光标移到末尾,palette 仍会因 value 完全等于命令
				// 而匹配它自己一条(只剩 1 项),用户可以再按 Enter 执行,或者继续编辑加参数
				m.input.SetValue(matches[m.commandPaletteIdx].name)
				m.input.SetCursor(len(matches[m.commandPaletteIdx].name))
				m.commandPaletteIdx = 0
				return m, nil
			case "enter":
				// palette 打开时 Enter = 直接执行当前选中命令。
				// 不走原 enter 分支的 input value(可能只是 "/sk" 这种半成品 → handleSlashCommand
				// 会报"未知命令")。先把 value 清掉,再执行完整命令。
				chosen := matches[m.commandPaletteIdx].name
				m.input.SetValue("")
				m.commandPaletteIdx = 0
				m.handleSlashCommand(chosen)
				return m, nil
			}
		} else {
			// palette 没在显示,idx 复位避免下次打开时停在过去位置
			m.commandPaletteIdx = 0
		}

		// Ctrl+A 全选态预处理:按下 Ctrl+A 后,任何其他按键都要先消费"全选"语义。
		// 放在外层 switch 之前,确保所有后续 case 都看不到带 selected 的状态。
		if m.inputAllSelected {
			switch msg.String() {
			case "ctrl+shift+a", "super+shift+a":
				return m, nil // 已全选,继续全选
			case "esc":
				m.inputAllSelected = false
				return m, nil
			case "ctrl+c":
				// 保留原"非流式时退出"语义,不被 selected 改变
			case "left", "right", "home", "end", "up", "down",
				"pgup", "pgdown", "pageup", "pagedown":
				// 方向 / 翻页 → 仅取消选择,光标移动交给后续 textinput.Update
				m.inputAllSelected = false
			case "backspace", "delete", "ctrl+w", "ctrl+u":
				m.input.SetValue("")
				m.attachedImagePaths = nil
				m.inputAllSelected = false
				return m, nil
			case "enter":
				// 全选状态按 Enter:按原 enter 流程走(发送当前 value),同时清除选择标记
				m.inputAllSelected = false
				// 不 return,继续走外层 enter case
			default:
				// 可打印字符或其他键:先清空 value,再让 textinput 处理这次按键(就把新字符填入空 value)
				m.input.SetValue("")
				m.attachedImagePaths = nil
				m.inputAllSelected = false
			}
		}
		switch msg.String() {
		// 输入框全选。两个 key 都注册:
		//   - "ctrl+shift+a"  — 跨平台通用,跟 Claude Code 一致 (issue #52912)
		//   - "super+shift+a" — macOS Cmd+Shift+A,Kitty Keyboard Protocol 透传 (iTerm2 /
		//     kitty / WezTerm / Ghostty 默认未占用此组合,自然送达;Terminal.app 不支持
		//     Kitty Protocol,那边用户用 Ctrl+Shift+A 即可)
		//
		// 不用 Cmd+A / Ctrl+A 是因为前者被 macOS 终端 GUI 拦截做"全选窗口文本"、
		// 字节不进 PTY;后者是 readline LineStart 的默认绑定 — Anthropic 的 Claude Code
		// (issue #14789 not_planned) 也确认 Cmd+A 物理不可达,所有 TUI 都用 Ctrl 组合键。
		case "ctrl+shift+a", "super+shift+a":
			if m.input.Value() != "" {
				m.inputAllSelected = true
			}
			return m, nil
		case "ctrl+c":
			// Ctrl+C 退出程序。若正在流式,先取消后台任务。
			if m.streaming {
				if m.cancelAgent != nil {
					m.cancelAgent()
					m.cancelAgent = nil
				}
				if m.streamCh != nil {
					drainAndDiscard(m.streamCh)
				}
			}
			return m, tea.Quit
		case "esc":
			// Esc 中断当前对话。取消 context 真正终止后台 HTTP 请求和工具调用,
			// 然后 drain channel 防止 goroutine 阻塞。
			if m.streaming && m.streamCh != nil {
				if m.cancelAgent != nil {
					m.cancelAgent()
					m.cancelAgent = nil
				}
				drainAndDiscard(m.streamCh)
				m.streamCh = nil
				m.streaming = false
				m.thinking = false
				m.status = "idle"
				m.chatContent.Append("\n\n_已中断_\n\n")
				m.refreshViewport()
				return m, nil
			}
			return m, nil
		case "up", "down", "pgup", "pgdown", "pageup", "pagedown", "home", "end", "ctrl+u", "ctrl+d":
			var c tea.Cmd
			m.chatViewport, c = m.chatViewport.Update(msg)
			return m, c
		case "ctrl+v":
			// 先看剪贴板有没有图,有就落盘 + 把 [Image #N] 占位符插到输入框光标处。
			// 没图则继续下落到 textinput,走文本粘贴。
			if data, err := readClipboardImage(); err == nil {
				path, err := saveAttachedImage(data, len(m.attachedImagePaths)+1)
				if err != nil {
					m.appendChat("System", "保存粘贴图片失败: "+err.Error())
					return m, nil
				}
				m.attachedImagePaths = append(m.attachedImagePaths, path)
				m.insertImagePlaceholder(len(m.attachedImagePaths))
				return m, nil
			}
		case "enter":
			if m.streaming {
				return m, nil
			}
			input := strings.TrimSpace(m.input.Value())
			if input == "" && len(m.attachedImagePaths) == 0 {
				return m, nil
			}

			// 斜杠命令:仅匹配已知命令,粘贴的路径类文本不误触
			if matches := filterSlashCommands(input); len(matches) > 0 {
				m.input.SetValue("")
				m.handleSlashCommand(matches[0].name)
				return m, nil
			}

			userMsg := m.buildUserMessage(input)
			// 聊天窗口里仍显示用户输入的原文 (含占位符),路径替换只发生在发给 LLM 的消息体中。
			m.appendChat("You", input)
			m.history = append(m.history, userMsg)
			// 持久化用户输入到 session 文件。占位符版本(input)而非含完整路径的 LLM 体 —— 重加时
			// 路径已失效但占位符语义清晰;memory 检索时关键词也按用户原话更直观。
			if m.session != nil {
				_ = m.session.Append("user", input)
			}
			m.attachedImagePaths = nil
			m.input.SetValue("")

			m.status = "thinking"
			m.streaming = true
			m.thinking = true
			m.currentReply.Reset()

			// 仪表盘:开计时,快照本轮输入字符数(用 history 总长近似 = 上下文输入),输出归零
			m.turnStartedAt = time.Now()
			m.turnInputChars = sumHistoryChars(m.history)
			m.turnOutputChars = 0

			m.chatContent.Open(deepxPrefix)
			m.refreshViewport()
			// 启动 spinner ticking
			cmds = append(cmds, m.spinner.Tick)

			// 每次新用户消息开始,角色重置回 flash (起手就是默认 flash)。
			// agent.StartStream 内部根据 keyword router 决定本轮真实模型,然后通过 ModelSwitchMsg 通知。
			m.activeModelRole = "flash"
			m.activeModelID = m.models.Flash.Model
			if m.activeModelID == "" {
				m.activeModelRole = "pro"
				m.activeModelID = m.models.Pro.Model
			}
			// 上一轮的 plan 清空,避免残留状态混淆当前任务
			m.plan = nil

			workspace, _ := os.Getwd()
			ctx, cancel := context.WithCancel(context.Background())
			m.cancelAgent = cancel
			cmd, ch := agent.StartStream(
				ctx,
				m.models,
				m.history, maxTokens,
				m.mode,
				workspace,
				m.skillCatalog,
			)
			m.streamCh = ch
			cmds = append(cmds, cmd)
			return m, tea.Batch(cmds...)
		}

	case agent.ReasoningTokenMsg:
		// streamCh 已 nil 说明 ESC/Ctrl+C 中断过了,丢弃残留消息
		if m.streamCh == nil {
			return m, nil
		}
		// 模型在思考。文字不进 chat,只确保 spinner 在转。
		m.status = "thinking"
		if !m.thinking {
			m.thinking = true
			cmds = append(cmds, m.spinner.Tick)
		}
		return m, tea.Batch(append(cmds, agent.ListenToStream(m.streamCh))...)

	case agent.TokenMsg:
		if m.streamCh == nil {
			return m, nil
		}
		// 助手正式回复开始,停止 spinner,把文本写进 chat
		m.status = "streaming"
		m.thinking = false
		text := string(msg)
		m.currentReply.WriteString(text)
		m.chatContent.Append(text)
		m.tokens += len([]rune(text))
		m.turnOutputChars += len([]rune(text))
		m.refreshViewport()
		return m, agent.ListenToStream(m.streamCh)

	case agent.ToolCallStartMsg:
		if m.streamCh == nil {
			return m, nil
		}
		// 工具调用 = 一次"动作",紧凑单行展示:<icon> Name (主参数)
		m.status = "tool"
		line := formatToolCallLine(msg.Name, msg.Args)
		// 关键:tool 行前必须落到 markdown 的"段落起点"。CommonMark 单个 \n 是 soft break,
		// glamour 渲染成空格 → "**🐋 deepx**: \n🐚 Bash ..." 就被拼回同一行。
		// 按 chatContent 现有结尾决定补几个 \n:
		//   - 已经以 \n 结尾(典型:上一次 tool 行写完):再补 1 个 → 凑成 \n\n 段落分隔
		//   - 不以 \n 结尾(典型:首次工具调用紧跟 deepxPrefix,或 stream 完正文后再调工具):
		//     补 2 个 → 强制段落分隔,避免被吸到上一行
		sep := "\n\n"
		if m.chatContent.EndsWithNewline() {
			sep = "\n"
		}
		m.chatContent.Append(sep + line + "\n")
		m.refreshViewport()
		// review 模式:暂停流,等待用户确认
		if msg.ReviewCh != nil {
			m.reviewPending = true
			m.reviewCh = msg.ReviewCh
			m.reviewToolName = msg.Name
			m.reviewToolArgs = msg.Args
			m.reviewYesNo = true // 默认 YES
			return m, nil
		}
		// 工具执行期间继续转 spinner,等结果回来后才可能切到 content
		if !m.thinking {
			m.thinking = true
			cmds = append(cmds, m.spinner.Tick)
		}
		return m, tea.Batch(append(cmds, agent.ListenToStream(m.streamCh))...)

	case agent.ToolCallResultMsg:
		if m.streamCh == nil {
			return m, nil
		}
		// 结果不再原样打印长输出。失败时短提示一行;成功默默吞掉(LLM 会用结果接着干)。
		// 这样多轮工具调用看到的就是清爽的工具列表,不被几百行输出淹没。
		if !msg.Success {
			out := msg.Output
			if len(out) > 200 {
				out = out[:200] + "…"
			}
			m.chatContent.Append("  ✗ " + msg.Name + " 失败: " + out + "\n")
		}
		m.currentReply.Reset()
		m.refreshViewport()
		// 工具结果后,LLM 还要继续思考(或最终回复),保持 spinner 状态
		if !m.thinking {
			m.thinking = true
			cmds = append(cmds, m.spinner.Tick)
		}
		return m, tea.Batch(append(cmds, agent.ListenToStream(m.streamCh))...)

	case spinner.TickMsg:
		// spinner 自驱动:每次 tick 来都更新 frame,只要还在 thinking 就继续 Tick
		var c tea.Cmd
		m.spinner, c = m.spinner.Update(msg)
		if m.thinking {
			cmds = append(cmds, c)
		}
		// thinking 状态下需要重渲染让 spinner 帧切换可见
		m.refreshViewport()
		return m, tea.Batch(cmds...)

	case agent.HistoryUpdateMsg:
		if m.streamCh == nil {
			return m, nil
		}
		m.history = msg.History
		// 持久化完整 history(含 tool_calls / tool results)到 binary gob 文件,
		// 重启时可直接反序列化恢复,无需重建。
		if m.session != nil {
			_ = m.session.SaveGob("history.gob", m.history)
		}
		return m, agent.ListenToStream(m.streamCh)

	case agent.ModelSwitchMsg:
		if m.streamCh == nil {
			return m, nil
		}
		m.activeModelRole = msg.Role
		m.activeModelID = msg.ModelID
		if msg.Reason != "" {
			// 升级类的切换在聊天流里留一行可见痕迹,便于用户察觉为什么变贵了
			m.chatContent.Append(fmt.Sprintf("\n[已升级到 %s 模型] 原因: %s\n", msg.Role, msg.Reason))
			m.refreshViewport()
		}
		return m, agent.ListenToStream(m.streamCh)

	case agent.PlanCreatedMsg:
		if m.streamCh == nil {
			return m, nil
		}
		m.plan = &planState{items: msg.Plans}
		// 不写 chatContent — plan 通过 refreshViewport 的 live overlay 实时渲染,
		// 每次 TaskStatusMsg 自然刷新出新 checkbox。
		// 流结束时(StreamDoneMsg)再固化一次进 chatContent 留作历史。
		m.refreshViewport()
		return m, agent.ListenToStream(m.streamCh)

	case agent.TaskStatusMsg:
		if m.streamCh == nil {
			return m, nil
		}
		// 只有 plan 已就位才有意义;否则可能是模型乱调,丢弃
		if m.plan != nil {
			m.plan.apply(msg)
			m.refreshViewport() // 触发 live overlay 重渲染,checkbox 立刻更新
		}
		return m, agent.ListenToStream(m.streamCh)

	case agent.UsageMsg:
		if m.streamCh == nil {
			return m, nil
		}
		// 主 agent 单次 API 用量,仅记录最新一轮(子 agent 的调用不发送 UsageMsg)。
		m.lastUsage = &msg.Usage
		// 持久化到 state.json,重启后立刻就有数据,Usage section 不再显示 "—"。
		if m.session != nil {
			m.session.SaveUsage(
				msg.Usage.PromptTokens,
				msg.Usage.CompletionTokens,
				msg.Usage.PromptCacheHitTokens,
				msg.Usage.PromptCacheMissTokens,
			)
		}
		return m, agent.ListenToStream(m.streamCh)

	case agent.StreamDoneMsg:
		if m.streamCh == nil {
			return m, nil
		}
		// 流结束时把当前 plan 最终状态固化进 chatContent,这样滚回历史还能看到。
		// 在写 "\n\n" 之前固化,plan 留在本轮回复结尾。
		if m.plan != nil {
			m.chatContent.Append("\n" + renderPlanForChat(m.plan))
		}
		m.chatContent.Append("\n\n")
		// 持久化助手最终回复。currentReply 在流式过程中累加,这里一次性落盘。
		// 注意只存"主对话内容" —— tool_call / tool_result / reasoning 都不进 session 文件。
		if m.session != nil {
			final := m.currentReply.String()
			if strings.TrimSpace(final) != "" {
				_ = m.session.Append("assistant", final)
			}
		}
		m.status = "idle"
		m.streaming = false
		m.thinking = false
		m.streamCh = nil
		m.cancelAgent = nil
		m.turnElapsed = time.Since(m.turnStartedAt)
		m.refreshViewport()

		// 显示区按字节预算自动裁剪 (chatLog.Append/Open 内部已调 trim),
		// 这里无需额外动作 — 旧的 trimDisplayTurns 按"10 轮"裁的逻辑已被 chatLog 取代。

		// 检查是否需要触发会话压缩：估算 token 数接近窗口的 70% 时触发。
		ctxWin := m.models.Pro.ContextWindow
		if ctxWin <= 0 {
			ctxWin = 65536
		}
		if m.session != nil && m.models.Pro.Model != "" && estimateTokens(sumHistoryChars(m.history)) >= ctxWin*70/100 {
			// 拷贝当前 history 快照,异步执行压缩
			snapshot := make([]agent.ChatMessage, len(m.history))
			copy(snapshot, m.history)
			pro := m.models.Pro
			return m, func() tea.Msg {
				summary, cutIdx, compressedTurns, err := runCompression(snapshot, pro, ctxWin)
				return compressionResultMsg{
					summary:         summary,
					cutIdx:          cutIdx,
					compressedTurns: compressedTurns,
					err:             err,
				}
			}
		}

		return m, nil

	case agent.StreamErrMsg:
		if m.streamCh == nil {
			return m, nil
		}
		m.chatContent.Append("\n[Error: " + msg.Err.Error() + "]\n\n")
		m.status = "error"
		m.streaming = false
		m.thinking = false
		m.streamCh = nil
		m.cancelAgent = nil
		m.turnElapsed = time.Since(m.turnStartedAt)
		m.refreshViewport()
		return m, nil

	case reviewResultMsg:
		// 审核完成,恢复流监听继续工具循环
		return m, agent.ListenToStream(m.streamCh)

	case compressionResultMsg:
		if msg.err != nil {
			m.chatContent.Append("\n[会话压缩失败: " + msg.err.Error() + "]\n\n")
			m.refreshViewport()
			return m, nil
		}
		// total_turns reset 为压缩后保留的轮数。后续 Append 从该值递增。
		_, totalTurns := m.session.LoadSummary()
		keptCount := totalTurns - msg.compressedTurns
		summaryMsg := "## 会话摘要\n" + msg.summary
		kept := make([]agent.ChatMessage, 0, len(m.history)-msg.cutIdx+1)
		kept = append(kept, agent.ChatMessage{Role: "assistant", Content: summaryMsg})
		kept = append(kept, m.history[msg.cutIdx:]...)
		m.history = kept

		_ = m.session.SaveSummary(msg.summary, keptCount)
		// 压缩后 history 已截断,写回 gob 保持下轮启动一致性
		if m.session != nil {
			_ = m.session.SaveGob("history.gob", m.history)
		}

		m.chatContent.Open(fmt.Sprintf("\n---\n**已压缩会话历史（%d 轮→摘要）**\n\n", msg.compressedTurns))
		m.refreshViewport()
		return m, nil
	}

	var inputCmd tea.Cmd
	m.input, inputCmd = m.input.Update(msg)
	cmds = append(cmds, inputCmd)

	return m, tea.Batch(cmds...)
}

// === 会话压缩 ===

// compressionPrompt 是压缩历史对话时发给 LLM 的 system prompt。
const compressionPrompt = `你是一个会话历史压缩助手。将对话历史压缩为结构化摘要。

## 摘要需保留
- 用户的任务目标和明确要求（尽量原文保留）
- 已修改的文件及改动目的
- 发现的错误和修复方案
- 架构设计决策
- 未完成的任务和下一步计划

## 可以丢弃
- 重复的调试尝试
- 工具调用的详细输出
- 已解决且不再相关的中间讨论

如果输入中有 [previous summary],将其与新对话合并为一个连贯摘要。

## 输出格式
[自然语言摘要]

最后模式: plan/auto`

// runCompression 执行一次会话压缩：按 context_window × 20% 保留尾部上下文。
// 在 bubbletea Cmd goroutine 中调用。
func runCompression(history []agent.ChatMessage, pro agent.ModelEntry, ctxWin int) (
	summary string, cutIdx int, compressedTurns int, err error) {

	hasPrevSummary := len(history) > 0 &&
		history[0].Role == "assistant" &&
		strings.HasPrefix(history[0].Content, "## 会话摘要")
	startOffset := 0
	if hasPrevSummary {
		startOffset = 1
	}

	// 从尾部按 token 估算保留 context_window × 20% 的上下文。
	totalUsers := 0
	for _, m := range history[startOffset:] {
		if m.Role == "user" {
			totalUsers++
		}
	}
	if totalUsers <= 2 {
		return "", 0, 0, fmt.Errorf("user 轮数不足,无需压缩")
	}

	keepTarget := ctxWin * 20 / 100
	keepStart := len(history)
	charCount := 0
	for i := len(history) - 1; i >= startOffset; i-- {
		charCount += len([]rune(history[i].Content))
		if history[i].Role == "user" {
			if charCount/3 >= keepTarget {
				keepStart = i
				break
			}
		}
	}

	// 触发条件（≥70% ctxWin）保证历史足够达到 20% 保留目标,
	// 此处兜底以防极端情况。
	if keepStart == len(history) {
		return "", 0, 0, fmt.Errorf("上下文不足,跳过压缩")
	}

	cutIdx = keepStart

	// 构造 LLM 输入。
	var inputBuf strings.Builder
	if hasPrevSummary {
		inputBuf.WriteString("[previous summary]\n" + history[0].Content + "\n\n")
	}
	lastMode := "auto"
	compressedUserCount := 0
	for _, m := range history[startOffset:keepStart] {
		if m.Role == "user" {
			compressedUserCount++
		}
		if m.Role == "assistant" && strings.Contains(m.Content, "当前模式: plan") {
			lastMode = "plan"
		}
		if m.Role == "assistant" && strings.Contains(m.Content, "当前模式: auto") {
			lastMode = "auto"
		}
		inputBuf.WriteString("[" + m.Role + "]\n" + m.Content + "\n\n")
	}

	compressedTurns = compressedUserCount

	convo := []agent.ChatMessage{
		{Role: "system", Content: compressionPrompt},
		{Role: "user", Content: inputBuf.String()},
	}

	summaryMax := ctxWin * 3 / 100
	if summaryMax < 256 {
		summaryMax = 256 // 最小 256 tok，避免太小失去摘要意义
	}
	summary, err = agent.CallOnce(context.Background(), pro.APIKey, pro.BaseURL, pro.Model, convo, summaryMax)
	if err != nil {
		return "", 0, 0, err
	}
	if !strings.Contains(summary, "最后模式:") {
		summary += "\n最后模式: " + lastMode
	}

	return summary, cutIdx, compressedTurns, nil
}

// buildUserMessage 把输入文本中的 [Image #N] 占位符替换成已落盘的图片绝对路径,
// 让不支持多模态输入的 LLM (例如 DeepSeek) 也能拿到图片信息——
// 模型可通过 img_ocr 工具按路径识别图片内容。
//
// 设计:图片在 Ctrl+V 时就落盘到 ~/.deepx/ocr/cache/<ts>-<N>.png,
// 这里只做"占位符 → 路径"的文本替换,不再产出多模态 ContentParts。
// 用户手动删掉的占位符对应的图片不会出现在最终消息里。
func (m model) buildUserMessage(text string) agent.ChatMessage {
	if len(m.attachedImagePaths) == 0 {
		return agent.ChatMessage{Role: "user", Content: text}
	}
	replaced := imagePlaceholderRe.ReplaceAllStringFunc(text, func(match string) string {
		// match 形如 "[Image #1]";提取数字索引
		sub := imagePlaceholderRe.FindStringSubmatch(match)
		if len(sub) < 2 {
			return match
		}
		idx, _ := strconv.Atoi(sub[1])
		if idx < 1 || idx > len(m.attachedImagePaths) {
			return match
		}
		return m.attachedImagePaths[idx-1]
	})
	return agent.ChatMessage{Role: "user", Content: replaced}
}

// saveAttachedImage 把刚粘贴的 PNG 字节写到 ~/.deepx/ocr/cache/ 下的新文件,
// 返回该文件的绝对路径。失败时返回空串 + 错误,调用方决定怎么提示用户。
func saveAttachedImage(data []byte, index int) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.TempDir()
	}
	cacheDir := filepath.Join(home, ".deepx", "ocr", "cache")
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return "", err
	}
	// 时间戳精确到纳秒,避免连续粘贴的文件名冲突
	name := fmt.Sprintf("%s-%d.png", time.Now().Format("20060102-150405.000000000"), index)
	path := filepath.Join(cacheDir, name)
	if err := os.WriteFile(path, data, 0644); err != nil {
		return "", err
	}
	return path, nil
}

// insertImagePlaceholder 在输入框当前光标位置插入 [Image #n],
// 必要时在前后补空格,避免和相邻字符黏在一起。
func (m *model) insertImagePlaceholder(n int) {
	placeholder := fmt.Sprintf("[Image #%d]", n)
	cur := []rune(m.input.Value())
	pos := m.input.Position()
	if pos < 0 || pos > len(cur) {
		pos = len(cur)
	}
	prefix := ""
	if pos > 0 && cur[pos-1] != ' ' {
		prefix = " "
	}
	suffix := ""
	if pos < len(cur) && cur[pos] != ' ' {
		suffix = " "
	}
	ins := []rune(prefix + placeholder + suffix)
	m.input.SetValue(string(cur[:pos]) + string(ins) + string(cur[pos:]))
	m.input.SetCursor(pos + len(ins))
}

// rolePrefix 把内部 role 名映射成 chatContent 里要写入的 markdown 前缀。
// glamour 渲染时 **...** 会变粗体,emoji 给视觉锚点,跨终端比 ANSI 着色稳。
// 未知 role 走兜底 "**role**: ",不会丢字。
func rolePrefix(role string) string {
	role = strings.ToLower(role)
	switch role {
	case "you":
		return userPrefix
	case "system":
		return systemPrefix
	case "deepx", "assistant":
		return deepxPrefix
	default:
		return "**" + role + "**: "
	}
}

func (m *model) appendChat(role, text string) {
	m.chatContent.Open(rolePrefix(role) + text + "\n\n")
	m.refreshViewport()
}

// rebuildChatFromHistory 把完整 []ChatMessage 按"显示块"粒度写入 chatLog。
//
// **为什么不返回一个大字符串再 Open 一次**:chatLog.trim 是按 segment 粒度丢最旧段的,
// 且 `len(segments) > 1` 才会触发裁剪。如果整个历史塞进单一 segment,首次新消息一旦
// 新开 segment 就把整段历史(可能数百 KB)作为最旧段一次性丢掉 —— 用户体验就是
// "重启看得到历史,发一条新消息历史全没了"。
// 改成每条消息(及 assistant 的每个 tool_call、每条 tool result)一次 Open 后,
// trim 能从最旧消息逐条丢,屏幕上保留尽可能多的近期上下文。
//
// 显示规则:
//   - user: 非空 content 显示为用户消息
//   - assistant: 非空 content 显示为助手回复;有 ToolCalls 时每个调用各自渲染成一段
//   - tool: **跳过** —— 跟直播流对齐。运行时 ToolCallResultMsg 成功时静默
//     (model.go:820 附近),仅失败写 `  ✗ ...`。但 history 里的 tool message 不带
//     成功/失败标记,无法在恢复时区分,索性全部不渲染。结果是否成功用户能从
//     紧随其后的 assistant 回复推断 —— 不影响理解,且跟运行时观感一致。
//   - system: 跳过
//
// 不写 m.history、不写 session.gob —— 仅是显示通道,改这里不影响 LLM 缓存。
func rebuildChatFromHistory(cl *chatLog, history []agent.ChatMessage) {
	for _, msg := range history {
		switch msg.Role {
		case "user":
			if msg.Content != "" {
				cl.Open(rolePrefix("You") + msg.Content + "\n\n")
			}
		case "assistant":
			if msg.Content != "" {
				cl.Open(rolePrefix("deepx") + msg.Content + "\n\n")
			}
			// 工具调用行:跟直播流 ToolCallStartMsg 相同的格式
			for _, tc := range msg.ToolCalls {
				line := formatToolCallLine(tc.Function.Name, tc.Function.Arguments)
				cl.Open(line + "\n")
			}
		}
	}
}

func modeNotification(mode agent.AgentMode, modelRole string) string {
	modelPart := ""
	if modelRole != "" {
		modelPart = ", 模型: " + modelRole
	}
	switch mode {
	case agent.AgentMode_Plan:
		return "当前模式: plan" + modelPart
	case agent.AgentMode_Review:
		return "当前模式: review" + modelPart
	default:
		return "当前模式: auto" + modelPart
	}
}

// handleSlashCommand 处理本地斜杠命令。// handleSlashCommand 处理本地斜杠命令。
func (m *model) handleSlashCommand(input string) {
	cmd := strings.ToLower(strings.TrimSpace(input))
	switch cmd {
	case "/plan":
		m.mode = agent.AgentMode_Plan
		msg := modeNotification(agent.AgentMode_Plan, m.activeModelRole)
		m.history = append(m.history, agent.ChatMessage{Role: "assistant", Content: msg})
		m.appendChat("assistant", msg)
		if m.session != nil {
			_ = m.session.Append("assistant", msg)
		}
	case "/auto":
		m.mode = agent.AgentMode_Auto
		msg := modeNotification(agent.AgentMode_Auto, m.activeModelRole)
		m.history = append(m.history, agent.ChatMessage{Role: "assistant", Content: msg})
		m.appendChat("assistant", msg)
		if m.session != nil {
			_ = m.session.Append("assistant", msg)
		}
	case "/review":
		m.mode = agent.AgentMode_Review
		msg := modeNotification(agent.AgentMode_Review, m.activeModelRole)
		m.history = append(m.history, agent.ChatMessage{Role: "assistant", Content: msg})
		m.appendChat("assistant", msg)
		if m.session != nil {
			_ = m.session.Append("assistant", msg)
		}
	case "/mode":
		m.appendChat("assistant", fmt.Sprintf("当前模式: %s", m.mode))
	case "/config":
		m.openSetupModal()
	case "/skills":
		m.appendChat("assistant", m.skillsListMessage())
	case "/help":
		// 走 markdown 列表 — chatContent 经 glamour 渲染,缩进对齐文本会被当 code block 处理,
		// `-` 项目符号更稳。每项格式: "`/cmd` — 说明"。
		m.appendChat("assistant", "\n**Slash 命令**\n\n"+
			"- `/plan` — 切到只读模式(仅 Read / List / Grep / Glob / Tree / Search / Fetch / Memory)\n"+
			"- `/auto` — 切回全工具模式(默认)\n"+
			"- `/review` — 切到审核模式(Write/Update/Bash 需人工确认)\n"+
			"- `/mode` — 显示当前模式\n"+
			"- `/config` — 重新配置 API key (覆盖 `~/.deepx/model.yaml`)\n"+
			"- `/skills` — 列出可用 skill\n"+
			"- `/help` — 帮助\n\n"+
			"**快捷键**\n\n"+
			"- `Enter` — 发送\n"+
			"- `Ctrl+Shift+A` / macOS `Cmd+Shift+A` — 输入框全选\n"+
			"- `Ctrl+V` — 粘贴(含图片)\n"+
			"- `Esc` — 中断当前对话\n"+
			"- `Ctrl+C` — 退出程序")
	default:
		m.appendChat("assistant", fmt.Sprintf("未知命令: %s (输入 /help 查看)", cmd))
	}
}

// === Skill 辅助 ===

// buildSkillCatalog 把所有可用 skill 拼成给 LLM 看的简短目录。
// 格式:每行 "- <name> (<scope>): <description>"。空 loader / 空目录返回空串。
func buildSkillCatalog(l *skill.Loader) string {
	if l == nil {
		return ""
	}
	metas := l.List()
	if len(metas) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, m := range metas {
		desc := m.Description
		if desc == "" {
			desc = "(无 description)"
		}
		fmt.Fprintf(&sb, "- %s (%s): %s\n", m.Name, m.Scope, desc)
	}
	return sb.String()
}

// skillsListMessage 给 /skills slash 命令的输出。
// 列出当前两个目录里所有 skill,显示 scope / name / description / 路径。
// 没有任何 skill 时给个友好引导。
func (m *model) skillsListMessage() string {
	if m.skillLoader == nil {
		return "skill 系统未启用"
	}
	metas := m.skillLoader.List()
	if len(metas) == 0 {
		var dirsBlock strings.Builder
		for _, d := range m.skillLoader.AllDirs() {
			fmt.Fprintf(&dirsBlock, "  - %s\n", d)
		}
		return "暂无 skill。在以下任一目录创建 <name>/SKILL.md 即可:\n" +
			dirsBlock.String() +
			"\nSKILL.md 格式:\n" +
			"  ---\n" +
			"  name: my-skill\n" +
			"  description: 一句话说明做什么 + 何时用\n" +
			"  ---\n" +
			"  <markdown 正文>"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "可用 skill (%d 个):\n\n", len(metas))
	for _, sm := range metas {
		fmt.Fprintf(&sb, "**%s** _(%s)_\n  %s\n  %s\n\n",
			sm.Name, sm.Scope, sm.Description, sm.Path)
	}
	return strings.TrimRight(sb.String(), "\n")
}

// scrollbarSeek 按鼠标 Y 在 chat 高度里的比例,直接 SetYOffset。
// chatTop 是 chat 区首行的屏幕绝对行号,vpH 是 chat 区高度。
func (m *model) scrollbarSeek(mouseY, chatTop, vpH int) {
	total := m.chatViewport.TotalLineCount()
	vis := m.chatViewport.VisibleLineCount()
	if total <= vis || vpH <= 1 {
		return
	}
	rel := mouseY - chatTop
	if rel < 0 {
		rel = 0
	}
	if rel > vpH-1 {
		rel = vpH - 1
	}
	pct := float64(rel) / float64(vpH-1)
	m.chatViewport.SetYOffset(int(pct * float64(total-vis)))
}

// collectSelectionText 从当前 chatContent 抠出选区文本(经过同样的 wrap 再按列裁剪),
// 用于 mouse-release 时写入剪贴板。
func (m *model) collectSelectionText() string {
	w := m.chatViewport.Width()
	if w <= 0 {
		return ""
	}
	content := m.chatContent.String()
	content = ansi.Wrap(content, w, " -")
	return extractSelectionText(content, m.selAnchor, m.selEnd, w)
}

// renderMarkdown 把 chatContent 转成 styled ANSI 输出。
// 关键预处理:ansi.Strip 剥掉 chatContent 里已有的 ANSI 转义(如 deepxPrefix 的着色),
// 避免下游再次被当 literal 处理。代价:丢 prefix 蓝色加粗,但 **bold** 标记会让 prefix 重新粗体。
// 支持: **bold** / *italic* / `code` / ### heading / --- / - list / ``` code blocks。
// 末尾用 ansi.Wrap 按 width 软换行,等价于旧 glamour 的 WithWordWrap。
func (m *model) renderMarkdown(content string, width int) string {
	if width <= 0 || content == "" {
		return content
	}
	content = ansi.Strip(content)

	// 定义 ANSI style 生成器
	bold := lipgloss.NewStyle().Bold(true).Render
	italic := lipgloss.NewStyle().Italic(true).Render
	code := lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Render
	dim := lipgloss.NewStyle().Foreground(dimColor).Render
	heading := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15")).Render
	divider := dim(strings.Repeat("─", width))

	lines := strings.Split(content, "\n")
	var out []string

	for i := 0; i < len(lines); i++ {
		line := lines[i]

		// 代码块: look-ahead 寻找匹配的 close fence。找到才进 code-block 模式,
		// 否则当普通行处理。这样 LLM 输出的未闭合 fence(stream 截断 / hallucinate)
		// 不会让整段后续内容被 dim 当作代码 —— 用户复现过历史会话里 fence 奇数 →
		// 表格、bold 标记全被 dim 字面化的 bug。
		if strings.HasPrefix(line, "~~~") || strings.HasPrefix(line, "```") {
			marker := line[:3]
			// infostring(fence 后的 lang 标签)区分 diff 块,后面按 -/+/@ 上色。
			info := strings.TrimSpace(line[3:])
			closeIdx := -1
			// 扫到下一个 rolePrefix 行或者 marker 闭合;rolePrefix 是消息硬边界,
			// 跨边界的 fence 一律视为未闭合,避免上一条消息污染下一条。
			for j := i + 1; j < len(lines); j++ {
				if isMessagePrefix(lines[j]) {
					break
				}
				if strings.HasPrefix(lines[j], marker) {
					closeIdx = j
					break
				}
			}
			if closeIdx > i {
				isDiff := info == "diff"
				for k := i; k <= closeIdx; k++ {
					if isDiff && k != i && k != closeIdx {
						out = append(out, colorizeDiffLine(lines[k], dim))
					} else {
						out = append(out, dim(lines[k]))
					}
				}
				i = closeIdx
				continue
			}
			// 未闭合:fence 行本身渲染成 dim,但不进入 code-block 状态
			out = append(out, dim(line))
			continue
		}

		empty := strings.TrimSpace(line) == ""

		// 空行:段落分隔
		if empty {
			out = append(out, "")
			continue
		}

		// 分隔线 ---
		if strings.TrimSpace(line) == "---" {
			out = append(out, divider)
			continue
		}

		// GFM table:header 行起手 `|`,下一行是对齐行(`|:---|---:|...|`)。
		// 收集到下一条非 `|` 起手的行作为表格结束,renderTable 用 lipgloss 画 unicode 边框。
		if strings.HasPrefix(strings.TrimSpace(line), "|") && i+1 < len(lines) && isTableSeparator(lines[i+1]) {
			end := i + 2
			for end < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[end]), "|") {
				end++
			}
			out = append(out, renderTable(lines[i:end], bold, italic, code, dim))
			i = end - 1
			continue
		}

		// 标题 ## 或 ###
		trimmed := line
		level := 0
		for strings.HasPrefix(trimmed, "#") {
			level++
			trimmed = strings.TrimLeft(trimmed, "# ")
		}
		if level > 0 && trimmed != "" {
			out = append(out, heading(trimmed))
			continue
		}

		// 列表项 - 或 *
		if len(line) > 2 && (line[0] == '-' || line[0] == '*') && line[1] == ' ' {
			rest := line[2:]
			rest = renderInline(rest, bold, italic, code)
			out = append(out, dim(" • ")+rest)
			continue
		}

		// 普通段落:行内渲染
		rendered := renderInline(line, bold, italic, code)
		out = append(out, rendered)
	}

	// ansi.Wrap 对每行按 width 软换行,保留 ANSI styling。
	// 旧 glamour 用 WithWordWrap 做这事,迁移后必须手动补上,否则长行溢出 viewport。
	return ansi.Wrap(strings.Join(out, "\n"), width, " -")
}

// colorizeDiffLine 按 unified-diff 行首给 ~~~diff 块的内容上色:
//   - `+` 绿(color 10) — 新增
//   - `-` 红(color 9)  — 删除
//   - `@` cyan(color 14)— hunk header (`@@ ... @@`)
//
// 其它行(context 行、`... (N more lines)` 等)走 fallback (调用方传入的 dim)。
// 单独 `+` / `-` / `@` 行视为前缀:用 HasPrefix 而不是首字符匹配,避免空行误判。
func colorizeDiffLine(line string, fallback func(...string) string) string {
	switch {
	case strings.HasPrefix(line, "+"):
		return lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Render(line)
	case strings.HasPrefix(line, "-"):
		return lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Render(line)
	case strings.HasPrefix(line, "@"):
		return lipgloss.NewStyle().Foreground(lipgloss.Color("14")).Render(line)
	default:
		return fallback(line)
	}
}

// isMessagePrefix 判断一行是否以已知 rolePrefix 起手(deepx / 用户 / system / 兜底 role)。
// 用于在 message 边界强制重置 inCodeBlock,避免上一条消息里的未闭合 fence 污染下一条。
// 匹配:`**🐋 deepx**: ...`、`**👤 我**: ...`、`**⚙ System**: ...`、`**<word>**: ...`。
func isMessagePrefix(line string) bool {
	if !strings.HasPrefix(line, "**") {
		return false
	}
	// 在前 60 字节内找 "**:" 或 "**: ",保证 prefix 形态。content 里 **bold**: 这种
	// 巧合命中概率低,且即使误命中也只是多关一次 code block,对正常文本无副作用。
	scan := line
	if len(scan) > 60 {
		scan = scan[:60]
	}
	return strings.Contains(scan[2:], "**:")
}

// splitTableRow 把 `| a | b | c |` 切成 ["a", "b", "c"]。
// 不严格校验 escape(`\|`)— 当前 deepx 场景 LLM 几乎不会输出 escaped pipe。
func splitTableRow(row string) []string {
	row = strings.TrimSpace(row)
	if !strings.HasPrefix(row, "|") {
		return nil
	}
	row = strings.TrimPrefix(row, "|")
	row = strings.TrimSuffix(row, "|")
	return strings.Split(row, "|")
}

// isTableSeparator 判断行是否是 GFM 表格对齐行,如 `|:---|---:|:---:|`。
// 要求每个 cell 只含 `-` 和 `:`,且非空。
func isTableSeparator(row string) bool {
	cells := splitTableRow(row)
	if len(cells) == 0 {
		return false
	}
	for _, c := range cells {
		c = strings.TrimSpace(c)
		if c == "" {
			return false
		}
		for _, r := range c {
			if r != '-' && r != ':' {
				return false
			}
		}
	}
	return true
}

// renderTable 把 GFM table 行渲染成 unicode 框线表格。
// rows[0] 是 header,rows[1] 是对齐行(消费掉、不输出),rows[2:] 是 body。
// 每个 cell 走 renderInline,所以 `**bold**`/`` `code` ``/`*italic*` 在表格里也工作。
// 列宽按 cell 显示宽度的列向最大值取(用 lineDisplayWidth 测,emoji 强制 2 cell)。
func renderTable(rows []string, bold, italic, code, dim func(...string) string) string {
	if len(rows) < 2 {
		return strings.Join(rows, "\n")
	}
	parsed := make([][]string, 0, len(rows))
	for _, row := range rows {
		cells := splitTableRow(row)
		if len(cells) > 0 {
			parsed = append(parsed, cells)
		}
	}
	if len(parsed) < 2 {
		return strings.Join(rows, "\n")
	}

	// 对齐:`:---` left, `---:` right, `:---:` center
	alignCells := parsed[1]
	aligns := make([]string, len(alignCells))
	for i, c := range alignCells {
		c = strings.TrimSpace(c)
		leftBias := strings.HasPrefix(c, ":")
		rightBias := strings.HasSuffix(c, ":")
		switch {
		case leftBias && rightBias:
			aligns[i] = "center"
		case rightBias:
			aligns[i] = "right"
		default:
			aligns[i] = "left"
		}
	}

	data := make([][]string, 0, len(parsed)-1)
	data = append(data, parsed[0])
	data = append(data, parsed[2:]...)

	numCols := 0
	for _, r := range data {
		if len(r) > numCols {
			numCols = len(r)
		}
	}
	for len(aligns) < numCols {
		aligns = append(aligns, "left")
	}

	// 预渲染所有 cell + 测宽
	cells := make([][]string, len(data))
	widths := make([][]int, len(data))
	colW := make([]int, numCols)
	for i, r := range data {
		cells[i] = make([]string, numCols)
		widths[i] = make([]int, numCols)
		for c := 0; c < numCols; c++ {
			raw := ""
			if c < len(r) {
				raw = strings.TrimSpace(r[c])
			}
			cells[i][c] = renderInline(raw, bold, italic, code)
			w := lineDisplayWidth(cells[i][c])
			widths[i][c] = w
			if w > colW[c] {
				colW[c] = w
			}
		}
	}

	var sb strings.Builder
	drawSep := func(left, mid, right string) {
		sb.WriteString(dim(left))
		for c := 0; c < numCols; c++ {
			sb.WriteString(dim(strings.Repeat("─", colW[c]+2)))
			if c < numCols-1 {
				sb.WriteString(dim(mid))
			}
		}
		sb.WriteString(dim(right))
	}
	drawRow := func(rowCells []string, rowWidths []int) {
		sb.WriteString(dim("│"))
		for c := 0; c < numCols; c++ {
			pad := colW[c] - rowWidths[c]
			if pad < 0 {
				pad = 0
			}
			switch aligns[c] {
			case "right":
				sb.WriteString(" " + strings.Repeat(" ", pad) + rowCells[c] + " ")
			case "center":
				lp := pad / 2
				rp := pad - lp
				sb.WriteString(" " + strings.Repeat(" ", lp) + rowCells[c] + strings.Repeat(" ", rp) + " ")
			default:
				sb.WriteString(" " + rowCells[c] + strings.Repeat(" ", pad) + " ")
			}
			sb.WriteString(dim("│"))
		}
	}

	drawSep("┌", "┬", "┐")
	sb.WriteString("\n")
	drawRow(cells[0], widths[0])
	sb.WriteString("\n")
	drawSep("├", "┼", "┤")
	for i := 1; i < len(cells); i++ {
		sb.WriteString("\n")
		drawRow(cells[i], widths[i])
	}
	sb.WriteString("\n")
	drawSep("└", "┴", "┘")
	return sb.String()
}

// renderInline 处理行内 markdown 标记: **bold** / *italic* / `code`
func renderInline(s string, bold, italic, code func(...string) string) string {
	var buf strings.Builder
	runes := []rune(s)
	i := 0
	for i < len(runes) {
		// ```inline code``` — 优先匹配较长 fence
		if i+2 < len(runes) && runes[i] == '`' && runes[i+1] == '`' && runes[i+2] == '`' {
			end := findClosing(runes, i+3, '`', '`', '`')
			if end >= 0 {
				buf.WriteString(code(string(runes[i+3 : end])))
				i = end + 3
				continue
			}
		}
		// `inline code`
		if runes[i] == '`' {
			end := findClosing(runes, i+1, '`')
			if end >= 0 {
				buf.WriteString(code(string(runes[i+1 : end])))
				i = end + 1
				continue
			}
		}
		// **bold**
		if i+1 < len(runes) && runes[i] == '*' && runes[i+1] == '*' {
			end := findClosing(runes, i+2, '*', '*')
			if end >= 0 {
				buf.WriteString(bold(string(runes[i+2 : end])))
				i = end + 2
				continue
			}
		}
		// *italic*
		if runes[i] == '*' {
			end := findClosing(runes, i+1, '*')
			if end >= 0 {
				buf.WriteString(italic(string(runes[i+1 : end])))
				i = end + 1
				continue
			}
		}
		buf.WriteRune(runes[i])
		i++
	}
	return buf.String()
}

// findClosing 在切片 runes[start:] 找连续 N 个 target rune,返回结束索引(不含)。
// 找不到返回 -1。
func findClosing(runes []rune, start int, targets ...rune) int {
	n := len(targets)
	if start+n > len(runes) {
		return -1
	}
	for i := start; i <= len(runes)-n; i++ {
		match := true
		for j := 0; j < n; j++ {
			if runes[i+j] != targets[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

func (m *model) refreshViewport() {
	atBottom := m.chatViewport.AtBottom()
	w := m.chatViewport.Width()

	// 按 segment 渲染:每段独立缓存 ANSI,流式期间只有最后一段(被 Append 清过 ansi)
	// 真正走重渲;前面段直接复用。resize 时所有段的 ansiWidth 不匹配,会整体重渲。
	content := m.chatContent.Render(w, func(raw string, width int) string {
		return ensureEmojiSpacingANSI(m.renderMarkdown(ensureEmojiSpacing(raw), width))
	})

	// plan / spinner 是 ANSI widget,叠加在 markdown 渲染之后。
	if m.plan != nil && m.streaming {
		content += "\n" + renderPlanForChat(m.plan)
	}
	if m.thinking {
		content += m.spinner.View() + " thinking..."
	}
	if m.selecting && w > 0 {
		content = applySelectionHighlight(content, m.selAnchor, m.selEnd, w)
	}
	m.chatViewport.SetContent(content)
	if atBottom {
		m.chatViewport.GotoBottom()
	}
}
