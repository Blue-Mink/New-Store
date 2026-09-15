package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"fnos-store/internal/config"
	"fnos-store/internal/source"
)

// SourceEntry 是外部源管理视图（列表 API 返回项）。
type SourceEntry struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	URL         string    `json:"url"`
	Author      string    `json:"author,omitempty"`
	Homepage    string    `json:"homepage,omitempty"`
	AppCount    int       `json:"app_count"`
	Error       string    `json:"error,omitempty"`
	LastFetched time.Time `json:"last_fetched,omitempty"`
}

func (s *Server) handleListSources(w http.ResponseWriter, r *http.Request) {
	entries := s.ListSources()
	writeJSON(w, http.StatusOK, map[string]any{"sources": entries})
}

type addSourceRequest struct {
	URL  string `json:"url"`
	Name string `json:"name"`
}

// addOneSource 添加单个外部源的核心逻辑：验证 + 去重 + 持久化。
func (s *Server) addOneSource(rawURL, name string) (config.CustomSource, bool, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return config.CustomSource{}, false, fmt.Errorf("源地址不能为空")
	}

	// 立即验证：可抓取且是 FnDepot V1/V2 结构（可能需数秒，含 GitHub 仓库解析与镜像回退）
	cs, err := source.NewFNDepotSource(rawURL, s.configMgr)
	if err != nil {
		return config.CustomSource{}, false, err
	}

	name = strings.TrimSpace(name)
	if name == "" {
		name = cs.Meta().Name // source_info.name → 仓库 owner → 仓库名
	}

	cfg := s.configMgr.Get()
	for _, existing := range cfg.Sources {
		if strings.EqualFold(strings.TrimSpace(existing.URL), cs.Meta().URL) || existing.ID == cs.ID() {
			return config.CustomSource{}, false, fmt.Errorf("该应用源已添加")
		}
	}

	entry := config.CustomSource{ID: cs.ID(), Name: name, URL: cs.Meta().URL}
	cfg.Sources = append(cfg.Sources, entry)
	if err := s.configMgr.SaveConfig(cfg); err != nil {
		return config.CustomSource{}, false, err
	}
	s.rebuildCustomSources()
	return entry, true, nil
}

// handleAddSource 添加外部应用源：立即抓取+解析验证，成功后持久化并刷新注册表。
func (s *Server) handleAddSource(w http.ResponseWriter, r *http.Request) {
	if s.configMgr == nil {
		writeAPIError(w, http.StatusInternalServerError, "config not available")
		return
	}
	var req addSourceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid json")
		return
	}

	entry, ok, err := s.addOneSource(req.URL, req.Name)
	if !ok {
		if strings.Contains(err.Error(), "已添加") {
			writeAPIError(w, http.StatusConflict, err.Error())
		} else {
			writeAPIError(w, http.StatusUnprocessableEntity, err.Error())
		}
		return
	}
	// 后台刷新注册表（新源的应用并入目录），不阻塞响应。
	go s.refreshRegistryDebounced(context.Background())

	writeJSON(w, http.StatusOK, map[string]any{"source": SourceEntry{
		ID:   entry.ID,
		Name: entry.Name,
		URL:  entry.URL,
	}})
}

type addSourceBatchRequest struct {
	Items []addSourceRequest `json:"items"`
}

type batchSourceResult struct {
	URL   string `json:"url"`
	OK    bool   `json:"ok"`
	Name  string `json:"name,omitempty"`
	Error string `json:"error,omitempty"`
}

// handleBatchAddSources 批量添加外部源（前端多行输入框一次提交）。
// 逐条验证与去重；单条失败不影响其他条。
func (s *Server) handleBatchAddSources(w http.ResponseWriter, r *http.Request) {
	if s.configMgr == nil {
		writeAPIError(w, http.StatusInternalServerError, "config not available")
		return
	}
	var req addSourceBatchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if len(req.Items) == 0 {
		writeAPIError(w, http.StatusBadRequest, "items 不能为空")
		return
	}
	if len(req.Items) > 20 {
		writeAPIError(w, http.StatusRequestEntityTooLarge, "单次最多添加 20 个源")
		return
	}

	results := make([]batchSourceResult, 0, len(req.Items))
	added := 0
	for _, item := range req.Items {
		if strings.TrimSpace(item.URL) == "" {
			continue // 跳过空行
		}
		entry, ok, err := s.addOneSource(item.URL, item.Name)
		if ok {
			added++
			results = append(results, batchSourceResult{URL: strings.TrimSpace(item.URL), OK: true, Name: entry.Name})
		} else {
			results = append(results, batchSourceResult{URL: strings.TrimSpace(item.URL), Error: err.Error()})
		}
	}

	if added > 0 {
		go s.refreshRegistryDebounced(context.Background())
	}
	writeJSON(w, http.StatusOK, map[string]any{"added": added, "results": results})
}

// handleRemoveSource 删除外部应用源并刷新注册表。
func (s *Server) handleRemoveSource(w http.ResponseWriter, r *http.Request) {
	if s.configMgr == nil {
		writeAPIError(w, http.StatusInternalServerError, "config not available")
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeAPIError(w, http.StatusBadRequest, "缺少源 ID")
		return
	}

	cfg := s.configMgr.Get()
	kept := make([]config.CustomSource, 0, len(cfg.Sources))
	removed := false
	for _, entry := range cfg.Sources {
		if entry.ID == id {
			removed = true
			continue
		}
		kept = append(kept, entry)
	}
	cfg.Sources = kept
	if !removed {
		writeAPIError(w, http.StatusNotFound, "应用源不存在")
		return
	}
	if err := s.configMgr.SaveConfig(cfg); err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.rebuildCustomSources()
	go s.refreshRegistryDebounced(context.Background())
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
