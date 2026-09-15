package api

import (
	"context"
	"errors"
	"sync"
	"time"

	"fnos-store/internal/core"
	"fnos-store/internal/platform"
	"fnos-store/internal/source"
)

type refreshDebouncer struct {
	mu      sync.Mutex
	timer   *time.Timer
	pending bool
}

func (s *Server) refreshRegistry(ctx context.Context) error {
	if s.source == nil || s.registry == nil {
		return errors.New("source/registry not configured")
	}

	localApps, err := core.ScanInstalled(s.appsDir)
	if err != nil {
		return err
	}

	remoteApps, fetchErr := s.source.FetchApps(ctx)

	// FnDepot 外部源：并发抓取，与内置目录合并（内置 appname 优先）。
	customApps, customStatus := s.fetchCustomSources(ctx)
	if customApps != nil {
		seen := make(map[string]bool, len(remoteApps))
		combined := append([]source.RemoteApp{}, remoteApps...)
		for _, a := range combined {
			seen[a.AppName] = true
		}
		for _, a := range customApps {
			if !seen[a.AppName] {
				combined = append(combined, a)
			}
		}
		remoteApps = combined
	}

	var installedTags map[string]string
	if s.cacheStore != nil {
		installedTags = s.cacheStore.InstalledTags()
	}

	now := time.Now()
	s.mu.Lock()
	// Preserve existing registry when all remote/cache/local fallbacks fail.
	if remoteApps != nil || fetchErr == nil {
		s.registry.Merge(localApps, remoteApps, installedTags)
	}
	s.lastCheck = now
	if len(customStatus) > 0 {
		s.sourceStatus = customStatus
	}
	s.mu.Unlock()

	if s.cacheStore != nil {
		s.cacheStore.SetLastCheckAt(now)
	}

	_ = s.refreshRecommended(ctx)

	s.refreshRuntimeStatus()
	return fetchErr
}

func (s *Server) RefreshRegistry(ctx context.Context) error {
	return s.refreshRegistry(ctx)
}

func (s *Server) refreshRuntimeStatus() {
	if s.ac == nil || s.queue == nil || s.registry == nil {
		return
	}

	var apps []platform.InstalledApp
	err := s.queue.WithCLI(func() error {
		var listErr error
		apps, listErr = s.ac.List()
		return listErr
	})
	if err != nil {
		return
	}

	status := make(map[string]string, len(apps))
	versions := make(map[string]string, len(apps))
	for _, app := range apps {
		status[app.AppName] = app.Status
		versions[app.AppName] = app.Version
	}

	s.mu.Lock()
	s.statusByApp = status
	// The daemon list is the authority on whether an app exists; fold it in
	// so apps whose /var/apps manifest the scan missed still show as
	// installed instead of dead-ending on install (#280/#281).
	s.registry.ReconcileInstalled(versions)
	s.mu.Unlock()
}

func (s *Server) refreshRecommended(ctx context.Context) error {
	if s.recommendedSource == nil {
		return nil
	}

	apps, err := s.recommendedSource.FetchRecommendedApps(ctx)
	if err != nil {
		return err
	}

	s.mu.Lock()
	s.recommendedApps = append([]source.RecommendedApp(nil), apps...)
	s.mu.Unlock()

	return nil
}

func (s *Server) listRegistryApps() []core.AppInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.registry == nil {
		return nil
	}
	return s.registry.List()
}

func (s *Server) listRecommendedApps() []source.RecommendedApp {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]source.RecommendedApp(nil), s.recommendedApps...)
}

func (s *Server) getRegistryApp(name string) (core.AppInfo, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.registry == nil {
		return core.AppInfo{}, false
	}
	return s.registry.Get(name)
}

func (s *Server) getRuntimeStatus(name string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.statusByApp[name]
}

func (s *Server) getLastCheck() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastCheck
}

func (s *Server) refreshRegistryDebounced(ctx context.Context) {
	s.refreshDebouncer.mu.Lock()
	defer s.refreshDebouncer.mu.Unlock()

	if s.refreshDebouncer.pending {
		if s.refreshDebouncer.timer != nil {
			s.refreshDebouncer.timer.Stop()
		}
	}

	s.refreshDebouncer.pending = true
	s.refreshDebouncer.timer = time.AfterFunc(2*time.Second, func() {
		s.refreshDebouncer.mu.Lock()
		s.refreshDebouncer.pending = false
		s.refreshDebouncer.mu.Unlock()

		_ = s.refreshRegistry(context.Background())
	})
}

// rebuildCustomSources 从配置重建外部源列表（惰性构造，不立即抓取）。
// 启动与每次源配置变更后调用。
func (s *Server) rebuildCustomSources() {
	if s.configMgr == nil {
		return
	}
	cfg := s.configMgr.Get()
	srcs := make([]*source.FNDepotSource, 0, len(cfg.Sources))
	for _, entry := range cfg.Sources {
		cs, err := source.NewFNDepotSourceLazy(entry.URL)
		if err != nil {
			continue // 无效地址在源列表 API 中按错误展示
		}
		cs.OverrideName(entry.Name)
		srcs = append(srcs, cs)
	}
	s.mu.Lock()
	s.customSources = srcs
	s.mu.Unlock()
}

// fetchCustomSources 并发抓取所有外部源，返回合并后的 RemoteApp 列表
// （仅当前架构可用的应用）与每源状态。单源失败不影响其他源与内置目录。
func (s *Server) fetchCustomSources(ctx context.Context) ([]source.RemoteApp, map[string]sourceStatusInfo) {
	s.mu.RLock()
	srcs := s.customSources
	s.mu.RUnlock()
	if len(srcs) == 0 {
		return nil, nil
	}

	type customResult struct {
		id   string
		apps []source.RemoteApp
		err  error
	}
	ch := make(chan customResult, len(srcs))
	for _, cs := range srcs {
		go func(cs *source.FNDepotSource) {
			cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
			defer cancel()
			apps, err := cs.FetchApps(cctx)
			ch <- customResult{cs.ID(), apps, err}
		}(cs)
	}

	merged := []source.RemoteApp{}
	status := make(map[string]sourceStatusInfo, len(srcs))
	now := time.Now()
	for range srcs {
		r := <-ch
		if r.err != nil {
			status[r.id] = sourceStatusInfo{Error: r.err.Error(), LastFetched: now}
			continue
		}
		status[r.id] = sourceStatusInfo{AppCount: len(r.apps), LastFetched: now}
		merged = append(merged, r.apps...)
	}
	return merged, status
}

// ListSources 返回外部源管理视图（配置 + 最近抓取状态）。
func (s *Server) ListSources() []SourceEntry {
	cfg := s.configMgr.Get()
	s.mu.RLock()
	srcs := s.customSources
	status := s.sourceStatus
	s.mu.RUnlock()

	byID := make(map[string]*source.FNDepotSource, len(srcs))
	for _, cs := range srcs {
		byID[cs.ID()] = cs
	}

	entries := make([]SourceEntry, 0, len(cfg.Sources))
	for _, entry := range cfg.Sources {
		e := SourceEntry{
			ID:   entry.ID,
			Name: entry.Name,
			URL:  entry.URL,
		}
		if cs, ok := byID[entry.ID]; ok {
			meta := cs.Meta()
			e.Name = meta.Name
			e.Author = meta.Author
			e.Homepage = meta.Homepage
		}
		if st, ok := status[entry.ID]; ok {
			e.AppCount = st.AppCount
			e.Error = st.Error
			e.LastFetched = st.LastFetched
		}
		entries = append(entries, e)
	}
	return entries
}
