package core

import (
	"fnos-store/internal/source"
	"sort"
	"strings"
	"time"
)

type AppStatus string

const (
	AppStatusNotInstalled      AppStatus = "not_installed"
	AppStatusInstalledUpToDate AppStatus = "installed_up_to_date"
	AppStatusUpdateAvailable   AppStatus = "update_available"
)

// AppKey 返回注册表内部键：外部源应用为 appname@源名（允许与内置目录同名共存），
// 内置目录（fnos-apps）保持裸 appname。
func (a AppInfo) AppKey() string {
	if a.Source != "" && a.Source != "fnos-apps" {
		return a.AppName + "@" + a.Source
	}
	return a.AppName
}

type AppInfo struct {
	AppName     string
	DisplayName string
	Description string
	HomepageURL string
	UpdatedAt   string
	ServicePort int
	Platform    string
	Source      string
	IconURL     string
	Installed   bool
	// InstalledVersion is the installed manifest's `version` field. It is the
	// UPSTREAM version string and is NOT comparable with LatestVersion: the two
	// come from different producers and routinely disagree (headscale ships
	// version=0.29.7 with fpk_version=0.29.3-r3 while the catalog says 0.29.3).
	// Only the FpkVersion pair below drives the update decision.
	InstalledVersion string
	LatestVersion    string
	ReleaseTag       string
	// FpkVersion is the CATALOG's package version; InstalledFpkVersion is the
	// installed package's. This pair is authoritative for both the update
	// decision and for display. InstalledFpkVersion is empty for packages built
	// before fpk_version existed, which fall back to the revision heuristic.
	FpkVersion          string
	InstalledFpkVersion string
	DownloadURL         string
	DownloadCount       int
	AppType             string
	Category            string
	Status              AppStatus
	HasRevisionUpdate   bool
	PostInstallNote     string

	// 外部源详情页扩展元数据（内置目录通常为空）。
	ReadmeURL      string
	PreviewURLs    []string
	Maintainer     string
	MaintainerURL  string
	Distributor    string
	DistributorURL string
	Changelog      string
	SizeBytes      int64
	SHA256         string
}

type Registry struct {
	apps       map[string]AppInfo
	updatedAt  time.Time
	lastResult []AppInfo
}

func NewRegistry() *Registry {
	return &Registry{
		apps: make(map[string]AppInfo),
	}
}

func (r *Registry) Merge(local []Manifest, remote []source.RemoteApp, installedTags map[string]string) []AppInfo {
	localByName := make(map[string]Manifest, len(local))
	for _, item := range local {
		localByName[item.AppName] = item
	}

	r.apps = make(map[string]AppInfo, len(remote))
	result := make([]AppInfo, 0, len(remote))
	for _, item := range remote {
		localManifest, installed := localByName[item.AppName]
		app := AppInfo{
			AppName:         item.AppName,
			DisplayName:     item.DisplayName,
			Description:     item.Description,
			HomepageURL:     item.HomepageURL,
			UpdatedAt:       item.UpdatedAt,
			ServicePort:     item.ServicePort,
			Platform:        strings.Join(item.Platforms, ","),
			Source:          item.Source,
			IconURL:         item.IconURL,
			Installed:       installed,
			LatestVersion:   item.Version,
			ReleaseTag:      item.ReleaseTag,
			FpkVersion:      item.FpkVersion,
			DownloadURL:     item.FpkURL,
			DownloadCount:   item.DownloadCount,
			AppType:         item.AppType,
			Category:        item.Category,
			Status:          AppStatusNotInstalled,
			PostInstallNote: item.PostInstallNote,
			ReadmeURL:           item.ReadmeURL,
			PreviewURLs:         item.PreviewURLs,
			Maintainer:          item.Maintainer,
			MaintainerURL:       item.MaintainerURL,
			Distributor:         item.Distributor,
			DistributorURL:      item.DistributorURL,
			Changelog:           item.Changelog,
			SizeBytes:           item.SizeBytes,
			SHA256:              item.SHA256,
		}

		if installed {
			app.InstalledVersion = localManifest.Version
			app.InstalledFpkVersion = localManifest.FpkVersion
			if app.ServicePort == 0 {
				app.ServicePort = localManifest.ServicePort
			}
			if app.Platform == "" {
				app.Platform = localManifest.Platform
			}

			// Use fpk_version comparison if both versions are available
			if localManifest.FpkVersion != "" && item.FpkVersion != "" {
				fpkCmp := CompareFpkVersions(localManifest.FpkVersion, item.FpkVersion)
				if fpkCmp < 0 {
					app.Status = AppStatusUpdateAvailable
				} else {
					app.Status = AppStatusInstalledUpToDate
				}
				app.HasRevisionUpdate = false
			} else {
				// Fallback to existing logic: version comparison + installedTags + revision check
				versionCmp := CompareVersions(localManifest.Version, item.Version)
				installedTag := installedTags[item.AppName]
				revisionUpdate := versionCmp == 0 && installedTag != item.ReleaseTag && hasRevisionUpdate(item.ReleaseTag, localManifest.Version)
				if versionCmp < 0 || revisionUpdate {
					app.Status = AppStatusUpdateAvailable
				} else {
					app.Status = AppStatusInstalledUpToDate
				}
				app.HasRevisionUpdate = revisionUpdate
			}
		}

		r.apps[app.AppKey()] = app
		result = append(result, app)
	}

	sort.Slice(result, func(i, j int) bool {
		if result[i].UpdatedAt != result[j].UpdatedAt {
			return result[i].UpdatedAt > result[j].UpdatedAt
		}
		return result[i].DisplayName < result[j].DisplayName
	})

	r.updatedAt = time.Now()
	r.lastResult = result
	return result
}

func (r *Registry) List() []AppInfo {
	out := make([]AppInfo, len(r.lastResult))
	copy(out, r.lastResult)
	return out
}

func (r *Registry) Get(appname string) (AppInfo, bool) {
	// 优先按内部键（appname 或 appname@源名）精确命中；
	// 回退按裸 appname 扫描（内置目录优先），兼容只传 appname 的旧调用。
	if app, ok := r.apps[appname]; ok {
		return app, ok
	}
	var fallback AppInfo
	hasFallback := false
	for _, app := range r.apps {
		if app.AppName != appname {
			continue
		}
		if !hasFallback || app.Source == "fnos-apps" {
			fallback, hasFallback = app, true
			if app.Source == "fnos-apps" {
				break
			}
		}
	}
	return fallback, hasFallback
}

func hasRevisionUpdate(releaseTag, installedVersion string) bool {
	prefix, ok := releaseTagPrefix(releaseTag)
	if !ok {
		return false
	}
	expectedTag := prefix + "/v" + installedVersion
	return releaseTag != expectedTag
}

func releaseTagPrefix(releaseTag string) (string, bool) {
	idx := strings.Index(releaseTag, "/v")
	if idx <= 0 {
		return "", false
	}
	return releaseTag[:idx], true
}

// ReconcileInstalled folds daemon-reported installed apps into the registry.
//
// The /var/apps manifest scan is the primary source of installed state, but
// the app-center daemon is authoritative for WHETHER an app is installed.
// When the daemon knows an app the scan missed, the store must not offer
// 安装 on it — the daemon would reject the install with
// "已安装，请使用更新功能" while the update tab shows nothing, dead-ending
// the user (conversun/fnos-apps#280 daidai-panel, #281 mihomo).
//
// Daemon-discovered apps show the daemon's version string and are treated as
// up to date: there is no local manifest to compare against, and a wrong
// "update available" badge is worse than none. Callers must hold the same
// lock they hold for Merge.
func (r *Registry) ReconcileInstalled(daemon map[string]string) {
	for i := range r.lastResult {
		if r.lastResult[i].Installed {
			continue
		}
		ver, known := daemon[r.lastResult[i].AppName]
		if !known {
			continue
		}
		r.lastResult[i].Installed = true
		r.lastResult[i].InstalledVersion = ver
		r.lastResult[i].Status = AppStatusInstalledUpToDate
		r.lastResult[i].HasRevisionUpdate = false

		if app, ok := r.apps[r.lastResult[i].AppName]; ok {
			app.Installed = true
			app.InstalledVersion = ver
			app.Status = AppStatusInstalledUpToDate
			app.HasRevisionUpdate = false
			r.apps[r.lastResult[i].AppName] = app
		}
	}
}
