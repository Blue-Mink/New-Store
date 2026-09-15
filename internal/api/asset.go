package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"fnos-store/internal/config"
)

// 应用详情页资源代理：README（Markdown）与预览图。
// 浏览器端直连 raw.githubusercontent.com 在多数网络下不可靠，这里由后端
// 按配置的 GitHub 镜像链抓取后回传。只代理应用自身声明的 URL，不接受
// 任意地址（避免 SSRF）。

const assetTTL = 10 * time.Minute

type assetCacheEntry struct {
	body        []byte
	contentType string
	expiresAt   time.Time
}

type assetCache struct {
	mu      sync.Mutex
	entries map[string]assetCacheEntry
	max     int
}

var appAssetCache = newAssetCache(128)

func newAssetCache(max int) *assetCache {
	return &assetCache{entries: make(map[string]assetCacheEntry), max: max}
}

func (c *assetCache) get(key string) (assetCacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || time.Now().After(e.expiresAt) {
		delete(c.entries, key)
		return assetCacheEntry{}, false
	}
	return e, true
}

func (c *assetCache) put(key string, e assetCacheEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= c.max {
		c.entries = make(map[string]assetCacheEntry) // 简单全量淘汰
	}
	c.entries[key] = e
}

func assetContentTypeFor(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	case ".gif":
		return "image/gif"
	case ".md", ".markdown":
		return "text/markdown; charset=utf-8"
	default:
		return "application/octet-stream"
	}
}

// handleAppAsset 代理应用自身声明的 readme / preview 资源。
// GET /api/apps/{appname}/asset?type=readme
// GET /api/apps/{appname}/asset?type=preview&index=0
func (s *Server) handleAppAsset(w http.ResponseWriter, r *http.Request) {
	appName := r.PathValue("appname")
	if appName == "" {
		writeAPIError(w, http.StatusBadRequest, "appname is required")
		return
	}
	app, ok := s.getRegistryApp(appName)
	if !ok {
		writeAPIError(w, http.StatusNotFound, "app not found")
		return
	}

	var target, kind string
	switch t := r.URL.Query().Get("type"); t {
	case "readme":
		target, kind = app.ReadmeURL, "readme"
	case "preview":
		idx, _ := strconv.Atoi(r.URL.Query().Get("index"))
		if idx < 0 || idx >= len(app.PreviewURLs) {
			writeAPIError(w, http.StatusBadRequest, "preview index out of range")
			return
		}
		target, kind = app.PreviewURLs[idx], "preview"
	default:
		writeAPIError(w, http.StatusBadRequest, "type must be readme or preview")
		return
	}
	target = strings.TrimSpace(target)
	if target == "" {
		writeAPIError(w, http.StatusNotFound, "该应用没有此资源")
		return
	}

	cacheKey := appName + "/" + kind + "/" + target
	if e, ok := appAssetCache.get(cacheKey); ok {
		w.Header().Set("Content-Type", e.contentType)
		w.Header().Set("Cache-Control", "private, max-age=600")
		_, _ = w.Write(e.body)
		return
	}

	body, contentType, err := s.fetchAsset(r.Context(), target)
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, "资源获取失败: "+err.Error())
		return
	}
	appAssetCache.put(cacheKey, assetCacheEntry{
		body: body, contentType: contentType, expiresAt: time.Now().Add(assetTTL),
	})
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "private, max-age=600")
	_, _ = w.Write(body)
}

// fetchAsset 抓取目标资源：GitHub raw 地址按镜像链回退，其余直连。
func (s *Server) fetchAsset(ctx context.Context, target string) ([]byte, string, error) {
	u, err := url.Parse(target)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, "", fmt.Errorf("无效的资源地址")
	}

	var candidates []string
	if strings.Contains(u.Host, "githubusercontent.com") {
		cfg := config.Config{}
		if s.configMgr != nil {
			cfg = s.configMgr.Get()
		}
		for _, prefix := range config.GitHubFallbackPrefixes(cfg.Mirror, cfg) {
			candidates = append(candidates, prefix+target)
		}
	}
	if len(candidates) == 0 {
		candidates = []string{target}
	}

	client := &http.Client{Timeout: 30 * time.Second}
	var lastErr error
	for _, cand := range candidates {
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		req, err := http.NewRequestWithContext(cctx, "GET", cand, nil)
		if err != nil {
			cancel()
			lastErr = err
			continue
		}
		req.Header.Set("User-Agent", "fnos-store/1.x")
		resp, err := client.Do(req)
		if err != nil {
			cancel()
			lastErr = err
			continue
		}
		body, rerr := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		resp.Body.Close()
		cancel()
		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("HTTP %s", resp.Status)
			continue
		}
		if rerr != nil {
			lastErr = rerr
			continue
		}
		if len(body) == 0 {
			lastErr = fmt.Errorf("资源为空")
			continue
		}
		ct := resp.Header.Get("Content-Type")
		if ct == "" || strings.HasPrefix(ct, "text/html") {
			ct = assetContentTypeFor(u.Path)
		}
		return body, ct, nil
	}
	return nil, "", fmt.Errorf("所有候选均失败: %w", lastErr)
}
