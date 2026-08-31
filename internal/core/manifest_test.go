package core

import (
	"os"
	"path/filepath"
	"testing"
)

func writeManifest(t *testing.T, dir, appname, content string) {
	t.Helper()
	appDir := filepath.Join(dir, appname)
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appDir, "manifest"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// One foreign app with a malformed manifest must not abort the whole local
// scan: a single bad file used to fail refreshRegistry, which made EVERY app
// look not-installed — the store then offered 安装 on apps the daemon knew
// were installed and dead-ended users on "已安装，请使用更新功能"
// (conversun/fnos-apps#280, #281).
func TestScanInstalledSkipsMalformedManifest(t *testing.T) {
	dir := t.TempDir()

	writeManifest(t, dir, "good-app", "appname         = good-app\ndistributor     = conversun\n")
	writeManifest(t, dir, "bad-port", "appname         = bad\nservice_port    = not-a-number\n")
	writeManifest(t, dir, "foreign-app", "appname         = foreign\nservice_port    = ???\n")

	apps, err := ScanInstalled(dir)
	if err != nil {
		t.Fatalf("ScanInstalled failed on a malformed sibling manifest: %v", err)
	}
	if len(apps) != 1 || apps[0].AppName != "good-app" {
		t.Fatalf("ScanInstalled = %+v, want only good-app", apps)
	}
}

// Foreign apps' manifests are parsed then filtered by distributor; a foreign
// app whose manifest PARSES fine but is missing entirely (directory with no
// manifest) is skipped silently.
func TestScanInstalledSkipsMissingManifest(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, "ours", "appname         = ours\ndistributor     = conversun\n")
	if err := os.MkdirAll(filepath.Join(dir, "no-manifest-app"), 0o755); err != nil {
		t.Fatal(err)
	}

	apps, err := ScanInstalled(dir)
	if err != nil {
		t.Fatalf("ScanInstalled: %v", err)
	}
	if len(apps) != 1 || apps[0].AppName != "ours" {
		t.Fatalf("ScanInstalled = %+v, want only ours", apps)
	}
}
