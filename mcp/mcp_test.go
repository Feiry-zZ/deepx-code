package mcp

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// fakeServerSrc 是一个最小的 stdio MCP server:回应 initialize / tools/list / tools/call。
const fakeServerSrc = `package main
import ("bufio";"encoding/json";"fmt";"os")
func main(){
	sc := bufio.NewScanner(os.Stdin); sc.Buffer(make([]byte,0,64*1024), 1<<20)
	for sc.Scan() {
		var req map[string]any
		if json.Unmarshal(sc.Bytes(), &req) != nil { continue }
		id, ok := req["id"]; if !ok { continue } // 通知,不回
		var result any
		switch req["method"] {
		case "initialize":
			result = map[string]any{"protocolVersion":"2024-11-05","capabilities":map[string]any{}}
		case "tools/list":
			result = map[string]any{"tools":[]any{map[string]any{
				"name":"echo","description":"回声",
				"inputSchema":map[string]any{"type":"object","properties":map[string]any{"text":map[string]any{"type":"string"}},"required":[]any{"text"}},
			}}}
		case "tools/call":
			p,_ := req["params"].(map[string]any)
			a,_ := p["arguments"].(map[string]any)
			result = map[string]any{"content":[]any{map[string]any{"type":"text","text":fmt.Sprint(a["text"])}}}
		}
		b,_ := json.Marshal(map[string]any{"jsonrpc":"2.0","id":id,"result":result})
		fmt.Fprintln(os.Stdout, string(b))
	}
}`

// buildFakeServer 把假 server 编译成临时二进制,返回路径。
func buildFakeServer(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "main.go")
	if err := os.WriteFile(src, []byte(fakeServerSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "fakeserver")
	cmd := exec.Command("go", "build", "-o", bin, src)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("编译假 server 失败: %v\n%s", err, out)
	}
	return bin
}

func TestClientHandshakeAndCall(t *testing.T) {
	bin := buildFakeServer(t)
	c, err := Connect(ServerConfig{Name: "fake", Command: bin})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	defs, err := c.ListTools()
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) != 1 || defs[0].Name != "echo" {
		t.Fatalf("tools/list = %+v, 期望 1 个 echo", defs)
	}

	out, err := c.CallTool("echo", map[string]any{"text": "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if out != "hello" {
		t.Fatalf("CallTool = %q, 期望 hello", out)
	}
	t.Log("MCP 握手 / tools.list / tools.call 全通 ✓")
}

func TestConfigRoundTrip(t *testing.T) {
	// 用临时 HOME 隔离 ~/.deepx/mcp.json
	t.Setenv("HOME", t.TempDir())
	if got, _ := LoadConfig(); len(got) != 0 {
		t.Fatalf("初始应为空, got %+v", got)
	}
	if err := AddServer(ServerConfig{Name: "a", Command: "echo", Args: []string{"hi"}}); err != nil {
		t.Fatal(err)
	}
	if err := AddServer(ServerConfig{Name: "b", Command: "cat"}); err != nil {
		t.Fatal(err)
	}
	got, _ := LoadConfig()
	if len(got) != 2 {
		t.Fatalf("应有 2 个, got %d", len(got))
	}
	// 同名覆盖
	if err := AddServer(ServerConfig{Name: "a", Command: "ls"}); err != nil {
		t.Fatal(err)
	}
	got, _ = LoadConfig()
	if len(got) != 2 {
		t.Fatalf("同名应覆盖不新增, got %d", len(got))
	}
	// 删除
	deleted, _ := DeleteServer("a")
	if !deleted {
		t.Fatal("应删到 a")
	}
	got, _ = LoadConfig()
	if len(got) != 1 || got[0].Name != "b" {
		t.Fatalf("删后应剩 b, got %+v", got)
	}
	t.Log("配置增删改查 ✓")
}
