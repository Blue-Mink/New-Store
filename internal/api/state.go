package api

import (
	"context"
	"errors"
	"log"
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

	// 内置源列表自动同步：每次目录检查时，把源列表里未添加过的 FnDepot 源
	// 自动加入（设置 source_list_disabled 可整体关闭）。失败只记日志，
	// 不阻断本次目录刷新。refreshOnAdd=false —— 外层紧接着抓取全部源。
	if s.configMgr != nil {
		if cfg := s.configMgr.Get(); !cfg.SourceListDisabled {
			if res := s.syncSourceList(ctx, false); res.Added > 0 || len(res.Errors) > 0 {
				for _, e := range res.Errors {
					log.Printf("source list: %s", e)
				}
			}
		}
	}

	remoteApps, fetchErr := s.source.FetchApps(ctx)

	// FnDepot 外部源：并发抓取，与内置目录合并。
	// 同名应用（appname 相同）全部保留：注册表用 appname@源名 区分，
	// 内置目录与外部源的同名应用会同时展示。
	customApps, customStatus := s.fetchCustomSources(ctx)
	if customApps != nil {
		remoteApps = append(remoteApps, customApps...)
	}

	var installedTags map[string]string
	if s.cacheStore != nil {
		installedTags = s.cacheStore.InstalledTags()
	}

	// 全部外部源抓取失败 + 本次没有外部应用 + 现有注册表已有外部应用时，
	// 不重建注册表（否则整个外部目录被清空，要等下次定时刷新才恢复）；
	// 保留旧数据，失败原因由源状态在 UI 展示。冷启动（注册表为空）照常合并。
	allCustomFailed := false
	if len(customStatus) > 0 {
		allCustomFailed = true
		for _, st := range customStatus {
			if st.Error == "" {
				allCustomFailed = false
				break
			}
		}
	}
	newExternal := 0
	if remoteApps != nil {
		for _, a := range remoteApps {
			if a.Source != "" && a.Source != "fnos-apps" {
				newExternal++
			}
		}
	}

	now := time.Now()
	s.mu.Lock()
	// Preserve existing registry when all remote/cache/local fallbacks fail.
	if remoteApps != nil || fetchErr == nil {
		if allCustomFailed && newExternal == 0 && s.registry.HasExternalApps() {
			log.Printf("refresh: all %d custom sources failed; keeping previous registry to avoid losing external catalog", len(customStatus))
		} else {
			s.registry.Merge(localApps, remoteApps, installedTags)
		}
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
	// 应用中心已安装但目录未收录的应用（如官方应用中心安装的 app）并入列表
	localApps := make([]core.LocalApp, 0, len(apps))
	for _, a := range apps {
		localApps = append(localApps, core.LocalApp{
			AppName:     a.AppName,
			DisplayName: a.DisplayName,
			Version:     a.Version,
			Status:      a.Status,
		})
	}
	s.registry.AddLocalApps(localApps)
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
		cs, err := source.NewFNDepotSourceLazy(entry.URL, s.configMgr)
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
			// 父预算要覆盖整条候选链（直连重试+镜像+分支回退，单候选 15s），
			// 否则前面候选耗光预算后，后面的直接 context deadline exceeded。
			cctx, cancel := context.WithTimeout(ctx, 120*time.Second)
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
