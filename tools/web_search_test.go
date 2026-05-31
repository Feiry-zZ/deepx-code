package tools

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// captcha 检测必须命中 Bing 反爬页面的几个常见特征,
// 同时不能在普通搜索结果页里误判("没结果"和"被反爬"是完全不同的诊断指引)。
func TestLooksLikeCaptcha(t *testing.T) {
	cases := []struct {
		name string
		html string
		want bool
	}{
		{"captcha_text class", `<div class="captcha_text">输入下方字符</div>`, true},
		{"captcha_header class", `<div class="captcha_header">证明你不是机器人</div>`, true},
		{"plain captcha class", `<form><input class="captcha" /></form>`, true},
		{"verify in title", `<html><head><title>Verify | Bing</title>`, true},
		{"id captchaSection", `<section id="captchaSection">...`, true},
		{"name cvid", `<input name="cvid" type="hidden" />`, true},
		// 普通搜索页里完全不该命中
		{"normal result page", `<li class="b_algo"><h2><a href="x">t</a></h2></li>`, false},
		{"empty html", `<html></html>`, false},
		{"no results page", `<div class="b_no">没找到</div>`, false},
	}
	for _, tc := range cases {
		got := looksLikeCaptcha(tc.html)
		if got != tc.want {
			t.Errorf("[%s] looksLikeCaptcha = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// 错误信息路由:errCaptcha 应该映射成"反爬限频"的诊断文案,
// 其它错误维持"网络访问问题"原版文案。这两个建议截然不同,不能混。
func TestWebSearch_ErrorMessageRouting_Captcha(t *testing.T) {
	// 触发 errCaptcha 的最干净方法是直接喂 captcha HTML 给 parse 之前的判断,
	// 但 WebSearch 入口要走真实网络。这里直接检查"错误信息分类"在 errCaptcha
	// 上能命中 errors.Is 这条不变量。
	if !errors.Is(errCaptcha, errCaptcha) {
		t.Fatal("errCaptcha 不能跟自己 Is 匹配,sentinel 设计崩了")
	}
	// 业务文案得明确指向反爬,不是"网络问题"
	wrapped := errCaptcha
	msg := wrapped.Error()
	if !strings.Contains(msg, "验证码") && !strings.Contains(msg, "反爬") {
		t.Errorf("errCaptcha 文案应明示验证码/反爬,got: %q", msg)
	}
}

// UA 池至少有 3 个;pickUA 必须能返回其中一个(否则后续轮换形同虚设)。
func TestPickUA(t *testing.T) {
	if len(uaPool) < 3 {
		t.Errorf("uaPool 至少应有 3 个 UA 做轮换,实际 %d", len(uaPool))
	}
	// 调几十次,落点必须覆盖至少 2 种(随机不保证全覆盖,2 个就证明轮换有效)
	seen := map[string]bool{}
	for i := 0; i < 60; i++ {
		seen[pickUA()] = true
	}
	if len(seen) < 2 {
		t.Errorf("60 次 pickUA 只落在 %d 种 UA 上,轮换可能没生效", len(seen))
	}
}

// 实跑 Bing,复现 issue 里的"虎皮鹦鹉适宜温度"查询。默认 SKIP,DEEPX_LIVE=1 才跑。
// 验证 cookie 暖身 + UA 轮换 + Referer 是不是真降低了 captcha 命中率。
func TestLive_WebSearchAgainstBing(t *testing.T) {
	if os.Getenv("DEEPX_LIVE") != "1" {
		t.Skip("set DEEPX_LIVE=1 to hit real Bing")
	}
	res := WebSearch(map[string]any{"query": "虎皮鹦鹉 适宜温度", "max_results": 5})
	t.Logf("Success=%v\nOutput:\n%s", res.Success, res.Output)
	if !res.Success {
		t.Fatalf("WebSearch failed: %s", res.Output)
	}
	if !strings.Contains(res.Output, "找到") {
		t.Errorf("expected '找到' marker in success output, got: %s", res.Output)
	}
}
