package duckgo

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

// testUA 只给「需要某个具体 UA」的测试用（挑战解算、重试路径），与生成逻辑无关。
const testUA = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/145.0.0.0 Safari/537.36"

// 生成出来的每个 UA 都必须过得了 chromeHints，且三样东西自洽：
// user-agent 里的版本、sec-ch-ua 的版本、sec-ch-ua-platform 的平台。
// 不自洽正是 418 ERR_CHALLENGE 的成因（旧代码写死 "Linux" + Windows UA 就是栽在这）。
func TestRandomUAIsSelfConsistent(t *testing.T) {
	shape := regexp.MustCompile(`^Mozilla/5\.0 \(.+\) AppleWebKit/537\.36 \(KHTML, like Gecko\) Chrome/(\d+)\.0\.0\.0 Safari/537\.36$`)

	for i := 0; i < 500; i++ {
		ua := RandomUA()

		m := shape.FindStringSubmatch(ua)
		if m == nil {
			t.Fatalf("生成的 UA 形状不对: %s", ua)
		}

		secChUA, platform := chromeHints(ua)
		if secChUA == "" || platform == "" {
			t.Fatalf("chromeHints 认不出自己生成的 UA: %s", ua)
		}
		if want := `"Google Chrome";v="` + m[1] + `"`; !strings.Contains(secChUA, want) {
			t.Fatalf("sec-ch-ua 与 UA 版本不一致: ua=%s hints=%s", ua, secChUA)
		}

		wantPlatform, wantNavPlatform := "Linux", "Linux x86_64"
		switch {
		case strings.Contains(ua, "Macintosh"):
			wantPlatform, wantNavPlatform = "macOS", "MacIntel"
		case strings.Contains(ua, "Windows"):
			wantPlatform, wantNavPlatform = "Windows", "Win32"
		}
		if platform != wantPlatform {
			t.Fatalf("sec-ch-ua-platform=%q, UA 却是 %s", platform, wantPlatform)
		}
		// 挑战 mock 的 navigator.platform 用的是另一张词表，但必须同一个来源 ——
		// 它以前写死 "Win32"，Linux/macOS UA 全都在三方打架。
		if _, navPlatform := platformFor(ua); navPlatform != wantNavPlatform {
			t.Fatalf("navigator.platform=%q, UA 却是 %s", navPlatform, wantNavPlatform)
		}
	}
}

// 每次生成都不一样 —— 这是「指纹用不完」的全部依据。
func TestRandomUAIsDistinct(t *testing.T) {
	const draws = 500
	seen := map[string]bool{}
	families := map[string]bool{}
	for i := 0; i < draws; i++ {
		ua := RandomUA()
		seen[ua] = true
		switch {
		case strings.Contains(ua, "Macintosh"):
			families["macOS"] = true
		case strings.Contains(ua, "Windows"):
			families["Windows"] = true
		default:
			families["Linux"] = true
		}
	}
	// 平台 7 个 × 版本 32 个 = 224 种；500 次抽样期望约 195 个不同值。
	if len(seen) < 150 {
		t.Fatalf("%d 次只生成 %d 个不同 UA，随机没生效", draws, len(seen))
	}
	if len(families) < 3 {
		t.Fatalf("500 次只覆盖 %d 个平台族，平台分布有问题: %v", len(families), families)
	}
}

// grease 品牌与版本照抄 Chromium 的算法，用三处 ground truth 钉住 ——
// 前两处是 components/embedder_support/user_agent_utils.cc 里官方单测的向量，
// 第三处是本机真 Chrome 145 的 navigator.userAgentData.brands 实测值。
// 写死成 "Not:A;Brand";v="24" 的那版在 145 上就是错的，这个测试就是拦它。
func TestGreaseBrandVersionMatchesChromium(t *testing.T) {
	for _, c := range []struct {
		seed    int
		brand   string
		version string
	}{
		{84, "Not;A=Brand", "8"},   // 官方单测
		{86, "Not?A_Brand", "24"},  // 官方单测
		{145, "Not:A-Brand", "99"}, // 本机真 Chrome 145 实测
	} {
		brand, version := greaseBrandVersion(c.seed)
		if brand != c.brand || version != c.version {
			t.Errorf("seed=%d: got %q v=%q, want %q v=%q",
				c.seed, brand, version, c.brand, c.version)
		}
	}
}

// 品牌顺序 = brandOrders[seed%6]（shuffled[order[i]] = list[i]）。
// 期望值是按算法推出的**3 品牌**形式：我们的 UA 自称 Chrome，所以带 "Google Chrome" 项。
// 本机那个 Chrome for Testing 是无品牌构建（只有 Chromium + grease 两项），
// 只能验证 grease 串本身与洗牌方向，验证不了 "Google Chrome" 项 —— 这点别误读成实测。
func TestChromeHintsBrandOrder(t *testing.T) {
	for _, c := range []struct {
		version int
		want    string
	}{
		{145, `"Not:A-Brand";v="99", "Google Chrome";v="145", "Chromium";v="145"`},
		{151, `"Not=A?Brand";v="99", "Google Chrome";v="151", "Chromium";v="151"`},
		{120, `"Not_A Brand";v="8", "Chromium";v="120", "Google Chrome";v="120"`},
	} {
		ua := fmt.Sprintf("Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/%d.0.0.0 Safari/537.36", c.version)
		got, platform := chromeHints(ua)
		if got != c.want {
			t.Errorf("Chrome/%d:\n got %s\nwant %s", c.version, got, c.want)
		}
		if platform != "Linux" {
			t.Errorf("Chrome/%d: platform=%q, want Linux", c.version, platform)
		}
	}
}

// X_USER_AGENT 覆盖一切：要复现某个指纹时钉住它。
func TestRandomUAEnvOverride(t *testing.T) {
	const pinned = "Mozilla/5.0 (X11; Linux x86_64) pinned-by-env"
	t.Setenv("X_USER_AGENT", pinned)
	for i := 0; i < 50; i++ {
		if got := RandomUA(); got != pinned {
			t.Fatalf("X_USER_AGENT 没生效: %q", got)
		}
	}
}

// 认不出的 UA 不硬套 Linux/Chrome，而是让调用方不发这两个头 —— 宁可不发，不发错的。
func TestChromeHintsUnknownUA(t *testing.T) {
	if secChUA, platform := chromeHints("curl/8.5.0"); secChUA != "" || platform != "" {
		t.Fatalf("未知 UA 应返回空: got %q %q", secChUA, platform)
	}
}
