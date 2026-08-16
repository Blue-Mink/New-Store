package api

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"fnos-store/internal/config"
)

// pullAbortNeedles marks pull failures that no mirror switch can fix: local
// conditions (cancellation, disk full, OOM kill) under which every further
// attempt fails the same way. Anything else — allowlist denials, access
// denied, missing manifests, network errors — is per-mirror, so the chain
// advances to the next candidate.
var pullAbortNeedles = []string{
	"context canceled",
	"context deadline exceeded",
	"no space left on device",
	"signal: killed",
}

// isPullAbortError reports whether a failed pull aborts the whole fallback
// chain (true) or falls through to the next mirror candidate (false).
func isPullAbortError(output string, err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	msg := output
	if err != nil {
		msg += "\n" + err.Error()
	}
	for _, needle := range pullAbortNeedles {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}

// dockerPullCandidates builds the ordered refs to try for one compose image:
// the selected mirror's shape first, every other real mirror re-prefixed by
// ITS OWN multi-registry capability, then the bare direct ref last
// (conversun/fnos-apps#267, #266, #257, #248). Duplicates collapse — e.g. a
// single-registry mirror cannot proxy a non-docker.io registry and yields
// the direct ref, which the chain already carries.
func dockerPullCandidates(composeRef string, cfg config.Config) []string {
	canonical := stripDockerMirrorPrefix(composeRef, cfg)
	prefixes := config.DockerFallbackPrefixes(cfg.DockerMirror, cfg)
	candidates := make([]string, 0, len(prefixes))
	seen := make(map[string]struct{}, len(prefixes))
	for _, prefix := range prefixes {
		ref := applyDockerMirrorPrefix(canonical, prefix)
		if _, dup := seen[ref]; dup {
			continue
		}
		seen[ref] = struct{}{}
		candidates = append(candidates, ref)
	}
	return candidates
}

// stripDockerMirrorPrefix removes whichever known mirror prefix composeRef
// carries — the configured one first, then any other — leaving the canonical
// ref each candidate re-prefixes from scratch.
func stripDockerMirrorPrefix(image string, cfg config.Config) string {
	if selected := config.DockerMirrorPrefix(cfg.DockerMirror, cfg); selected != "" && strings.HasPrefix(image, selected) {
		return image[len(selected):]
	}
	for _, m := range config.DockerMirrorOptions() {
		if m.URL != "" && strings.HasPrefix(image, m.URL) {
			return image[len(m.URL):]
		}
	}
	return image
}

// applyDockerMirrorPrefix prefixes a canonical ref for one mirror, honoring
// that mirror's multi-registry capability with the exact semantics of
// normalizeImageForPull: multi-registry proxies keep the registry in the
// path, single-registry proxies strip docker.io/ and cannot proxy other
// registries at all (those refs pass through unprefixed).
func applyDockerMirrorPrefix(image, prefix string) string {
	return normalizeImageForPull(prefix+image, prefix, dockerPrefixMultiRegistry(prefix))
}

// dockerPrefixMultiRegistry resolves a prefix URL to its mirror's capability.
// The custom prefix and "" are not in the table and read as single-registry,
// matching IsDockerMirrorMultiRegistry("custom"/"direct").
func dockerPrefixMultiRegistry(prefix string) bool {
	if prefix == "" {
		return false
	}
	for _, m := range config.DockerMirrorOptions() {
		if m.URL == prefix {
			return m.MultiRegistry
		}
	}
	return false
}

// pullImageWithFallback walks the candidate refs, one attempt each: mirror
// denials advance with an SSE notice, fatal local errors abort immediately.
// It returns the ref that pulled successfully so the caller can tag it back
// onto the compose ref, or ONE aggregated error naming every source tried.
func (p *installPipeline) pullImageWithFallback(ctx context.Context, stream *sseStream, candidates []string, message string) (string, error) {
	attempts := make([]string, 0, len(candidates))
	for i, ref := range candidates {
		if i > 0 {
			_ = stream.sendProgress(progressPayload{
				Step:    "pulling",
				Message: fmt.Sprintf("镜像源拉取失败，正在尝试备用镜像源 (%d/%d)...", i+1, len(candidates)),
			})
		}
		output, err := p.pullSingleImage(ctx, stream, ref, message)
		if err == nil {
			return ref, nil
		}
		detail := output
		if detail == "" {
			detail = err.Error()
		}
		if isPullAbortError(detail, err) {
			return "", fmt.Errorf("Docker 镜像拉取失败: %s\n请尝试在 Docker 设置中更换镜像加速源后重试", detail)
		}
		attempts = append(attempts, ref+": "+detail)
	}
	return "", fmt.Errorf("Docker 镜像拉取失败: 已尝试 %d 个镜像源均失败:\n%s\n请尝试在 Docker 设置中更换镜像加速源后重试",
		len(attempts), strings.Join(attempts, "\n"))
}
