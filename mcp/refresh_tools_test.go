package mcp

import (
	"deepx/tools"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// slowListServerSrc 是一个 stdio MCP server，tools/list 响应延迟 2 秒。
const slowListServerSrc = `package main
import ("bufio";"encoding/json";"fmt";"os";"time")
func main(){
	sc := bufio.NewScanner(os.Stdin); sc.Buffer(make([]byte,0,64*1024), 1<<20)
	for sc.Scan() {
		var req map[string]any
		if json.Unmarshal(sc.Bytes(), &req) != nil { continue }
		id, ok := req["id"]; if !ok { continue }
		var result any
		switch req["method"] {
		case "initialize":
			result = map[string]any{"protocolVersion":"2024-11-05","capabilities":map[string]any{}}
		case "tools/list":
			time.Sleep(2 * time.Second)
			result = map[string]any{"tools":[]any{map[string]any{
				"name":"slow","description":"慢工具",
				"inputSchema":map[string]any{"type":"object","properties":map[string]any{}},
			}}}
		case "tools/call":
			result = map[string]any{"content":[]any{map[string]any{"type":"text","text":"ok"}}}
		}
		b,_ := json.Marshal(map[string]any{"jsonrpc":"2.0","id":id,"result":result})
		fmt.Fprintln(os.Stdout, string(b))
	}
}`

func buildSlowListServer(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "main.go")
	if err := os.WriteFile(src, []byte(slowListServerSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "slowserver")
	cmd := exec.Command("go", "build", "-o", bin, src)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("编译失败: %v\n%s", err, out)
	}
	return bin
}

// refreshTools 期间 getClient 不应被阻塞。
func TestRefreshTools_DoesNotBlockGetClient(t *testing.T) {
	bin := buildSlowListServer(t)
	m := NewManager()
	if err := m.Connect(ServerConfig{Name: "slow", Command: bin}); err != nil {
		t.Fatalf("连接失败: %v", err)
	}
	t.Cleanup(func() { m.Disconnect("slow") })

	var wg sync.WaitGroup
	getClientDuration := make(chan time.Duration, 1)

	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(100 * time.Millisecond)
		start := time.Now()
		_, err := m.getClient("slow")
		elapsed := time.Since(start)
		if err != nil {
			t.Errorf("getClient 失败: %v", err)
		}
		getClientDuration <- elapsed
	}()

	m.refreshTools()
	wg.Wait()

	elapsed := <-getClientDuration
	t.Logf("getClient 耗时: %v", elapsed)

	if elapsed > 500*time.Millisecond {
		t.Errorf("getClient 耗时 %v，仍然持锁阻塞", elapsed)
	}
}

// refreshTools 后工具注入仍然正确。
func TestRefreshTools_StillInjectsToolsCorrectly(t *testing.T) {
	bin := buildFakeServer(t)
	m := NewManager()
	if err := m.Connect(ServerConfig{Name: "fake", Command: bin}); err != nil {
		t.Fatalf("连接失败: %v", err)
	}
	t.Cleanup(func() { m.Disconnect("fake") })

	m.refreshTools()

	tools := m.toolsSnapshot()
	if len(tools) == 0 {
		t.Fatal("refreshTools 后工具列表为空")
	}

	found := false
	for _, tool := range tools {
		if tool.Name == "mcp__fake__echo" {
			found = true
			break
		}
	}
	if !found {
		t.Error("未找到 mcp__fake__echo 工具")
	}
}

// 并发 refresh 中慢的那次不应覆盖快的新鲜结果。
func TestRefreshTools_StaleRefreshDoesNotOverwrite(t *testing.T) {
	fakeBin := buildFakeServer(t)
	slowBin := buildSlowListServer(t)

	m := NewManager()
	if err := m.Connect(ServerConfig{Name: "fast", Command: fakeBin}); err != nil {
		t.Fatalf("连接 fast 失败: %v", err)
	}
	if err := m.Connect(ServerConfig{Name: "slow", Command: slowBin}); err != nil {
		t.Fatalf("连接 slow 失败: %v", err)
	}
	t.Cleanup(func() {
		m.Disconnect("fast")
		m.Disconnect("slow")
	})

	// 启动第一次 refresh（会卡 2 秒在 slow 的 ListTools）
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		m.refreshTools()
	}()

	// 等第一次 refresh 进入 ListTools 阶段
	time.Sleep(200 * time.Millisecond)

	// 断开 slow，再做第二次 refresh（只有 fast，应该很快）
	m.Disconnect("slow")
	m.refreshTools()

	// 此时工具列表应只有 fast，不应被第一次 refresh 覆盖回 slow
	tools := m.toolsSnapshot()
	for _, tool := range tools {
		if tool.Name == "mcp__slow__slow" {
			t.Error("stale refresh 覆盖了新结果：slow 工具不应存在")
		}
	}

	hasFast := false
	for _, tool := range tools {
		if tool.Name == "mcp__fast__echo" {
			hasFast = true
		}
	}
	if !hasFast {
		t.Error("fast 工具丢失")
	}

	wg.Wait()
}

// 慢 server 不阻塞 manager 的其他操作。
func TestRefreshTools_SlowServerDoesNotBlockManager(t *testing.T) {
	fakeBin := buildFakeServer(t)
	slowBin := buildSlowListServer(t)

	m := NewManager()
	if err := m.Connect(ServerConfig{Name: "fast", Command: fakeBin}); err != nil {
		t.Fatalf("连接 fast 失败: %v", err)
	}
	if err := m.Connect(ServerConfig{Name: "slow", Command: slowBin}); err != nil {
		t.Fatalf("连接 slow 失败: %v", err)
	}
	t.Cleanup(func() {
		m.Disconnect("fast")
		m.Disconnect("slow")
	})

	// 后台触发 refreshTools（会卡 2 秒）
	go m.refreshTools()

	// 等 refreshTools 进入 ListTools 阶段
	time.Sleep(200 * time.Millisecond)

	// 此时 getClient 应立即返回，不被 slow 的 ListTools 阻塞
	start := time.Now()
	_, err := m.getClient("fast")
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("getClient 失败: %v", err)
	}
	if elapsed > 200*time.Millisecond {
		t.Errorf("getClient 耗时 %v，被 refreshTools 阻塞", elapsed)
	}
	t.Logf("getClient 耗时: %v", elapsed)
}

func (m *Manager) toolsSnapshot() []tools.Tool {
	return tools.MCPTools()
}
