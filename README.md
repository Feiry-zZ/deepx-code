# deepx

> 本地优先的 AI 编码 agent —— 完整上下文恢复、零延迟路由、代码图谱导航、DeepSeek 缓存原生友好。

![deepx screenshot](assets/screenshot.jpg)

## 为什么选 deepx

- 🚀 golang开发，小巧快速，全平台覆盖。
- 🚀 gob二进制持久化。tool_calls、tool results、reasoning_content 全部保留，LLM 无缝续接
- 🚀 分层压缩 + 旧摘要合并
- 🚀 支持skill，MCP
- 🚀 本地关键词路由。零延迟、零 token 消耗，命中直接升 pro
- 🚀 自动模型切换。根据问题复杂度，自动升级为pro模型
- 🚀 Plan DAG 并发调度。按依赖关系并行跑子 agent，每个节点独立选模型
- 🚀 本地OCR(PaddleOCR)。离线识别，不依赖多模态 API
- 🚀 代码图谱(codeGraph)。极大减少read,glob,grep token浪费

## 快速开始

### 安装

- macOS / Linux

```bash
curl -fsSL https://raw.githubusercontent.com/itmisx/deepx-code/main/scripts/install.sh | bash
```

- Windows (PowerShell)

```bash
irm https://raw.githubusercontent.com/itmisx/deepx-code/main/scripts/install.ps1 | iex
```

### 使用

```bash
cd <你的项目目录>
deepx
```

首次启动弹出配置弹窗，填入 API key 后自动写入 `~/.deepx/model.yaml`。

## 配置

```yaml
# ~/.deepx/model.yaml
flash:
  base_url: https://api.deepseek.com
  model: deepseek-v4-flash
  api_key: sk-xxx
  context_window: 1048576

pro:
  base_url: https://api.deepseek.com
  model: deepseek-v4-pro
  api_key: sk-xxx
  context_window: 1048576
```

flash / pro 独立配置 endpoint 和模型 —— flash 用便宜小模型、pro 用 Claude / GPT 都行，只要兼容 OpenAI Chat Completions 协议。

`context_window` 控制会话压缩触发阈值（`窗口 × 70%`）。

## 核心机制

### 模型路由（本地，零延迟）

用户消息发来时，deepx 在本地做关键词匹配 + 长度判定：

```
消息含 "重构/refactor/architecture/调试…" → 直接升 pro
消息长度 < 100 字符                       → flash
消息长度 > 500 字符                       → pro
```

覆盖中（简/繁）/ 英 / 日 / 韩五种语言。**路由发生在一瞬间，不额外消耗任何 LLM token。**

### 会话持久化（gob 二进制）

```
~/.deepx/sessions/<sha1(workspace)[:16]>/
├── meta.json          # 工作区元信息
├── state.json         # 压缩状态 (summary + total_turns)
├── YYYY-MM-DD.jsonl   # 文本日志（Memory 搜索用）
└── history.gob        # 二进制完整历史
```

| 格式               | 存储内容                                                                          | 用途                       |
| :----------------- | :-------------------------------------------------------------------------------- | :------------------------- |
| `history.gob`      | system + user + assistant（含 `tool_calls`、`tool results`、`reasoning_content`） | **重启恢复，LLM 无缝续接** |
| `YYYY-MM-DD.jsonl` | user / assistant 纯文本（含模式通知）                                             | Memory 工具搜索            |

重启时优先加载 gob；失败则回退 JSONL。system prompt 如因 build 升级 / skill 变化而变动，gob 恢复时自动原地替换为当前版本。

### 会话压缩

长对话超出上下文窗口 70% 时自动触发。尾部分层保留约 20K token，旧内容压缩为 LLM 生成的连贯摘要并合并已有摘要。**压缩后也同步更新 gob**，重启一致。

### Plan DAG 并发调度

模型通过 `CreatePlan` 工具将复杂任务拆为 DAG 节点，deepx 按依赖关系启动并发子 agent：

```
PlanCreated
  ├─ plan-1: Read (flash) ─────┐
  ├─ plan-2: Read (flash) ─────┤
  ├─ plan-3: Grep (flash) ─────┤
  └─ plan-4: Write (pro) ──────┘ depends_on: [1,2,3]
```

### 审核模式（默认）

| 模式             | Write / Update / Bash | 其余工具 | 切换命令  |
| :--------------- | :-------------------- | :------- | :-------- |
| `review`（默认） | 人工 YES/NO 确认      | 自动执行 | `/review` |
| `auto`           | 自动执行              | 自动执行 | `/auto`   |
| `plan`           | 禁用                  | 自动执行 | `/plan`   |

### 本地 OCR

Ctrl+V 粘贴图片 → deepx 自动落盘 → LLM 通过 `OCR` 工具（PaddleOCR PP-OCRv5）识别图片中的文字。首次自动下载 ~37MB 模型，后续秒级响应。**DeepSeek 不支持多模态，本地 OCR 补齐最大短板。**

### 代码图谱

deepx 内置代码图谱，模型可以直接「跳到定义、找谁调用了某函数、查某接口被哪些类型实现、估算改动一个符号会牵连到哪些下游」，不用满仓库 grep + 一个个翻文件 —— 更准、更省 token。

- 覆盖 **Go 及 20+ 主流语言**（TypeScript / JavaScript / Python / Java / Rust / C / C++ / C# / Ruby / PHP / Kotlin / Swift / Scala / Dart 等）。
- Go 还能**精确到隐式接口实现**（"谁实现了这个接口"——这是文本搜索查不出来的）。
- 开机后台预建索引、改文件自动失效重建；无结果时会如实说明覆盖边界，不会误导。

启用后右栏状态区会显示「代码图谱」的就绪状态与调用次数。

## 工具集

| 类型     | 工具                               |         plan | auto | review |
| :------- | :--------------------------------- | -----------: | :--: | :----: |
| 文件只读 | `Read` `List` `Tree` `Glob` `Grep` |            ✓ |  ✓   |   ✓    |
| 代码图谱 | `CodeGraph`                        |            ✓ |  ✓   |   ✓    |
| 文件写入 | `Write` `Update`                   |            ✗ |  ✓   |   ⏳   |
| Shell    | `Bash`                             |            ✗ |  ✓   |   ⏳   |
| 联网     | `Search` `Fetch`                   |            ✓ |  ✓   |   ✓    |
| 记忆     | `Memory`                           |            ✓ |  ✓   |   ✓    |
| 技能     | `LoadSkill`                        |            ✓ |  ✓   |   ✓    |
| 图片     | `OCR`                              |            ✓ |  ✓   |   ✓    |
| 规划     | `CreatePlan` `UpdatePlanStatus`    | LLM 自主调用 |      |        |
| 升级     | `SwitchModel`                      | LLM 自主调用 |      |        |

> ⏳ = 自动执行，但需人工确认。

## Slash 命令

| 命令      | 作用             |
| :-------- | :--------------- |
| `/plan`   | 切只读模式       |
| `/auto`   | 切全自动模式     |
| `/review` | 切审核模式       |
| `/mode`   | 显示当前模式     |
| `/config` | 重新配置 API key |
| `/skills` | 列出可用 skill   |
| `/help`   | 帮助             |

## Skills 生态

```
workspace 级  <wd>/.deepx/skills/
global 级     ~/.agents/skills/ → ~/.claude/skills/ → ~/.deepx/skills/
```

- workspace 级可 `git add` 共享给团队
- global 兼容 Claude Code 生态，已有 skill 直接复用

## 架构

```
单轮对话:
  用户输入
    ↓
  RouteByKeyword (本地) ─► flash 或 pro
    ↓
  StartStream (主循环)
    ├─ 直接答
    ├─ 调工具 → review 拦截写/Shell → 执行 → 结果回灌 → 继续
    ├─ SwitchModel → 升 pro
    └─ CreatePlan → DAG scheduler → 子 agent 并发 → 汇总

会话持久化:
  HistoryUpdateMsg → SaveGob (history.gob, 完整 fidelity)
  StreamDoneMsg  → Append JSONL (纯文本, Memory 搜索)
  重启           → LoadGob (优先) / JSONL (回退)

会话压缩:
  tokens ≥ ctxWindow × 70% → runCompression (异步)
    → 尾部分层保留 ~20K token
    → LLM 合并新旧摘要
    → 更新 gob + state.json
```

## Token 经济

- **路由零 token**：纯本地关键词，不发 LLM 调用
- **工具不预注入**：`Memory` / `LoadSkill` 只在调用时才进 context
- **system prompt 极简**：仅跨工具规约 + workspace，工具触发条件在各自 description 里
- **DeepSeek KV cache 友好**：tools 数组不随模式变化；system prompt gob 恢复时版本感知

## 项目结构

```
deepx/
├── main.go
├── agent/          StartStream 工具循环 + 路由 + DAG 调度 + 子 agent
├── config/         ~/.deepx/model.yaml 读写
├── session/        gob 持久化 + JSONL 日志 + 会话压缩状态
├── tools/          全部工具实现（读写/搜索/OCR/Memory/Skill/Plan/CodeGraph）
├── codegraph/      代码图谱：跳定义 / 找调用 / 继承实现 / 影响面
├── skill/          多路径 skill 发现与加载
├── ocr/            PaddleOCR 包装（ONNX Runtime）
├── tui/            bubbletea TUI（输入/渲染/剪贴板/选中/仪表盘）
└── scripts/        安装脚本
```

## 卸载

```bash
# macOS / Linux
rm -f ~/.local/bin/deepx && rm -rf ~/.deepx

# Windows
# 删除 %LOCALAPPDATA%\Programs\deepx 和 %USERPROFILE%\.deepx
```

## License

[MIT](LICENSE) © 2026 itmisx
