package source

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"fnos-store/internal/platform"
)

// FnDepot 外部应用源协议（V1/V2）：
//   - V2: 根节点 { schema_version:"2", source_info, apps{<appname>: {...}} }
//   - V1: 根节点直接是 { <appname>: {...} }（无 schema_version）
//   - 源地址可以是 JSON 直链（任意文件名）或 GitHub 仓库根地址
//     （固定读取根目录 fnpack.json，默认分支，回退 main/master）。
// 参考: https://github.com/EWEDLCM/FnDepot README「FnDepot 外部应用源 V2 编写说明」
const fndepotFetchTimeout = 25 * time.Second

// FNDepotSource 实现 Source 接口，把 FnDepot 外部源翻译成 RemoteApp 列表。
type FNDepotSource struct {
	httpClient *http.Client
	sourceURL  string     // 用户填写的原始地址
	jsonURL    string     // 解析后的 JSON 地址
	fallbacks  []string   // GitHub 仓库模式的分支回退地址
	id         string     // 稳定 ID（sha1(原始地址)）
	name       string     // 源显示名（source_info.name 优先，否则仓库名/主机名）
	author     string
	homepage   string
}

// FNDepotSourceMeta 是源的管理元信息（列表展示用）。
type FNDepotSourceMeta struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Author   string `json:"author,omitempty"`
	Homepage string `json:"homepage,omitempty"`
	URL      string `json:"url"`
}

type fndepotV2 struct {
	SchemaVersion string                    `json:"schema_version"`
	SourceInfo    fndepotSourceInfo         `json:"source_info"`
	Apps          map[string]fndepotAppEntry `json:"apps"`
}

type fndepotSourceInfo struct {
	Name        string `json:"name"`
	Author      string `json:"author"`
	Homepage    string `json:"homepage"`
	Description string `json:"description"`
}

type fndepotRelease struct {
	UpdatedAt string                `json:"updated_at"`
	Packages  map[string]fndepotPkg `json:"packages"`
}

type fndepotPkg struct {
	DownloadURL string `json:"download_url"`
}

type fndepotAppEntry struct {
	DisplayName   string          `json:"display_name"`
	Desc          string          `json:"desc"`
	Platform      json.RawMessage `json:"platform"`
	Categories    []string        `json:"categories"`
	IconURL       string          `json:"icon_url"`
	MaintainerURL string          `json:"maintainer_url"`
	BugReportURL  string          `json:"bug_report_url"`
	Homepage      string          `json:"homepage"`
	IsDocker      bool            `json:"is_docker"`
	ServicePort   string          `json:"service_port"`
	Releases      map[string]fndepotRelease `json:"releases"`
}

var (
	githubRepoRE = regexp.MustCompile(`^https?://(?:www\.)?github\.com/([^/]+)/([^/?#]+)/?$`)
	htmlTagRE    = regexp.MustCompile(`<[^>]+>`)
	appnameRE    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
)

func fndepotCurrentArch() string {
	p := platform.DetectPlatform()
	if strings.Contains(p, "arm") {
		return "arm"
	}
	return "x86"
}

func platformMatches(p, current string) bool {
	p = strings.ToLower(strings.TrimSpace(p))
	return p == "all" || p == current
}

// fndepotPlatformOK 按源声明的 platform（字符串或数组）判断是否适配当前设备。
func fndepotPlatformOK(declared json.RawMessage, current string) bool {
	switch {
	case len(declared) == 0:
		return true // 历史规则：无 platform 默认 x86；保守放行，交给包选择兜底
	default:
		var one string
		if err := json.Unmarshal(declared, &one); err == nil {
			return platformMatches(one, current)
		}
		var many []string
		if err := json.Unmarshal(declared, &many); err == nil {
			for _, p := range many {
				if platformMatches(p, current) {
					return true
				}
			}
			return false
		}
		return true
	}
}

// NewFNDepotSource 解析并验证用户填写的源地址（立即 fetch+parse）。
func NewFNDepotSource(rawURL string) (*FNDepotSource, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return nil, fmt.Errorf("源地址为空")
	}
	if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		rawURL = "https://" + rawURL
	}
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("无效的源地址: %s", rawURL)
	}

	client := &http.Client{Timeout: fndepotFetchTimeout}
	s := &FNDepotSource{
		httpClient: client,
		sourceURL:  rawURL,
		id:         fndepotSourceID(rawURL),
	}

	jsonURL, fallbacks := resolveJSONURL(u)
	var lastErr error
	for _, candidate := range append([]string{jsonURL}, fallbacks...) {
		body, fetchErr := httpGetJSON(candidate, client)
		if fetchErr != nil {
			lastErr = fetchErr
			continue
		}
		if err := s.parse(body, candidate); err != nil {
			lastErr = err
			continue
		}
		s.jsonURL = candidate
		s.fallbacks = fallbacks
		return s, nil
	}
	return nil, fmt.Errorf("源加载失败: %w", lastErr)
}

// resolveJSONURL 区分「JSON 直链」与「GitHub 仓库根地址」。
func resolveJSONURL(u *url.URL) (string, []string) {
	if m := githubRepoRE.FindStringSubmatch(u.String()); m != nil {
		owner, repo := m[1], m[2]
		return fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/HEAD/fnpack.json", owner, repo),
			[]string{
				fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/main/fnpack.json", owner, repo),
				fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/master/fnpack.json", owner, repo),
			}
	}
	return u.String(), nil
}

// NewFNDepotSourceLazy 只做地址解析（离线），不立即抓取 JSON。
// 用于启动/配置变更时重建源列表；可达性在 FetchApps 时验证。
func NewFNDepotSourceLazy(rawURL string) (*FNDepotSource, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return nil, fmt.Errorf("源地址为空")
	}
	if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		rawURL = "https://" + rawURL
	}
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("无效的源地址: %s", rawURL)
	}
	jsonURL, fallbacks := resolveJSONURL(u)
	return &FNDepotSource{
		httpClient: &http.Client{Timeout: fndepotFetchTimeout},
		sourceURL:  rawURL,
		jsonURL:    jsonURL,
		fallbacks:  fallbacks,
		id:         fndepotSourceID(rawURL),
	}, nil
}

// ID 返回源稳定标识（sha1 前 12 位）。
func (s *FNDepotSource) ID() string { return s.id }

// OverrideName 用添加时记录的显示名覆盖（惰性构造拿不到 source_info）。
func (s *FNDepotSource) OverrideName(name string) {
	if strings.TrimSpace(name) != "" {
		s.name = strings.TrimSpace(name)
	}
}

func fndepotSourceID(rawURL string) string {
	h := sha1.Sum([]byte(rawURL))
	return hex.EncodeToString(h[:])[:12]
}

func httpGetJSON(rawURL string, client *http.Client) ([]byte, error) {
	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "fnos-store/1.x")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %s", resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}

// parse 校验并提取源元信息（V1/V2）。
func (s *FNDepotSource) parse(body []byte, jsonURL string) error {
	appsMap, _ := decodeFndepotApps(body)
	if len(appsMap) == 0 {
		return fmt.Errorf("无法识别的源格式（需 FnDepot V1/V2 结构）")
	}
	_ = jsonURL
	// V2 元信息
	var v2 fndepotV2
	if err := json.Unmarshal(body, &v2); err == nil && v2.SchemaVersion == "2" {
		s.name = strings.TrimSpace(v2.SourceInfo.Name)
		s.author = v2.SourceInfo.Author
		s.homepage = v2.SourceInfo.Homepage
	}
	if s.name == "" {
		s.name = sourceNameFromURL(jsonURL)
	}
	return nil
}

// Name 实现 Source。
func (s *FNDepotSource) Name() string {
	if s.name == "" {
		return sourceNameFromURL(s.jsonURL)
	}
	return s.name
}

// Meta 返回管理元信息。
func (s *FNDepotSource) Meta() FNDepotSourceMeta {
	return FNDepotSourceMeta{
		ID:       s.id,
		Name:     s.Name(),
		Author:   s.author,
		Homepage: s.homepage,
		URL:      s.sourceURL,
	}
}

// JSONURL 返回解析后的 JSON 地址（调试/展示用）。
func (s *FNDepotSource) JSONURL() string { return s.jsonURL }

// FetchApps 实现 Source：抓取源 JSON 并翻译为 RemoteApp（按当前架构过滤）。
func (s *FNDepotSource) FetchApps(ctx context.Context) ([]RemoteApp, error) {
	candidates := append([]string{s.jsonURL}, s.fallbacks...)
	var lastErr error
	var body []byte
	fetchedFrom := s.jsonURL
	for _, candidate := range candidates {
		req, err := http.NewRequestWithContext(ctx, "GET", candidate, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "fnos-store/1.x")
		resp, err := s.httpClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		rb, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("源 HTTP %s", resp.Status)
			continue
		}
		body = rb
		fetchedFrom = candidate
		break
	}
	if body == nil {
		return nil, fmt.Errorf("源不可达: %w", lastErr)
	}

	appsMap, _ := decodeFndepotApps(body)
	apps := make([]RemoteApp, 0, len(appsMap))
	for appName, entry := range appsMap {
		if ra, ok := translateFndepotApp(appName, entry, fetchedFrom, s.Name()); ok {
			apps = append(apps, ra)
		}
	}
	sort.Slice(apps, func(i, j int) bool { return apps[i].AppName < apps[j].AppName })
	return apps, nil
}

// decodeFndepotApps 解析 V1/V2 两种结构。
func decodeFndepotApps(body []byte) (map[string]fndepotAppEntry, bool) {
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return map[string]fndepotAppEntry{}, false
	}
	var v2 fndepotV2
	if err := json.Unmarshal(body, &v2); err == nil && v2.SchemaVersion == "2" && len(v2.Apps) > 0 {
		return v2.Apps, true
	}
	var v1 map[string]fndepotAppEntry
	if err := json.Unmarshal(body, &v1); err == nil {
		for k := range v1 {
			if !appnameRE.MatchString(k) {
				return map[string]fndepotAppEntry{}, false
			}
		}
		return v1, false
	}
	return map[string]fndepotAppEntry{}, false
}

// resolveAgainst 把源 JSON 内的相对 URL 解析为绝对 URL。
func resolveAgainst(base, ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
		return ref
	}
	baseU, err := url.Parse(base)
	if err != nil {
		return ref
	}
	refU, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	return baseU.ResolveReference(refU).String()
}

// translateFndepotApp 把单个 FnDepot 应用翻译成 RemoteApp。
// 选择规则：当前架构包优先 → all 包；版本号取最高。
func translateFndepotApp(appName string, entry fndepotAppEntry, baseURL, sourceName string) (RemoteApp, bool) {
	if !appnameRE.MatchString(appName) {
		return RemoteApp{}, false
	}
	if len(entry.Releases) == 0 {
		return RemoteApp{}, false // 拆分模式（details_url）暂不支持
	}
	if !fndepotPlatformOK(entry.Platform, fndepotCurrentArch()) {
		return RemoteApp{}, false
	}
	displayName := strings.TrimSpace(entry.DisplayName)
	if displayName == "" {
		displayName = appName
	}

	versions := make([]string, 0, len(entry.Releases))
	for v := range entry.Releases {
		versions = append(versions, v)
	}
	sortByVersionDesc(versions)

	current := fndepotCurrentArch()
	for _, v := range versions {
		release := entry.Releases[v]
		pkg, ok := release.Packages[current]
		if !ok {
			pkg, ok = release.Packages["all"]
		}
		if !ok || strings.TrimSpace(pkg.DownloadURL) == "" {
			continue
		}
		port := 0
		if ps := strings.TrimSpace(entry.ServicePort); ps != "" {
			if n, err := strconv.Atoi(ps); err == nil {
				port = n
			}
		}
		appType := ""
		if entry.IsDocker {
			appType = "docker"
		}
		return RemoteApp{
			AppName:     appName,
			DisplayName: displayName,
			Version:     v,
			FpkVersion:  v,
			Description: stripHTML(entry.Desc),
			HomepageURL: firstNonEmpty(entry.Homepage, entry.MaintainerURL, entry.BugReportURL),
			UpdatedAt:   release.UpdatedAt,
			ServicePort: port,
			Platforms:   []string{current},
			FpkURL:      resolveAgainst(baseURL, pkg.DownloadURL),
			IconURL:     resolveAgainst(baseURL, entry.IconURL),
			AppType:     appType,
			Category:    mapFndepotCategory(entry.Categories),
			Source:      sourceName,
		}, true
	}
	return RemoteApp{}, false
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func stripHTML(s string) string {
	s = htmlTagRE.ReplaceAllString(s, " ")
	return strings.Join(strings.Fields(s), " ")
}

// mapFndepotCategory 把 FnDepot 固定分类映射到本店分类键（未匹配返回空）。
func mapFndepotCategory(cats []string) string {
	if len(cats) == 0 {
		return ""
	}
	switch cats[0] {
	case "影音娱乐":
		return "media"
	case "AI赋能":
		return "ai"
	case "系统工具", "编程开发", "硬件驱动", "智能智控":
		return "system"
	case "生活服务", "教育学习":
		return "content"
	case "游戏地带":
		return "media"
	default:
		return ""
	}
}

func sourceNameFromURL(u string) string {
	p := strings.TrimSuffix(u, "/")
	p = strings.TrimSuffix(p, "/fnpack.json")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		p = p[i+1:]
	}
	if p == "" {
		p = u
	}
	return p
}

// sortByVersionDesc 按版本号降序（SemVer 风格，非数字段回退 0）。
func sortByVersionDesc(versions []string) {
	sort.Slice(versions, func(i, j int) bool {
		return compareFndepotVersions(versions[i], versions[j]) > 0
	})
}

func compareFndepotVersions(a, b string) int {
	pa, pb := versionParts(a), versionParts(b)
	for i := 0; i < len(pa) && i < len(pb); i++ {
		if pa[i] != pb[i] {
			if pa[i] < pb[i] {
				return -1
			}
			return 1
		}
	}
	if len(pa) != len(pb) {
		if len(pa) < len(pb) {
			return -1
		}
		return 1
	}
	return 0
}

func versionParts(v string) []int {
	v = strings.TrimPrefix(v, "v")
	v = strings.SplitN(v, "-", 2)[0]
	v = strings.SplitN(v, "+", 2)[0]
	parts := strings.Split(v, ".")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			n = 0
		}
		out = append(out, n)
	}
	return out
}
