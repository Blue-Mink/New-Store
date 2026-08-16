package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type DownloadRequest struct {
	URLs     []string
	FileName string
	AppName  string
}

type Downloader struct {
	httpClient  *http.Client
	downloadDir string
	tmpDir      string
}

func NewDownloader(downloadDir string) *Downloader {
	if downloadDir == "" {
		downloadDir = os.TempDir()
	}
	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}
	return &Downloader{
		httpClient:  &http.Client{Transport: transport},
		downloadDir: downloadDir,
		tmpDir:      os.TempDir(),
	}
}

// staleTmpAge is how old a temp file must be before cleanup may reap it. A
// younger file may belong to an in-flight download — deleting it out from
// under os.Rename was conversun/fnos-apps#245.
const staleTmpAge = time.Hour

// CleanupStaleTmpFiles reaps ABANDONED download temp files (older than
// staleTmpAge). It is safe to run while a download is in flight.
func (d *Downloader) CleanupStaleTmpFiles() error {
	entries, err := os.ReadDir(d.downloadDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if !strings.HasSuffix(entry.Name(), ".fpk.tmp") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if time.Since(info.ModTime()) < staleTmpAge {
			continue
		}
		_ = os.Remove(filepath.Join(d.downloadDir, entry.Name()))
	}
	return nil
}

func (d *Downloader) Download(ctx context.Context, req DownloadRequest, progress func(downloaded, total int64)) (string, error) {
	if req.FileName == "" {
		return "", errors.New("file name is required")
	}

	if err := os.MkdirAll(d.downloadDir, 0o755); err != nil {
		return "", fmt.Errorf("create download dir: %w", err)
	}

	if err := checkTmpSpace(d.tmpDir); err != nil {
		return "", err
	}

	prefixedName := req.AppName + "-" + req.FileName
	finalPath := filepath.Join(d.downloadDir, prefixedName)

	urls := req.URLs

	if len(urls) == 0 {
		return "", errors.New("download urls are empty")
	}

	var lastErr error
	for _, url := range urls {
		// A unique temp file per attempt: the deterministic finalPath+".tmp"
		// let any second actor (OS /tmp reaper, stale cleanup, retry) delete
		// the in-flight file under os.Rename (conversun/fnos-apps#245). The
		// ".fpk.tmp" suffix is kept so CleanupStaleTmpFiles still matches.
		tmp, err := os.CreateTemp(d.downloadDir, prefixedName+".*.fpk.tmp")
		if err != nil {
			return "", fmt.Errorf("create temp file: %w", err)
		}
		tmpPath := tmp.Name()
		if err := tmp.Close(); err != nil {
			_ = os.Remove(tmpPath)
			return "", fmt.Errorf("close temp file: %w", err)
		}

		if err := d.downloadFromURL(ctx, url, tmpPath, progress); err != nil {
			lastErr = err
			_ = os.Remove(tmpPath)
			continue
		}

		if err := os.Rename(tmpPath, finalPath); err != nil {
			_ = os.Remove(tmpPath)
			return "", fmt.Errorf("rename %q to %q: %w", tmpPath, finalPath, err)
		}
		return finalPath, nil
	}

	if lastErr == nil {
		lastErr = errors.New("download failed")
	}
	return "", lastErr
}

func (d *Downloader) downloadFromURL(ctx context.Context, url, dstPath string, progress func(downloaded, total int64)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}

	resp, err := d.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %q: %s", url, resp.Status)
	}

	f, err := os.OpenFile(dstPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	total := resp.ContentLength
	buf := make([]byte, 128*1024)
	var downloaded int64
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, err := f.Write(buf[:n]); err != nil {
				return err
			}
			downloaded += int64(n)
			if progress != nil {
				progress(downloaded, total)
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return readErr
		}
	}

	if err := f.Sync(); err != nil {
		return err
	}

	const minFpkSize int64 = 10 * 1024
	if downloaded < minFpkSize {
		return fmt.Errorf("downloaded file too small (%d bytes) — likely corrupted", downloaded)
	}
	return nil
}

func checkTmpSpace(tmpDir string) error {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(tmpDir, &stat); err != nil {
		return fmt.Errorf("statfs %q: %w", tmpDir, err)
	}

	available := stat.Bavail * uint64(stat.Bsize)
	const minRequired = 64 * 1024 * 1024
	if available < minRequired {
		return fmt.Errorf("insufficient free space in %s: %d bytes available", tmpDir, available)
	}
	return nil
}
