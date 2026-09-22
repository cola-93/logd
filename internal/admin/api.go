package admin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"logd/internal/store"
)

type tokenCreateInput struct {
	ProjectKey string
	Name       string
	Enabled    bool
}

type tokenUpdateInput struct {
	ProjectKey *string
	Name       *string
	Enabled    *bool
}

func (h *Handler) logsAPI(w http.ResponseWriter, r *http.Request) {
	filter, err := h.parseLogFilters(r, true)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "INVALID_QUERY", err.Error())
		return
	}

	items, hasMore, err := h.db.QueryLogs(r.Context(), filter.Value)
	if err != nil {
		h.logger.Error("query logs API", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "查询日志失败")
		return
	}

	nextCursor := ""
	if hasMore && len(items) > 0 {
		nextCursor, err = encodeCursor(items[len(items)-1])
		if err != nil {
			h.logger.Error("encode logs cursor", "error", err)
			writeJSONError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "生成分页游标失败")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":       items,
		"next_cursor": nextCursor,
		"has_more":    hasMore,
	})
}

func (h *Handler) detailAPI(w http.ResponseWriter, r *http.Request) {
	eventID, level, eventTime, err := h.parseDetailQuery(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "INVALID_QUERY", err.Error())
		return
	}
	detail, err := h.db.LogDetail(r.Context(), eventID, level, eventTime)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "NOT_FOUND", "日志不存在")
			return
		}
		h.logger.Error("load log detail API", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "读取日志详情失败")
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (h *Handler) listTokensAPI(w http.ResponseWriter, r *http.Request) {
	tokens, err := h.db.ListProjectTokens(r.Context())
	if err != nil {
		h.logger.Error("list project tokens API", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "读取 Token 失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": tokens})
}

func (h *Handler) createTokenAPI(w http.ResponseWriter, r *http.Request) {
	var request struct {
		ProjectKey string `json:"project_key"`
		Name       string `json:"name"`
		Enabled    *bool  `json:"enabled"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeJSONError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}
	enabled := true
	if request.Enabled != nil {
		enabled = *request.Enabled
	}
	token, record, err := h.createToken(
		r.Context(),
		tokenCreateInput{
			ProjectKey: strings.TrimSpace(request.ProjectKey),
			Name:       strings.TrimSpace(request.Name),
			Enabled:    enabled,
		},
	)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "INVALID_TOKEN", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"token":         token,
		"project_token": record,
	})
}

func (h *Handler) updateTokenAPI(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !uuidFormat.MatchString(id) {
		writeJSONError(w, http.StatusBadRequest, "INVALID_TOKEN_ID", "Token ID 无效")
		return
	}
	var request tokenUpdateInput
	if err := decodeJSON(w, r, &request); err != nil {
		writeJSONError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}
	if request.ProjectKey != nil {
		value := strings.TrimSpace(*request.ProjectKey)
		request.ProjectKey = &value
	}
	if request.Name != nil {
		value := strings.TrimSpace(*request.Name)
		request.Name = &value
	}

	record, err := h.updateToken(r.Context(), id, request)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "NOT_FOUND", "Token 不存在")
			return
		}
		writeJSONError(w, http.StatusBadRequest, "INVALID_TOKEN", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, record)
}

func (h *Handler) deleteTokenAPI(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !uuidFormat.MatchString(id) {
		writeJSONError(w, http.StatusBadRequest, "INVALID_TOKEN_ID", "Token ID 无效")
		return
	}
	deleted, err := h.db.DeleteProjectToken(r.Context(), id)
	if err != nil {
		h.logger.Error("delete project token API", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "删除 Token 失败")
		return
	}
	if !deleted {
		writeJSONError(w, http.StatusNotFound, "NOT_FOUND", "Token 不存在")
		return
	}
	if err := h.db.RefreshTokenCache(r.Context(), h.tokens); err != nil {
		h.logger.Error("refresh project token cache", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "刷新 Token 缓存失败")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) settingsAPI(w http.ResponseWriter, r *http.Request) {
	settings, err := h.db.SystemSettings(r.Context())
	if err != nil {
		h.logger.Error("load settings API", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "读取系统设置失败")
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

func (h *Handler) updateSettingsAPI(w http.ResponseWriter, r *http.Request) {
	var request struct {
		RetentionDays *int `json:"retention_days"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeJSONError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}

	if err := h.updateSettings(r, request.RetentionDays); err != nil {
		writeJSONError(w, http.StatusBadRequest, "INVALID_SETTINGS", err.Error())
		return
	}
	settings, err := h.db.SystemSettings(r.Context())
	if err != nil {
		h.logger.Error("reload settings API", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "读取系统设置失败")
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

func (h *Handler) statusAPI(w http.ResponseWriter, r *http.Request) {
	databaseOK := h.db.Ping(r.Context()) == nil
	retentionDays := 0
	if settings, err := h.db.SystemSettings(r.Context()); err == nil {
		retentionDays = settings.RetentionDays
	} else {
		databaseOK = false
	}

	var lastWrite *time.Time
	if value := h.lastWrite(); !value.IsZero() {
		lastWrite = &value
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"database_ok":    databaseOK,
		"queue_depth":    h.queue.Depth(),
		"queue_capacity": h.queue.Capacity(),
		"retention_days": retentionDays,
		"last_write":     lastWrite,
		"alerts":         h.monitor.ActiveAlerts(),
	})
}

func (h *Handler) createTokenFromForm(r *http.Request) (string, store.ProjectTokenRecord, error) {
	if err := r.ParseForm(); err != nil {
		return "", store.ProjectTokenRecord{}, fmt.Errorf("请求格式错误")
	}
	return h.createToken(
		r.Context(),
		tokenCreateInput{
			ProjectKey: strings.TrimSpace(r.FormValue("project_key")),
			Name:       strings.TrimSpace(r.FormValue("name")),
			Enabled:    r.Form.Has("enabled"),
		},
	)
}

func (h *Handler) createToken(
	ctx context.Context,
	input tokenCreateInput,
) (string, store.ProjectTokenRecord, error) {
	if err := validateName(input.ProjectKey, 64); err != nil {
		return "", store.ProjectTokenRecord{}, fmt.Errorf("项目标识%s", err)
	}
	if err := validateName(input.Name, 100); err != nil {
		return "", store.ProjectTokenRecord{}, fmt.Errorf("名称%s", err)
	}

	id, err := newUUID()
	if err != nil {
		return "", store.ProjectTokenRecord{}, err
	}
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", store.ProjectTokenRecord{}, fmt.Errorf("generate project token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	sum := sha256.Sum256([]byte(token))

	record, err := h.db.CreateProjectToken(
		ctx,
		id,
		input.ProjectKey,
		input.Name,
		hex.EncodeToString(sum[:]),
		input.Enabled,
	)
	if err != nil {
		return "", record, err
	}
	if err := h.db.RefreshTokenCache(ctx, h.tokens); err != nil {
		return "", record, err
	}
	return token, record, nil
}

func (h *Handler) updateTokenFromForm(r *http.Request) error {
	if err := r.ParseForm(); err != nil {
		return fmt.Errorf("请求格式错误")
	}
	projectKey := strings.TrimSpace(r.FormValue("project_key"))
	name := strings.TrimSpace(r.FormValue("name"))
	enabled := r.Form.Has("enabled")
	_, err := h.updateToken(r.Context(), r.PathValue("id"), tokenUpdateInput{
		ProjectKey: &projectKey,
		Name:       &name,
		Enabled:    &enabled,
	})
	return err
}

func (h *Handler) updateToken(
	ctx context.Context,
	id string,
	input tokenUpdateInput,
) (store.ProjectTokenRecord, error) {
	if input.ProjectKey != nil {
		if err := validateName(*input.ProjectKey, 64); err != nil {
			return store.ProjectTokenRecord{}, fmt.Errorf("项目标识%s", err)
		}
	}
	if input.Name != nil {
		if err := validateName(*input.Name, 100); err != nil {
			return store.ProjectTokenRecord{}, fmt.Errorf("名称%s", err)
		}
	}

	record, err := h.db.UpdateProjectToken(ctx, id, store.ProjectTokenUpdate{
		ProjectKey: input.ProjectKey,
		Name:       input.Name,
		Enabled:    input.Enabled,
	})
	if err != nil {
		return record, err
	}
	if err := h.db.RefreshTokenCache(ctx, h.tokens); err != nil {
		return record, err
	}
	return record, nil
}

func (h *Handler) updateSettings(
	r *http.Request,
	retentionDays *int,
) error {
	if retentionDays == nil {
		return nil
	}
	if *retentionDays < 1 || *retentionDays > 3650 {
		return fmt.Errorf("保留天数必须在 1 到 3650 之间")
	}

	current, err := h.db.SystemSettings(r.Context())
	if err != nil {
		return err
	}

	if *retentionDays > current.RetentionDays {
		if err := h.partitions.Ensure(r.Context(), *retentionDays); err != nil {
			return fmt.Errorf("调整日志分区失败: %w", err)
		}
		if err := h.db.UpdateSystemSettings(r.Context(), retentionDays); err != nil {
			return err
		}
		h.onRetentionChange(*retentionDays)
		return nil
	}

	if err := h.db.UpdateSystemSettings(r.Context(), retentionDays); err != nil {
		return err
	}
	h.onRetentionChange(*retentionDays)

	if *retentionDays < current.RetentionDays {
		if err := h.partitions.Ensure(r.Context(), *retentionDays); err != nil {
			h.logger.Error("cleanup partitions after retention update", "error", err)
		}
	}
	return nil
}
