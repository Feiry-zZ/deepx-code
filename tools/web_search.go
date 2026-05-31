package tools

import (
	"errors"
	"fmt"
	stdhtml "html"
	"io"
	"math/rand"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// === 公共类型 ===

// webResult 是单条搜索结果的归一化结构。
type webResult struct {
	Title   string
	URL     string
	Snippet string
}

// errCaptcha 是 Bing 反爬挑战页 sentinel,区别于真正的"DOM 结构变了"。
// WebSearch 入口用 errors.Is 判断后给用户对应的建议(换网络 vs 反馈 BUG)。
var errCaptcha = errors.New("Bing 返回了验证码页面(反爬触发,IP 可能被限频)")

// === 工具入口 ===

// WebSearch 是 OpenAI-style 工具入口,被 tools.go 的工具表注册并由 LLM 调用。
// 后端固定走 Bing HTML 抓取(先试 cn.bing.com 再试 www.bing.com),零配置、无需任何 API key。
// 进程内复用一个带 cookie jar 的 http.Client + UA 池,降低被 Bing 当 bot 直接拦的概率。
func WebSearch(args map[string]any) ToolResult {
	query, _ := args["query"].(string)
	query = strings.TrimSpace(query)
	if query == "" {
		return ToolResult{Output: "query 不能为空", Success: false}
	}
	maxResults := toInt(args["max_results"], 5)
	if maxResults <= 0 {
		maxResults = 5
	}
	if maxResults > 15 {
		maxResults = 15
	}

	results, err := (&bingProvider{}).search(query, maxResults)
	if err != nil {
		// 区分两类错误,给用户对应的诊断指引:
		//   captcha → IP 被 Bing 反爬限频,换网络/等一会
		//   其它    → 真的连不上 Bing,或 Bing 改 DOM 解不出来
		suffix := "可能是当前网络无法访问 Bing,稍后重试或检查网络。"
		if errors.Is(err, errCaptcha) {
			suffix = "Bing 反爬把请求识别成机器人,把你的 IP 暂时限频了。\n" +
				"建议:① 换个网络(VPN / 移动热点);② 稍等几分钟让限频过期;③ 如频繁触发,后续可能要换搜索后端。"
		}
		return ToolResult{
			Output:  fmt.Sprintf("搜索失败 (Bing): %v\n\n%s", err, suffix),
			Success: false,
		}
	}
	if len(results) == 0 {
		return ToolResult{Output: fmt.Sprintf("\"%s\" 无结果", query), Success: true}
	}
	return ToolResult{Output: formatWebResults(query, results), Success: true}
}

func formatWebResults(query string, results []webResult) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "搜索 \"%s\" 找到 %d 条结果:\n\n", query, len(results))
	for i, r := range results {
		fmt.Fprintf(&sb, "%d. %s\n   %s\n", i+1, r.Title, r.URL)
		if r.Snippet != "" {
			fmt.Fprintf(&sb, "   %s\n", r.Snippet)
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// === Bing HTML 抓取(唯一后端,零配置)===

type bingProvider struct{}

func (b *bingProvider) search(query string, n int) ([]webResult, error) {
	// 先尝试国内域名 cn.bing.com,失败回退国际版 www.bing.com。
	// cn.bing.com 在国内 ISP 通常直连;海外用户走国际版。
	var lastErr error
	for _, host := range []string{"cn.bing.com", "www.bing.com"} {
		results, err := bingFetch(host, query, n)
		if err == nil && len(results) > 0 {
			return results, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errors.New("两个 Bing 域名都返回 0 条结果")
	}
	return nil, lastErr
}

// === HTTP 客户端复用 + cookie 暖身 + UA 轮换 (反爬韧性) ===

// uaPool 三个最常见的桌面浏览器 UA。同一进程内多次搜索时轮换,降低被
// Bing 当固定指纹 bot 识别的概率。不追求"扮像真人",只求别一发就被拦。
var uaPool = []string{
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36",
	"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36",
}

func pickUA() string {
	return uaPool[rand.Intn(len(uaPool))]
}

// 进程级共享:cookie jar 跨多次 search 累积 Bing 写的 cookie(SRCHHPGUSR / MUID 等)。
// 没 cookie 直接 /search 触发 captcha 概率明显高于带 cookie。
var (
	bingClientOnce sync.Once
	bingClient     *http.Client

	warmedMu sync.Mutex
	warmed   = map[string]bool{} // 每个 host 第一次 hit 时去首页暖一次,后续复用 jar 里的 cookie
)

func getBingClient() *http.Client {
	bingClientOnce.Do(func() {
		jar, _ := cookiejar.New(nil)
		bingClient = &http.Client{
			Jar:     jar,
			Timeout: 12 * time.Second,
		}
	})
	return bingClient
}

// warmCookies 先 GET host 首页,让 Bing 写一波 cookie 进 jar。
// 失败静默(让后续 search 自己决定是否能成),不阻塞主路径。
func warmCookies(host, ua string) {
	req, err := http.NewRequest("GET", "https://"+host+"/", nil)
	if err != nil {
		return
	}
	setBrowserHeaders(req, ua, "")
	resp, err := getBingClient().Do(req)
	if err != nil {
		return
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
}

func setBrowserHeaders(req *http.Request, ua, referer string) {
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Sec-Fetch-User", "?1")
	req.Header.Set("Sec-Fetch-Dest", "document")
	req.Header.Set("Upgrade-Insecure-Requests", "1")
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
}

func bingFetch(host, query string, n int) ([]webResult, error) {
	client := getBingClient()
	ua := pickUA()

	// 该 host 第一次访问时先暖 cookie(GET 首页)。后续复用 jar,直达 search。
	warmedMu.Lock()
	first := !warmed[host]
	if first {
		warmed[host] = true
	}
	warmedMu.Unlock()
	if first {
		warmCookies(host, ua)
	}

	u := fmt.Sprintf("https://%s/search?q=%s&count=%d&FORM=PERE",
		host, url.QueryEscape(query), n)
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, err
	}
	setBrowserHeaders(req, ua, "https://"+host+"/")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, host)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	bodyStr := string(body)

	// 关键判断:captcha 页面 vs 真正的 DOM 结构变化
	if looksLikeCaptcha(bodyStr) {
		return nil, errCaptcha
	}

	return parseBingHTML(bodyStr, n)
}

// captchaMarkers 是 Bing 反爬挑战页常见特征片段。命中任一 → 判为 captcha。
// 不追求 100% 精准,漏判最坏退回"HTML 结构变化"错误,误判把真结果当 captcha
// 概率极低(普通搜索页不含 captcha_text 之类的 class)。
var captchaMarkers = []string{
	`class="captcha"`,
	`class="captcha_text"`,
	`class="captcha_header"`,
	`id="captchaSection"`,
	`<title>verify`,
	`name="cvid"`, // bing 验证页常出现的字段
}

func looksLikeCaptcha(htmlBody string) bool {
	lower := strings.ToLower(htmlBody)
	for _, m := range captchaMarkers {
		if strings.Contains(lower, strings.ToLower(m)) {
			return true
		}
	}
	return false
}

// parseBingHTML 用正则从 Bing 结果页 HTML 提取搜索结果。
// Bing 的 DOM 结构经常微调,这里只锚定相对稳定的几个标签 (b_algo / h2 a / b_caption)。
// 任何一项解不到就跳过该结果,不让一个坏结构拖垮整体。
// 注意:Bing 实际输出的 <li class="b_algo" data-id ... iid=SERP.xxx> 会带额外属性,
// 任何对 class 后字符的固定假设都会失配。所有结果块的 class= 后用 [^>]* 容忍后续属性。
var (
	bingBlockRe      = regexp.MustCompile(`(?s)<li class="b_algo"[^>]*>.*?</li>`)
	bingTitleRe      = regexp.MustCompile(`(?s)<h2[^>]*>.*?<a[^>]+href="([^"]+)"[^>]*>(.*?)</a>`)
	bingSnippetRe    = regexp.MustCompile(`(?s)<p[^>]*class="[^"]*b_lineclamp[^"]*"[^>]*>(.*?)</p>`)
	bingSnippetAltRe = regexp.MustCompile(`(?s)<div[^>]*class="[^"]*b_caption[^"]*"[^>]*>.*?<p[^>]*>(.*?)</p>`)
	tagRe            = regexp.MustCompile(`<[^>]+>`)
	wsRe             = regexp.MustCompile(`\s+`)
)

func parseBingHTML(htmlBody string, n int) ([]webResult, error) {
	var results []webResult
	for _, block := range bingBlockRe.FindAllString(htmlBody, -1) {
		if len(results) >= n {
			break
		}
		var r webResult
		if m := bingTitleRe.FindStringSubmatch(block); m != nil {
			r.URL = stdhtml.UnescapeString(strings.TrimSpace(m[1]))
			r.Title = cleanHTMLText(m[2])
		}
		if m := bingSnippetRe.FindStringSubmatch(block); m != nil {
			r.Snippet = cleanHTMLText(m[1])
		} else if m := bingSnippetAltRe.FindStringSubmatch(block); m != nil {
			r.Snippet = cleanHTMLText(m[1])
		}
		if r.Title != "" && r.URL != "" {
			results = append(results, r)
		}
	}
	if len(results) == 0 {
		return nil, errors.New("HTML 结构变化导致 0 条结果可解析")
	}
	return results, nil
}

func cleanHTMLText(s string) string {
	s = tagRe.ReplaceAllString(s, "")
	s = stdhtml.UnescapeString(s)
	s = wsRe.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}
