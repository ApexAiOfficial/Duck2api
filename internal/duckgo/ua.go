package duckgo

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// 指纹 UA 是**生成**的，不是从固定池子里挑的。
//
// 实测（2026-10，12/12 全过）：Windows / Edge / Android / Safari（连 Chrome 版本都没有）
// / Chrome 100 与 160 都能拿到挑战 —— UA 基本是自由字段。真正被拒的只有「客户端提示与 UA
// 自相矛盾」那一类：旧代码把 sec-ch-ua-platform 写死 "Linux"，于是 Windows UA 一律 418，
// 当时被误读成「Windows UA 不被接受」；现在由 chromeHints 从 UA 反推，Windows 直接过。
//
// 所以枚举成池子是把上限白白钉死在池子大小上（5 个），生成则是用不完的。
// ponytail: 只组合 OS + Chrome 版本；实测够用。要更"真"再加 WebKit 版本/语言/screen
// （挑战 mock 里那些值仍是常量，且实测不参与校验）。
var uaPlatforms = []string{
	"X11; Linux x86_64",
	"X11; Ubuntu; Linux x86_64",
	"X11; Fedora; Linux x86_64",
	"Macintosh; Intel Mac OS X 10_15_7",
	"Macintosh; Intel Mac OS X 13_6_0",
	"Macintosh; Intel Mac OS X 14_5_0",
	"Windows NT 10.0; Win64; x64",
}

// 下界取实测通过带（100~160）的内侧。上界按「真机存在的版本」收：2026-10 本机 Chromium
// 是 151、Chrome for Testing 145，而原来的 158 是个**尚未发布**的版本 —— 报未来版本是最
// 容易看出来的假 UA。上游并不校验版本（实测 160 也过），所以这是保真度而非可用性。
// Chrome 发新版后把这里抬上去。
const (
	uaVersionMin = 120
	uaVersionMax = 151
)

// RandomUA 生成一个指纹 UA。
//
// X_USER_AGENT 覆盖一切：要复现某个具体指纹，或排查「到底是不是 UA 的问题」，钉住它。
func RandomUA() string {
	if v := strings.TrimSpace(os.Getenv("X_USER_AGENT")); v != "" {
		return v
	}
	plat := uaPlatforms[int(randInt63n(int64(len(uaPlatforms))))]
	ver := uaVersionMin + int(randInt63n(int64(uaVersionMax-uaVersionMin+1)))
	return fmt.Sprintf(
		"Mozilla/5.0 (%s) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/%d.0.0.0 Safari/537.36",
		plat, ver)
}

var chromeVersionRE = regexp.MustCompile(`Chrome/(\d+)\.\d+\.\d+\.\d+`)

// platformFor 从 UA 认操作系统，返回两张词表里的平台名。
//
// sec-ch-ua-platform 用 "Linux"/"macOS"/"Windows"，navigator.platform 用
// "Linux x86_64"/"MacIntel"/"Win32" —— 词表本来就不同，但「这是哪个系统」只能有一个来源，
// 所以认一次、各取所需。macOS 一律 MacIntel（Chrome 在 Apple Silicon 上也这么报）。
// 认不出来按历史行为落到 Linux；本包的 RandomUA 只生成上面那 7 个平台，走不到默认分支。
func platformFor(ua string) (chromePlatform, navPlatform string) {
	switch {
	case strings.Contains(ua, "Macintosh"):
		return "macOS", "MacIntel"
	case strings.Contains(ua, "Windows"):
		return "Windows", "Win32"
	default:
		return "Linux", "Linux x86_64"
	}
}

// 品牌顺序与 grease 品牌/版本都由 Chrome 大版本号算出，照抄 Chromium
// components/embedder_support/user_agent_utils.cc 的
// GenerateBrandVersionList / GetGreasedUserAgentBrandVersion / GetRandomOrder：
//
//	greasey_brand   = "Not" + chars[seed%11] + "A" + chars[(seed+1)%11] + "Brand"
//	greasey_version = {"8","99","24"}[seed%3]
//	order           = brandOrders[seed%6]        // 3 个品牌时
//	shuffled[order[i]] = list[i]                 // list = [grease, Chromium, Google Chrome]
//
// 三处 ground truth 都对上（见 ua_test.go）：Chromium 官方单测 seed=84 →
// "Not;A=Brand";v="8"、seed=86 → "Not?A_Brand";v="24"；本机真 Chrome 145 的
// navigator.userAgentData.brands → ["Chromium";v="145", "Not:A-Brand";v="99"]
// （那个是无品牌构建，只有 2 项，校验的是 grease 串与洗牌算法，不含 "Google Chrome" 本身）。
//
// 别退回写死的串：写死那版把 grease 钉成 "Not:A;Brand";v="24"，对 145 这类版本就是错的
// （真值 "Not:A-Brand";v="99"）—— 等于所有出口共用一个编造的常量，而版本号我们自己就在随机。
var (
	greaseChars    = []string{" ", "(", ":", "-", ".", "/", ")", ";", "=", "?", "_"}
	greaseVersions = []string{"8", "99", "24"}
	brandOrders    = [][3]int{{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0}}
)

func greaseBrandVersion(seed int) (brand, version string) {
	return "Not" + greaseChars[seed%len(greaseChars)] +
			"A" + greaseChars[(seed+1)%len(greaseChars)] + "Brand",
		greaseVersions[seed%len(greaseVersions)]
}

// chromeHints 从 UA 派生 sec-ch-ua / sec-ch-ua-platform 两个客户端提示。
// 由 UA 反推而不是再维护一张常量表: 两份来源迟早不一致, 而客户端提示与 UA 打架
// 正是 418 ERR_CHALLENGE 的成因之一。认不出来就返回空串, 调用方不发这两个头。
func chromeHints(ua string) (secChUA, platform string) {
	m := chromeVersionRE.FindStringSubmatch(ua)
	if m == nil {
		return "", ""
	}
	seed, err := strconv.Atoi(m[1])
	if err != nil {
		return "", ""
	}

	brand, version := greaseBrandVersion(seed)
	list := [3]string{
		fmt.Sprintf(`"%s";v="%s"`, brand, version),
		fmt.Sprintf(`"Chromium";v="%d"`, seed),
		fmt.Sprintf(`"Google Chrome";v="%d"`, seed),
	}
	shuffled := [3]string{}
	for i, at := range brandOrders[seed%len(brandOrders)] {
		shuffled[at] = list[i]
	}

	platform, _ = platformFor(ua)
	return strings.Join(shuffled[:], ", "), platform
}
