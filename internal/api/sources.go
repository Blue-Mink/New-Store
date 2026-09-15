package api

import (
	"context"
	"encoding/json"
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
	req.URL = strings.TrimSpace(req.URL)
	if req.URL == "" {
		writeAPIError(w, http.StatusBadRequest, "源地址不能为空")
	}

	// 立即验证：可抓取且是 FnDepot V1/V2 结构（可能需数秒，含 GitHub 仓库解析与镜像回退）
	cs, err := source.NewFNDepotSource(req.URL, s.configMgr)
	if err != nil {
		writeAPIError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = cs.Meta().Name
	}

	cfg := s.configMgr.Get()
	for _, existing := range cfg.Sources {
		if strings.EqualFold(strings.TrimSpace(existing.URL), cs.Meta().URL) || existing.ID == cs.ID() {
			writeAPIError(w, http.StatusConflict, "该应用源已添加")
			return
		}
	}

	entry := config.CustomSource{ID: cs.ID(), Name: name, URL: cs.Meta().URL}
	cfg.Sources = append(cfg.Sources, entry)
	if err := s.configMgr.SaveConfig(cfg); err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.rebuildCustomSources()
	// 后台刷新注册表（新源的应用并入目录），不阻塞响应。
	go s.refreshRegistryDebounced(context.Background())

	writeJSON(w, http.StatusOK, map[string]any{"source": SourceEntry{
		ID:   entry.ID,
		Name: entry.Name,
		URL:  entry.URL,
	}})
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
