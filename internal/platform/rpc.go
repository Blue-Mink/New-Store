//go:build linux

package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httptrace"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The fnOS app-center daemon exposes its own HTTP API over a unix socket. This
// is the channel the web UI uses, and it is the ONLY way to upgrade an app
// without destroying it.
//
// Why this exists: `appcenter-cli` offers no data-preserving upgrade. Its
// `install-local` implements an upgrade as uninstall-then-reinstall, and on
// fnOS 1.2.0203 the reinstall always fails with error 10237 — measured twice on
// a live box, taking the app AND its @appdata with it
// (conversun/fnos-apps#189). The daemon, meanwhile, has a proper upgrade
// subsystem (Operation.Upgrade with Prepare/Restore/ActivateUpgradeRollback)
// that the CLI simply never exposes.
//
// Verified end-to-end on fnOS 1.2.0203: beszel 0.18.7-r1 -> r2 with a canary
// file in @appdata surviving byte-identical, and the daemon logging
// class=upgrade rather than a uninstall/install pair.
//
// This is a var, not a const, solely so tests can point the client at a fake
// daemon on a temp socket. Production code MUST NOT reassign it.
var daemonSocket = "/var/run/com.trim.app.center.sock"

// Daemon routes, measured. nginx proxies the browser's /app-center/* here
// unchanged, but these internal /rpc/v1 routes need no session token.
const (
	routeDownloadTask   = "/rpc/v1/download/task"
	routeDownloadStatus = "/rpc/v1/download/status"
	routeUpdateInfo     = "/rpc/v1/update/info"
	routeUpdateTask     = "/rpc/v1/update/task"
	routeInstallInfo    = "/rpc/v1/install/info"
	routeInstallTask    = "/rpc/v1/install/task"
	routeUninstallTask  = "/rpc/v1/uninstall/task"
	routeCommonStatus   = "/rpc/v1/common/status"
)

// Daemon status codes observed on the staging/task polls.
const (
	daemonStatusRunning = 1
	daemonStatusSuccess = 2
	// daemonStatusUnknownTask is what /rpc/v1/common/status answers for a taskId
	// the daemon does not know: code 0 (so not an error envelope) with status 5.
	// Measured on 1.2.0505. A task the daemon has reaped and a task lost to a
	// daemon restart are indistinguishable here, so this NEVER proves failure.
	daemonStatusUnknownTask = 5
)

// rpcEnvelope is the daemon's uniform response shape.
type rpcEnvelope struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

// newDaemonClient dials the unix socket. The store runs as root on the same
// box, so no authentication is involved.
func newDaemonClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", daemonSocket)
			},
		},
	}
}

// daemonCall posts a JSON body and decodes data into out.
//
// A non-zero code is ALWAYS a failure. The daemon returns HTTP 200 with an
// error code in the body, so ignoring it would repeat the exact mistake that
// made appcenter-cli's exit status meaningless.
func daemonCall(ctx context.Context, path string, body any, out any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode %s request: %w", path, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://localhost"+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	var connected bool
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{
		GotConn: func(httptrace.GotConnInfo) { connected = true },
	}))

	resp, err := newDaemonClient(60 * time.Second).Do(req)
	if err != nil {
		// Distinguish "provably never sent" from "may already have been acted
		// on". Without a connection no byte reached the daemon, so a caller may
		// safely fall back or retry. Once connected, a timeout or reset says
		// nothing about whether the daemon received and executed the request —
		// treating that as a plain failure is how a mutation gets run twice.
		if !connected {
			return fmt.Errorf("%w (%s): %v", ErrDaemonUnreachable, path, err)
		}
		return fmt.Errorf("%w (%s): %v", ErrDaemonAmbiguous, path, err)
	}
	defer resp.Body.Close()

	var env rpcEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return fmt.Errorf("decode %s response: %w", path, err)
	}
	if env.Code != 0 {
		return &DaemonError{Path: path, Code: env.Code, Msg: env.Msg}
	}
	if out != nil && len(env.Data) > 0 {
		if err := json.Unmarshal(env.Data, out); err != nil {
			return fmt.Errorf("decode %s data: %w", path, err)
		}
	}
	return nil
}

// ErrDaemonUnreachable marks a call that provably never reached the daemon:
// the unix socket could not be dialed, so no byte was written and no state can
// have changed. This is the ONLY transport failure after which a caller may
// safely take a different mutating path.
var ErrDaemonUnreachable = errors.New("app center daemon unreachable")

// ErrDaemonAmbiguous marks a call that reached the daemon but whose outcome is
// unknown — a timeout or reset after the connection was established. The
// daemon may have received the request and acted on it, so a mutation MUST NOT
// be retried or rerouted on this error.
var ErrDaemonAmbiguous = errors.New("app center 请求结果未知")

// DaemonError carries the daemon's own error code so callers can react to
// known ones (e.g. 19000 = a required wizard field is missing).
type DaemonError struct {
	Path string
	Code int
	Msg  string
}

func (e *DaemonError) Error() string {
	if e.Msg != "" {
		return fmt.Sprintf("app center 返回错误 %d: %s", e.Code, e.Msg)
	}
	return fmt.Sprintf("app center 返回错误 %d (%s)", e.Code, e.Path)
}

// Daemon error codes worth naming.
const (
	daemonCodeValidation       = 10030 // malformed request
	daemonCodeTransientTimeout = 10050 // daemon-internal timeout, e.g. "failed to get volume info: TRPC read timeout" — the next poll typically succeeds
	daemonCodePackageMissing   = 10100 // package not staged
	daemonCodeWizardRequired   = 19000 // a required wizard field was not supplied
)

// maxConsecutiveTransientPolls bounds how many transient status-poll errors
// StageFpk rides out before giving up, so a genuinely sick daemon still fails
// fast instead of spinning until the staging deadline.
const maxConsecutiveTransientPolls = 5

// StagedPackage describes an fpk the daemon has unpacked and identified.
type StagedPackage struct {
	AppName     string `json:"appName"`
	Version     string `json:"version"`
	Name        string `json:"name"`
	PackageType string `json:"packageType"`
	Installed   bool   `json:"installed"`
	Path        string `json:"path"`
}

type stageStatus struct {
	Status      int     `json:"status"`
	Message     string  `json:"message"`
	Progress    float64 `json:"progress"`
	PackageType string  `json:"packageType"`
	Path        string  `json:"path"`
	AppName     string  `json:"appName"`
	Version     string  `json:"version"`
	Name        string  `json:"name"`
	Installed   bool    `json:"installed"`
}

// StageFpk hands a local fpk to the daemon, which unpacks and identifies it.
// Both install and upgrade require this first: without it the info calls
// answer 10100 (package not found).
func (a *LinuxAppCenter) StageFpk(ctx context.Context, fpkPath string) (*StagedPackage, error) {
	var task struct {
		DownloadTaskID string `json:"downloadTaskId"`
	}
	err := daemonCall(ctx, routeDownloadTask, map[string]any{
		"packageSourceType": "file",
		"path":              fpkPath,
	}, &task)
	if err != nil {
		return nil, fmt.Errorf("暂存安装包失败: %w", err)
	}
	if task.DownloadTaskID == "" {
		return nil, fmt.Errorf("暂存安装包失败: app center 未返回任务 ID")
	}

	// Staging is fast (sub-second on a 12 MB fpk) but poll generously; a slow
	// volume should not look like a failure.
	deadline := time.Now().Add(3 * time.Minute)
	transientFailures := 0
	for time.Now().Before(deadline) {
		var st stageStatus
		err := daemonCall(ctx, routeDownloadStatus, map[string]any{
			"downloadTaskId": task.DownloadTaskID,
		}, &st)
		if err != nil {
			// A transient blip (10050 / TRPC timeout) must not kill the
			// whole install — tolerate a bounded number in a row
			// (conversun/fnos-apps#247). Any other daemon error aborts
			// immediately, exactly as before.
			if !isTransientStagePollError(err) {
				return nil, fmt.Errorf("查询暂存状态失败: %w", err)
			}
			transientFailures++
			if transientFailures >= maxConsecutiveTransientPolls {
				return nil, fmt.Errorf("查询暂存状态失败: %w（已连续 %d 次瞬时错误，放弃）", err, transientFailures)
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Second):
			}
			continue
		}
		transientFailures = 0
		if st.Status == daemonStatusSuccess {
			if st.AppName == "" || st.Version == "" {
				return nil, fmt.Errorf("暂存完成但 app center 未能识别安装包内容")
			}
			return &StagedPackage{
				AppName: st.AppName, Version: st.Version, Name: st.Name,
				PackageType: st.PackageType, Installed: st.Installed, Path: st.Path,
			}, nil
		}
		if st.Status != daemonStatusRunning {
			return nil, fmt.Errorf("暂存安装包失败: 状态 %d %s", st.Status, st.Message)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return nil, fmt.Errorf("暂存安装包超时")
}

// isTransientStagePollError reports whether a staging-status poll failure is a
// daemon-internal blip worth retrying: code 10050 (failed to get volume info),
// or any error whose message carries a TRPC/transport timeout. Every other
// daemon error is a refusal and must abort staging immediately.
func isTransientStagePollError(err error) bool {
	var de *DaemonError
	if errors.As(err, &de) && de.Code == daemonCodeTransientTimeout {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "TRPC read timeout") || strings.Contains(msg, "timeout")
}

// wizardInfo is the subset of the info response we act on. It also carries the
// form definition the app declares, which is what the native App Center renders.
type wizardInfo struct {
	AppName           string          `json:"appName"`
	Version           string          `json:"version"`
	Name              string          `json:"name"`
	InstallType       string          `json:"installType"`
	InstalledType     string          `json:"installedType"`
	InstalledVolumeID int             `json:"installedVolumeID"`
	HasWizard         bool            `json:"hasWizard"`
	WizardContent     json.RawMessage `json:"wizardContent"`
	Docker            bool            `json:"docker"`
}

type infoResponse struct {
	WizardInfo wizardInfo `json:"wizardInfo"`
}

// UpgradeFpk upgrades an ALREADY-INSTALLED app in place, preserving its data.
//
// This is the whole point of the RPC channel: it drives the daemon's upgrade
// operation (which stops the app, swaps the payload and keeps @appdata) instead
// of install-local's uninstall-then-reinstall.
func (a *LinuxAppCenter) UpgradeFpk(ctx context.Context, fpkPath string, params []WizardParam) error {
	staged, err := a.StageFpk(ctx, fpkPath)
	if err != nil {
		return err
	}
	if !staged.Installed {
		return fmt.Errorf("%s 尚未安装，无法执行升级", staged.AppName)
	}

	var info infoResponse
	if err := daemonCall(ctx, routeUpdateInfo, map[string]any{
		"appName":       staged.AppName,
		"updateVersion": staged.Version,
		"packageType":   staged.PackageType,
		"language":      "zh-CN",
	}, &info); err != nil {
		return fmt.Errorf("升级前检查失败: %w", err)
	}

	// Pin to the volume the app already occupies. The daemon reports it, so we
	// never have to consult the broken `appcenter-cli default-volume` getter.
	volume := info.WizardInfo.InstalledVolumeID
	if volume <= 0 {
		return fmt.Errorf("无法确定 %s 当前所在的存储卷，已中止升级以保护现有数据", staged.AppName)
	}

	if params == nil {
		params = []WizardParam{}
	}
	var task struct {
		TaskID string `json:"taskId"`
	}
	if err := daemonCall(ctx, routeUpdateTask, map[string]any{
		"appName":       staged.AppName,
		"updateVersion": staged.Version,
		"packageType":   staged.PackageType,
		"systemParameters": map[string]any{
			"agreedToProtocol": true,
			"installVolumeID":  volume,
			"dataVolumeId":     volume,
			"immediateStart":   false,
		},
		"customParameters": params,
		"language":         "zh-CN",
	}, &task); err != nil {
		return fmt.Errorf("升级失败: %w", err)
	}
	if err := a.waitTask(ctx, task.TaskID, "升级"); err != nil {
		return err
	}
	// Reap the daemon's unpacked copy only once the task is DEFINITIVELY done.
	// On a failed or unknown outcome the task may still be live and reading it.
	removeStagedPackage(staged.Path)
	return nil
}

// submitUninstall asks the daemon to uninstall an app and returns the task ID.
//
// This replaces `appcenter-cli uninstall`, whose pre-flight issues
// GET /rpc/v1/uninstall/info against a daemon that only serves POST, so the
// CLI aborts 100% of the time on fnOS 1.2.0203 (conversun/fnos-apps#265).
// wizard_delete_data is pinned to "false" — what the native Web UI sends — so
// the app's @appdata always survives an in-store uninstall.
func (a *LinuxAppCenter) submitUninstall(ctx context.Context, appname string) (string, error) {
	var task struct {
		TaskID string `json:"taskId"`
	}
	err := daemonCall(ctx, routeUninstallTask, map[string]any{
		"appname":            appname,
		"wizard_delete_data": "false",
	}, &task)
	if err != nil {
		return "", err
	}
	return task.TaskID, nil
}

type taskStatus struct {
	Status     int     `json:"status"`
	Message    string  `json:"message"`
	Progress   float64 `json:"progress"`
	OutputText string  `json:"outputText"`
}

// ErrTaskOutcomeUnknown marks a daemon task whose result could not be
// observed: the daemon no longer knows the task ID, polling was cut off, or no
// terminal state was reached in time. The mutation may well have SUCCEEDED, so
// this must never be reported as a plain failure and must never trigger an
// automatic retry. Callers resolve it by checking on-disk postconditions.
var ErrTaskOutcomeUnknown = errors.New("无法确认操作结果")

// taskPollOutageBudget bounds how long waitTask tolerates being unable to
// observe a task. Status polls are read-only, so retrying them is always safe;
// the daemon emits transient failures (code 10050 / TRPC read timeout) under
// load and is restarted outright by fnOS system updates. A consecutive-failure
// count would be the wrong unit here — the poll interval, not the number of
// failures, decides how much real downtime gets covered.
const taskPollOutageBudget = 90 * time.Second

// taskPollInterval paces both the happy path and the outage retry.
const taskPollInterval = 2 * time.Second

// sleepCtx waits for d unless ctx is done first.
func sleepCtx(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// waitTask polls a daemon task to completion.
//
// It deliberately distinguishes three outcomes: success, definite failure, and
// ErrTaskOutcomeUnknown. Losing sight of a task is NOT the same as the task
// failing — the daemon keeps working after we stop watching — and conflating
// them is what turns a completed upgrade into a user-visible error plus a
// retry that mutates the same app a second time.
func (a *LinuxAppCenter) waitTask(ctx context.Context, taskID, what string) error {
	if taskID == "" {
		return fmt.Errorf("%s失败: app center 未返回任务 ID", what)
	}
	deadline := time.Now().Add(15 * time.Minute)
	var outageStart time.Time
	for time.Now().Before(deadline) {
		var st taskStatus
		err := daemonCall(ctx, routeCommonStatus, map[string]any{"taskId": taskID}, &st)
		if err != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("%w: %s过程中断开了与 app center 的连接", ErrTaskOutcomeUnknown, what)
			}
			// Failing to READ the status says nothing about the task, so ride
			// out a bounded outage rather than report a failure the daemon
			// never had. StageFpk already does this for its own poll; the task
			// poll covers far more time and needs it more.
			if outageStart.IsZero() {
				outageStart = time.Now()
			}
			if time.Since(outageStart) > taskPollOutageBudget {
				return fmt.Errorf("%w: 已连续 %s 无法查询%s状态 (%v)", ErrTaskOutcomeUnknown, taskPollOutageBudget, what, err)
			}
			if sleepErr := sleepCtx(ctx, taskPollInterval); sleepErr != nil {
				return fmt.Errorf("%w: %s过程中断开了与 app center 的连接", ErrTaskOutcomeUnknown, what)
			}
			continue
		}
		outageStart = time.Time{}

		switch st.Status {
		case daemonStatusSuccess:
			return nil
		case daemonStatusRunning:
		case daemonStatusUnknownTask:
			// The daemon has no record of this task. Reaped after completing, or
			// lost to a restart — indistinguishable from here, so neither may be
			// assumed.
			return fmt.Errorf("%w: app center 已不再持有该%s任务", ErrTaskOutcomeUnknown, what)
		default:
			detail := st.Message
			if detail == "" {
				detail = st.OutputText
			}
			return fmt.Errorf("%s失败: 状态 %d %s", what, st.Status, detail)
		}
		if err := sleepCtx(ctx, taskPollInterval); err != nil {
			return fmt.Errorf("%w: %s过程中断开了与 app center 的连接", ErrTaskOutcomeUnknown, what)
		}
	}
	return fmt.Errorf("%w: %s超过 15 分钟仍未结束", ErrTaskOutcomeUnknown, what)
}

// removeStagedPackage deletes the daemon's unpacked copy of a package.
//
// The daemon never reaps these. A box with a handful of installs had
// accumulated 165 MB of them, including two full copies belonging to an app
// that had since been uninstalled — and because FetchWizard stages too, merely
// PREVIEWING an install wizard and cancelling leaves one behind.
//
// This runs as root, so an unexpected path is left alone rather than removed:
// only an absolute `.../appcenter-downloads/<something>-tpk` is touched.
func removeStagedPackage(p string) {
	if p == "" {
		return
	}
	clean := filepath.Clean(p)
	if !filepath.IsAbs(clean) ||
		!strings.HasSuffix(clean, stagedDirSuffix) ||
		filepath.Base(filepath.Dir(clean)) != stagedDirParent {
		log.Printf("removeStagedPackage: refusing to remove unexpected path %q", p)
		return
	}
	if err := os.RemoveAll(clean); err != nil {
		log.Printf("removeStagedPackage: %s: %v", clean, err)
	}
}

// Shape of the daemon's staging area, e.g.
// /vol1/appcenter-downloads/openlist-4.2.5-tpk
const (
	stagedDirParent = "appcenter-downloads"
	stagedDirSuffix = "-tpk"
)

// DaemonUpgradeAvailable reports whether the daemon's upgrade channel is
// usable, so the store can prefer it and fall back to refusing rather than
// assuming either outcome from the fnOS version string alone.
func (a *LinuxAppCenter) DaemonUpgradeAvailable() bool {
	if _, err := net.DialTimeout("unix", daemonSocket, 2*time.Second); err != nil {
		return false
	}
	// An empty body must be REJECTED by a route that exists (validation error),
	// and produce a transport/404-shaped failure when it does not.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := daemonCall(ctx, routeUpdateInfo, map[string]any{}, nil)
	var de *DaemonError
	if errors.As(err, &de) {
		return de.Code == daemonCodeValidation
	}
	return false
}

// DaemonInstallAvailable reports whether the daemon's INSTALL channel is
// reachable.
//
// Deliberately separate from DaemonUpgradeAvailable: they probe different
// routes, and conflating them means any change to the UPDATE probe silently
// reroutes fresh installs onto install-local — the destructive path — without
// anything about installs having actually broken.
func (a *LinuxAppCenter) DaemonInstallAvailable() bool {
	if _, err := net.DialTimeout("unix", daemonSocket, 2*time.Second); err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Measured on 1.2.0505: an empty body yields 10030 on a route that exists,
	// while a missing route answers a plain-text 404 that fails JSON decoding
	// and therefore never produces a DaemonError.
	err := daemonCall(ctx, routeInstallInfo, map[string]any{}, nil)
	var de *DaemonError
	if errors.As(err, &de) {
		return de.Code == daemonCodeValidation
	}
	return false
}

// FetchWizard stages an fpk and returns its install-time form definition
// WITHOUT installing anything.
//
// Staging is a read-only operation from the app's point of view: the daemon
// unpacks the package to its download area and identifies it. Verified on a
// live box that repeated staging leaves an installed app and its @appdata
// untouched.
func (a *LinuxAppCenter) FetchWizard(ctx context.Context, fpkPath string) (*AppWizard, error) {
	staged, err := a.StageFpk(ctx, fpkPath)
	if err != nil {
		return nil, err
	}
	// Nothing runs off this staging directory, so it can always be reaped — a
	// wizard the user then cancels must not leave a full unpacked copy behind.
	defer removeStagedPackage(staged.Path)

	route := routeInstallInfo
	body := map[string]any{
		"appName":     staged.AppName,
		"version":     staged.Version,
		"packageType": staged.PackageType,
		"language":    "zh-CN",
	}
	if staged.Installed {
		// An installed app answers on the update route; the install route
		// would reject it.
		route = routeUpdateInfo
		body = map[string]any{
			"appName":       staged.AppName,
			"updateVersion": staged.Version,
			"packageType":   staged.PackageType,
			"language":      "zh-CN",
		}
	}

	var info infoResponse
	if err := daemonCall(ctx, route, body, &info); err != nil {
		return nil, fmt.Errorf("读取安装向导失败: %w", err)
	}
	w := info.WizardInfo
	return &AppWizard{
		AppName:         staged.AppName,
		Version:         staged.Version,
		HasWizard:       w.HasWizard,
		Content:         w.WizardContent,
		InstallVolumeID: w.InstalledVolumeID,
	}, nil
}

// InstallFpkWithWizard installs a NOT-YET-INSTALLED app through the daemon,
// passing the user's answers to the app's install wizard.
//
// This is what makes third-party installs equivalent to the native App
// Center's: apps that need a token, password or path can ask for it instead of
// silently starting up misconfigured. The daemon enforces the app's own
// required-field rules and answers 19000 naming any field left empty.
func (a *LinuxAppCenter) InstallFpkWithWizard(ctx context.Context, fpkPath string, volume int, params []WizardParam) error {
	staged, err := a.StageFpk(ctx, fpkPath)
	if err != nil {
		return err
	}
	if staged.Installed {
		// Routing an installed app here would make the daemon treat it as a
		// fresh install; callers must use UpgradeFpk, which preserves data.
		return fmt.Errorf("%s 已安装，请使用更新功能", staged.AppName)
	}
	if volume <= 0 {
		return fmt.Errorf("未指定安装存储卷")
	}
	if params == nil {
		params = []WizardParam{}
	}

	var task struct {
		TaskID string `json:"taskId"`
	}
	if err := daemonCall(ctx, routeInstallTask, map[string]any{
		"appName":     staged.AppName,
		"version":     staged.Version,
		"packageType": staged.PackageType,
		"systemParameters": map[string]any{
			"agreedToProtocol": true,
			"installVolumeID":  volume,
			"dataVolumeId":     volume,
			"immediateStart":   false,
		},
		"customParameters": params,
		"language":         "zh-CN",
	}, &task); err != nil {
		return fmt.Errorf("安装失败: %w", err)
	}
	if err := a.waitTask(ctx, task.TaskID, "安装"); err != nil {
		return err
	}
	removeStagedPackage(staged.Path)
	return nil
}
