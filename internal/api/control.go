package api

import "net/http"

// handleStartStop 同步 fnOS 应用中心的启动/停用：
// POST /api/apps/{appname}/start
// POST /api/apps/{appname}/stop
// 仅对已安装应用生效；执行走 appcenter-cli 串行队列，
// 与安装/更新/卸载互斥，避免并发操作同一应用。
func (s *Server) handleStartStop(w http.ResponseWriter, r *http.Request, action string) {
	appname := r.PathValue("appname")
	if appname == "" {
		writeAPIError(w, http.StatusBadRequest, "appname is required")
		return
	}

	app, ok := s.getRegistryApp(appname)
	if !ok {
		writeAPIError(w, http.StatusNotFound, "app not found")
		return
	}
	if !app.Installed {
		writeAPIError(w, http.StatusConflict, "该应用尚未安装")
		return
	}

	if !s.queue.TryStart(action, appname) {
		writeAPIError(w, http.StatusConflict, "another operation is already running")
		return
	}
	defer s.queue.FinishApp(appname)

	verb, cliErr := "启动", s.queue.WithCLI(func() error { return s.ac.Start(appname) })
	if action == "stop" {
		verb, cliErr = "停用", s.queue.WithCLI(func() error { return s.ac.Stop(appname) })
	}
	if cliErr != nil {
		writeAPIError(w, http.StatusInternalServerError, verb+"失败: "+cliErr.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "appname": appname})
}
