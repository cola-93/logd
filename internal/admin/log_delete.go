package admin

import (
	"net/http"
	"strings"

	"logd/internal/store"
)

func (h *Handler) deleteSelectedLogsPage(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Items []store.LogRef `json:"items"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeJSONError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}
	if len(request.Items) == 0 {
		writeJSONError(w, http.StatusBadRequest, "EMPTY_SELECTION", "请选择要删除的日志")
		return
	}
	for _, item := range request.Items {
		if !uuidFormat.MatchString(item.EventID) ||
			!validLevel(item.Level) ||
			item.EventTime.IsZero() {
			writeJSONError(w, http.StatusBadRequest, "INVALID_LOG_REF", "日志定位信息无效")
			return
		}
	}

	result, err := h.db.DeleteLogs(r.Context(), request.Items)
	if err != nil {
		h.logger.Error("delete selected logs", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "删除日志失败")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) deleteFilteredLogsPage(w http.ResponseWriter, r *http.Request) {
	filter, err := h.parseLogFilters(r, false)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "INVALID_FILTER", err.Error())
		return
	}

	result, err := h.db.DeleteLogsByFilter(r.Context(), filter.Value)
	if err != nil {
		h.logger.Error("delete filtered logs", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "删除日志失败")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) deleteScopedLogsPage(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeJSONError(w, http.StatusBadRequest, "INVALID_FORM", "请求格式错误")
		return
	}

	projectKey := strings.TrimSpace(r.FormValue("project_key"))
	nodeKey := strings.TrimSpace(r.FormValue("node_key"))
	level := strings.TrimSpace(r.FormValue("level"))
	if projectKey == "" {
		writeJSONError(w, http.StatusBadRequest, "INVALID_SCOPE", "项目不能为空")
		return
	}
	if level != "" && !validLevel(level) {
		writeJSONError(w, http.StatusBadRequest, "INVALID_SCOPE", "级别无效")
		return
	}

	result, err := h.db.DeleteLogsByScope(r.Context(), projectKey, nodeKey, level)
	if err != nil {
		h.logger.Error("delete scoped logs", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "删除日志失败")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) setSelectedLogsLockedPage(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Items  []store.LogRef `json:"items"`
		Locked *bool          `json:"locked"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeJSONError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}
	if len(request.Items) == 0 || request.Locked == nil {
		writeJSONError(w, http.StatusBadRequest, "INVALID_LOCK_REQUEST", "请选择要锁定或解锁的日志")
		return
	}
	for _, item := range request.Items {
		if !uuidFormat.MatchString(item.EventID) ||
			!validLevel(item.Level) ||
			item.EventTime.IsZero() {
			writeJSONError(w, http.StatusBadRequest, "INVALID_LOG_REF", "日志定位信息无效")
			return
		}
	}

	updated, err := h.db.SetLogsLocked(r.Context(), request.Items, *request.Locked)
	if err != nil {
		h.logger.Error("update selected log lock state", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "更新锁定状态失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"updated": updated})
}
