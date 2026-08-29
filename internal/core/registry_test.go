package core

import (
	"testing"

	"fnos-store/internal/source"
)

// Merge decides the update badge for every app. The scenarios below are the
// distinct shapes observed on a live fnOS box: modern packages carry
// fpk_version and take the exact comparison, while packages built before
// fpk_version existed fall back to a heuristic.
func TestMergeUpdateStatus(t *testing.T) {
	tests := []struct {
		name             string
		local            *Manifest
		remote           source.RemoteApp
		installedTags    map[string]string
		wantStatus       AppStatus
		wantRevisionFlag bool
		wantInstalledFpk string
	}{
		{
			name:             "not installed",
			local:            nil,
			remote:           source.RemoteApp{AppName: "openlist", Version: "4.2.5", FpkVersion: "4.2.5-r3", ReleaseTag: "openlist/v4.2.5-r3"},
			wantStatus:       AppStatusNotInstalled,
			wantInstalledFpk: "",
		},
		{
			// headscale: manifest version (0.29.7) disagrees with the catalog
			// version (0.29.3), but fpk_version is authoritative and shows a
			// genuine revision bump.
			name:             "fpk revision bump wins over disagreeing plain versions",
			local:            &Manifest{AppName: "headscale", Version: "0.29.7", FpkVersion: "0.29.3-r3"},
			remote:           source.RemoteApp{AppName: "headscale", Version: "0.29.3", FpkVersion: "0.29.3-r4", ReleaseTag: "headscale/v0.29.3-r4"},
			wantStatus:       AppStatusUpdateAvailable,
			wantRevisionFlag: false,
			wantInstalledFpk: "0.29.3-r3",
		},
		{
			name:             "fpk versions identical",
			local:            &Manifest{AppName: "beszel", Version: "0.18.8", FpkVersion: "0.18.8"},
			remote:           source.RemoteApp{AppName: "beszel", Version: "0.18.8", FpkVersion: "0.18.8", ReleaseTag: "beszel/v0.18.8"},
			wantStatus:       AppStatusInstalledUpToDate,
			wantInstalledFpk: "0.18.8",
		},
		{
			name:             "installed fpk ahead of catalog is not an update",
			local:            &Manifest{AppName: "beszel", Version: "0.18.9", FpkVersion: "0.18.9"},
			remote:           source.RemoteApp{AppName: "beszel", Version: "0.18.8", FpkVersion: "0.18.8", ReleaseTag: "beszel/v0.18.8"},
			wantStatus:       AppStatusInstalledUpToDate,
			wantInstalledFpk: "0.18.9",
		},
		{
			// 1panel: legacy package with no fpk_version. Equal plain versions
			// plus a revision-suffixed remote tag means the catalog is ahead.
			name:             "legacy manifest falls back to revision heuristic",
			local:            &Manifest{AppName: "1panel", Version: "1.10.34-lts"},
			remote:           source.RemoteApp{AppName: "1panel", Version: "1.10.34-lts", FpkVersion: "1.10.34-lts-r8", ReleaseTag: "1panel/v1.10.34-lts-r8"},
			wantStatus:       AppStatusUpdateAvailable,
			wantRevisionFlag: true,
			wantInstalledFpk: "",
		},
		{
			// Same legacy shape, but the store already recorded the installed
			// tag and it matches the catalog: no badge.
			name:             "legacy manifest with matching cached tag is up to date",
			local:            &Manifest{AppName: "1panel", Version: "1.10.34-lts"},
			remote:           source.RemoteApp{AppName: "1panel", Version: "1.10.34-lts", FpkVersion: "1.10.34-lts-r8", ReleaseTag: "1panel/v1.10.34-lts-r8"},
			installedTags:    map[string]string{"1panel": "1panel/v1.10.34-lts-r8"},
			wantStatus:       AppStatusInstalledUpToDate,
			wantRevisionFlag: false,
			wantInstalledFpk: "",
		},
		{
			// alist: legacy manifest whose plain version is ahead of the
			// catalog. Must not offer a downgrade.
			name:             "legacy manifest ahead of catalog is up to date",
			local:            &Manifest{AppName: "alist", Version: "3.63.1"},
			remote:           source.RemoteApp{AppName: "alist", Version: "3.63.0", FpkVersion: "3.63.0-r3", ReleaseTag: "alist/v3.63.0-r3"},
			wantStatus:       AppStatusInstalledUpToDate,
			wantRevisionFlag: false,
			wantInstalledFpk: "",
		},
		{
			name:             "legacy manifest behind catalog upstream version",
			local:            &Manifest{AppName: "prometheus", Version: "3.13.3"},
			remote:           source.RemoteApp{AppName: "prometheus", Version: "3.14.0", FpkVersion: "3.14.0", ReleaseTag: "prometheus/v3.14.0"},
			wantStatus:       AppStatusUpdateAvailable,
			wantRevisionFlag: false,
			wantInstalledFpk: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var local []Manifest
			if tt.local != nil {
				local = []Manifest{*tt.local}
			}

			got := NewRegistry().Merge(local, []source.RemoteApp{tt.remote}, tt.installedTags)
			if len(got) != 1 {
				t.Fatalf("Merge returned %d apps, want 1", len(got))
			}
			app := got[0]

			if app.Status != tt.wantStatus {
				t.Errorf("Status = %q, want %q", app.Status, tt.wantStatus)
			}
			if app.HasRevisionUpdate != tt.wantRevisionFlag {
				t.Errorf("HasRevisionUpdate = %v, want %v", app.HasRevisionUpdate, tt.wantRevisionFlag)
			}
			if app.InstalledFpkVersion != tt.wantInstalledFpk {
				t.Errorf("InstalledFpkVersion = %q, want %q", app.InstalledFpkVersion, tt.wantInstalledFpk)
			}
			if app.Installed != (tt.local != nil) {
				t.Errorf("Installed = %v, want %v", app.Installed, tt.local != nil)
			}
		})
	}
}

// A remote app the box does not have installed must never inherit another
// app's local manifest, and apps absent from the catalog must not appear.
func TestMergeOnlyReturnsCatalogApps(t *testing.T) {
	local := []Manifest{
		{AppName: "openlist", Version: "4.2.5", FpkVersion: "4.2.5-r3"},
		{AppName: "gone-from-catalog", Version: "1.0.0", FpkVersion: "1.0.0"},
	}
	remote := []source.RemoteApp{
		{AppName: "openlist", Version: "4.2.5", FpkVersion: "4.2.5-r3", ReleaseTag: "openlist/v4.2.5-r3"},
		{AppName: "never-installed", Version: "2.0.0", FpkVersion: "2.0.0", ReleaseTag: "never-installed/v2.0.0"},
	}

	got := NewRegistry().Merge(local, remote, nil)
	if len(got) != 2 {
		t.Fatalf("Merge returned %d apps, want 2 (catalog size)", len(got))
	}

	byName := make(map[string]AppInfo, len(got))
	for _, a := range got {
		byName[a.AppName] = a
	}

	if _, ok := byName["gone-from-catalog"]; ok {
		t.Error("app missing from the catalog must not be returned")
	}
	if !byName["openlist"].Installed {
		t.Error("openlist should be marked installed")
	}
	if byName["never-installed"].Installed {
		t.Error("never-installed must not be marked installed")
	}
	if byName["never-installed"].InstalledVersion != "" {
		t.Errorf("never-installed InstalledVersion = %q, want empty", byName["never-installed"].InstalledVersion)
	}
}
