package admin

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"logd/internal/monitor"
	"logd/internal/store"
)

var (
	logLevels  = []string{"INFO", "WARN", "ERROR", "DEBUG"}
	uuidFormat = regexp.MustCompile(
		`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`,
	)
)

const (
	settingsSectionBasic  = "basic"
	settingsSectionTokens = "tokens"
	settingsSectionStatus = "status"
)

type pageData struct {
	Title   string
	ShowNav bool
	Active  string
	CSRF    string
	Error   string
	Notice  string
	Version string
}

type loginPageData struct {
	pageData
}

type logFilterForm struct {
	StartTime  string
	EndTime    string
	ProjectKey string
	NodeKey    string
	Level      string
	ErrorScene string
	RequestIP  string
	Member     string
	SessionID  string
}

type parsedLogFilter struct {
	Value store.LogFilter
	Form  logFilterForm
}

type logRow struct {
	store.LogSummary
	DetailURL string
}

type LevelNode struct {
	ProjectKey string
	NodeKey    string
	Level      string
}

type NodeBranch struct {
	ProjectKey string
	NodeKey    string
	Levels     []LevelNode
}

type ProjectNode struct {
	ProjectKey string
	Nodes      []NodeBranch
}

type logsPageData struct {
	pageData
	Filters logFilterForm
	Levels  []string
	Logs    []logRow
	HasMore bool
	PrevURL string
	NextURL string
	Tree    []ProjectNode
}

type detailPageData struct {
	pageData
	Detail  store.LogDetail
	BackURL string
}

type settingsPageData struct {
	pageData
	Section       string
	Settings      store.SystemSettings
	Tokens        []store.ProjectTokenRecord
	NewToken      string
	DatabaseOK    bool
	QueueDepth    int
	QueueCapacity int
	LastWrite     string
	Alerts        []monitor.Alert
}

type cursorValue struct {
	EventTime time.Time `json:"event_time"`
	EventID   string    `json:"event_id"`
}

func (h *Handler) basePageData(r *http.Request, title, active string) pageData {
	nonce, _ := h.sessionNonce(r)
	return pageData{
		Title:   title,
		ShowNav: true,
		Active:  active,
		CSRF:    h.csrfToken(nonce),
		Version: "1.0.0",
	}
}

func (h *Handler) logsPage(w http.ResponseWriter, r *http.Request) {
	hierarchyItems, err := h.db.LogHierarchy(r.Context())
	if err != nil {
		h.logger.Warn("load log hierarchy error", "error", err)
	}
	tree := buildHierarchyTree(hierarchyItems)

	filter, err := h.parseLogFilters(r, false)
	if err != nil {
		data := h.basePageData(r, "日志列表", "logs")
		data.Error = err.Error()
		h.render(w, "logs", http.StatusBadRequest, logsPageData{
			pageData: data,
			Levels:   logLevels,
			Tree:     tree,
		})
		return
	}

	items, hasMore, err := h.db.QueryLogs(r.Context(), filter.Value)
	if err != nil {
		h.serverError(w, r, err)
		return
	}

	backURL := r.URL.RequestURI()
	if backURL == "" {
		backURL = "/admin/logs"
	}
	rows := make([]logRow, 0, len(items))
	for _, item := range items {
		rows = append(rows, logRow{
			LogSummary: item,
			DetailURL:  h.detailURL(item, backURL),
		})
	}

	currentCursor := strings.TrimSpace(r.URL.Query().Get("cursor"))
	currentTrail := strings.TrimSpace(r.URL.Query().Get("trail"))

	prevURL := ""
	if currentCursor != "" {
		if currentTrail == "" || currentTrail == "_" {
			prevURL = buildLogsPageURL(filter.Form, "", "")
		} else {
			parts := strings.Split(currentTrail, ",")
			targetCursor := parts[len(parts)-1]
			remainingParts := parts[:len(parts)-1]
			newTrail := strings.Join(remainingParts, ",")
			if targetCursor == "_" {
				targetCursor = ""
				newTrail = ""
			}
			prevURL = buildLogsPageURL(filter.Form, targetCursor, newTrail)
		}
	}

	nextURL := ""
	if hasMore && len(items) > 0 {
		cursor, err := encodeCursor(items[len(items)-1])
		if err != nil {
			h.serverError(w, r, err)
			return
		}
		var nextTrail string
		if currentCursor == "" {
			nextTrail = "_"
		} else {
			if currentTrail == "" {
				currentTrail = "_"
			}
			nextTrail = currentTrail + "," + currentCursor
		}
		nextURL = buildLogsPageURL(filter.Form, cursor, nextTrail)
	}

	data := logsPageData{
		pageData: h.basePageData(r, "日志列表", "logs"),
		Filters:  filter.Form,
		Levels:   logLevels,
		Logs:     rows,
		HasMore:  hasMore,
		PrevURL:  prevURL,
		NextURL:  nextURL,
		Tree:     tree,
	}
	if deleted := strings.TrimSpace(r.URL.Query().Get("deleted")); deleted != "" {
		data.Notice = "已删除 " + deleted + " 条日志"
		if skipped := strings.TrimSpace(r.URL.Query().Get("skipped_locked")); skipped != "" && skipped != "0" {
			data.Notice += "，跳过 " + skipped + " 条已锁定日志"
		}
	}
	h.render(w, "logs", http.StatusOK, data)
}

func buildHierarchyTree(items []store.LogHierarchyItem) []ProjectNode {
	type nodeKey struct {
		project string
		node    string
	}
	type levelKey struct {
		project string
		node    string
		level   string
	}

	projectMap := make(map[string]*ProjectNode)
	var projectOrder []string

	nodeMap := make(map[nodeKey]*NodeBranch)
	var nodeOrder []nodeKey
	levelSet := make(map[levelKey]struct{})

	for _, item := range items {
		if item.ProjectKey == "" {
			continue
		}
		if _, ok := projectMap[item.ProjectKey]; !ok {
			projectMap[item.ProjectKey] = &ProjectNode{ProjectKey: item.ProjectKey}
			projectOrder = append(projectOrder, item.ProjectKey)
		}
		if item.NodeKey == "" {
			continue
		}
		nk := nodeKey{project: item.ProjectKey, node: item.NodeKey}
		if _, ok := nodeMap[nk]; !ok {
			nodeMap[nk] = &NodeBranch{ProjectKey: item.ProjectKey, NodeKey: item.NodeKey}
			nodeOrder = append(nodeOrder, nk)
		}
		if item.Level == "" {
			continue
		}
		lk := levelKey{project: item.ProjectKey, node: item.NodeKey, level: item.Level}
		if _, ok := levelSet[lk]; !ok {
			levelSet[lk] = struct{}{}
			nb := nodeMap[nk]
			nb.Levels = append(nb.Levels, LevelNode{
				ProjectKey: item.ProjectKey,
				NodeKey:    item.NodeKey,
				Level:      item.Level,
			})
		}
	}

	for _, nk := range nodeOrder {
		projectMap[nk.project].Nodes = append(projectMap[nk.project].Nodes, *nodeMap[nk])
	}

	result := make([]ProjectNode, 0, len(projectOrder))
	for _, project := range projectOrder {
		result = append(result, *projectMap[project])
	}
	return result
}

func (h *Handler) detailPage(w http.ResponseWriter, r *http.Request) {
	eventID, level, eventTime, err := h.parseDetailQuery(r)
	if err != nil {
		h.pageError(w, r, "日志详情", "logs", http.StatusBadRequest, err.Error())
		return
	}

	detail, err := h.db.LogDetail(r.Context(), eventID, level, eventTime)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			h.pageError(w, r, "日志详情", "logs", http.StatusNotFound, "日志不存在")
			return
		}
		h.serverError(w, r, err)
		return
	}

	page := h.basePageData(r, "日志详情", "detail")
	page.ShowNav = false
	data := detailPageData{
		pageData: page,
		Detail:   detail,
		BackURL:  safeBackURL(r.URL.Query().Get("back")),
	}
	h.render(w, "detail", http.StatusOK, data)
}

func (h *Handler) tokensPage(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/admin/settings?section="+settingsSectionTokens, http.StatusSeeOther)
}

func (h *Handler) createTokenPage(w http.ResponseWriter, r *http.Request) {
	nonce, _ := h.sessionNonce(r)
	if !h.validCSRF(r, nonce) {
		h.renderSettingsPage(w, r, http.StatusForbidden, settingsSectionTokens, "CSRF 校验失败", "")
		return
	}

	token, record, err := h.createTokenFromForm(r)
	if err != nil {
		h.renderSettingsPage(w, r, http.StatusBadRequest, settingsSectionTokens, err.Error(), "")
		return
	}
	_ = record
	h.renderSettingsPage(w, r, http.StatusOK, settingsSectionTokens, "", token)
}

func (h *Handler) updateTokenPage(w http.ResponseWriter, r *http.Request) {
	nonce, _ := h.sessionNonce(r)
	if !h.validCSRF(r, nonce) {
		h.renderSettingsPage(w, r, http.StatusForbidden, settingsSectionTokens, "CSRF 校验失败", "")
		return
	}

	if err := h.updateTokenFromForm(r); err != nil {
		h.renderSettingsPage(w, r, http.StatusBadRequest, settingsSectionTokens, err.Error(), "")
		return
	}
	http.Redirect(w, r, "/admin/settings?section=tokens&saved=1", http.StatusSeeOther)
}

func (h *Handler) deleteTokenPage(w http.ResponseWriter, r *http.Request) {
	nonce, _ := h.sessionNonce(r)
	if !h.validCSRF(r, nonce) {
		h.renderSettingsPage(w, r, http.StatusForbidden, settingsSectionTokens, "CSRF 校验失败", "")
		return
	}

	id := r.PathValue("id")
	if !uuidFormat.MatchString(id) {
		h.renderSettingsPage(w, r, http.StatusBadRequest, settingsSectionTokens, "Token ID 无效", "")
		return
	}
	deleted, err := h.db.DeleteProjectToken(r.Context(), id)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	if deleted {
		if err := h.db.RefreshTokenCache(r.Context(), h.tokens); err != nil {
			h.serverError(w, r, err)
			return
		}
	}
	http.Redirect(w, r, "/admin/settings?section=tokens&deleted=1", http.StatusSeeOther)
}

func (h *Handler) settingsPage(w http.ResponseWriter, r *http.Request) {
	section := normalizeSettingsSection(r.URL.Query().Get("section"))
	data, err := h.loadSettingsPageData(r, section)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	switch section {
	case settingsSectionBasic:
		if r.URL.Query().Get("saved") == "1" {
			data.Notice = "系统设置已保存"
		}
	case settingsSectionTokens:
		switch r.URL.Query().Get("saved") {
		case "1":
			data.Notice = "Token 已保存"
		case "deleted":
			data.Notice = "Token 已删除"
		}
	}
	h.render(w, "settings", http.StatusOK, data)
}

func (h *Handler) updateSettingsPage(w http.ResponseWriter, r *http.Request) {
	nonce, _ := h.sessionNonce(r)
	if !h.validCSRF(r, nonce) {
		h.renderSettingsPage(w, r, http.StatusForbidden, settingsSectionBasic, "CSRF 校验失败", "")
		return
	}
	if err := r.ParseForm(); err != nil {
		h.renderSettingsPage(w, r, http.StatusBadRequest, settingsSectionBasic, "请求格式错误", "")
		return
	}

	retentionDays, err := strconv.Atoi(r.FormValue("retention_days"))
	if err != nil {
		h.renderSettingsPage(w, r, http.StatusBadRequest, settingsSectionBasic, "保留天数无效", "")
		return
	}
	if err := h.updateSettings(r, &retentionDays); err != nil {
		h.renderSettingsPage(w, r, http.StatusBadRequest, settingsSectionBasic, err.Error(), "")
		return
	}
	http.Redirect(w, r, "/admin/settings?saved=1", http.StatusSeeOther)
}

func (h *Handler) statusPage(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/admin/settings?section="+settingsSectionStatus, http.StatusSeeOther)
}

func normalizeSettingsSection(value string) string {
	switch value {
	case settingsSectionTokens, settingsSectionStatus:
		return value
	default:
		return settingsSectionBasic
	}
}

func (h *Handler) loadSettingsPageData(
	r *http.Request,
	section string,
) (settingsPageData, error) {
	settings, err := h.db.SystemSettings(r.Context())
	if err != nil {
		return settingsPageData{}, err
	}
	tokens, err := h.db.ListProjectTokens(r.Context())
	if err != nil {
		return settingsPageData{}, err
	}

	data := settingsPageData{
		pageData:      h.basePageData(r, "系统设置", "settings"),
		Section:       section,
		Settings:      settings,
		Tokens:        tokens,
		DatabaseOK:    h.db.Ping(r.Context()) == nil,
		QueueDepth:    h.queue.Depth(),
		QueueCapacity: h.queue.Capacity(),
		Alerts:        h.monitor.ActiveAlerts(),
	}
	if lastWrite := h.lastWrite(); !lastWrite.IsZero() {
		data.LastWrite = lastWrite.In(h.location).Format("2006-01-02 15:04:05")
	}
	return data, nil
}

func (h *Handler) renderSettingsPage(
	w http.ResponseWriter,
	r *http.Request,
	status int,
	section string,
	message string,
	newToken string,
) {
	data, err := h.loadSettingsPageData(r, normalizeSettingsSection(section))
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	data.Error = message
	data.NewToken = newToken
	h.render(w, "settings", status, data)
}

func (h *Handler) pageError(
	w http.ResponseWriter,
	r *http.Request,
	title string,
	active string,
	status int,
	message string,
) {
	data := h.basePageData(r, title, active)
	data.Error = message
	h.render(w, "logs", status, logsPageData{
		pageData: data,
		Levels:   logLevels,
	})
}

func (h *Handler) parseLogFilters(r *http.Request, requireTime bool) (parsedLogFilter, error) {
	if err := r.ParseForm(); err != nil {
		return parsedLogFilter{}, fmt.Errorf("请求格式错误")
	}
	query := r.Form
	startValue := strings.TrimSpace(query.Get("start_time"))
	endValue := strings.TrimSpace(query.Get("end_time"))

	var startTime, endTime time.Time
	var err error

	if requireTime && (startValue == "" || endValue == "") {
		return parsedLogFilter{}, fmt.Errorf("start_time 和 end_time 不能为空")
	}

	if startValue != "" {
		startTime, err = parseTime(startValue, h.location)
		if err != nil {
			return parsedLogFilter{}, fmt.Errorf("start_time 格式无效")
		}
	}
	if endValue != "" {
		endTime, err = parseTime(endValue, h.location)
		if err != nil {
			return parsedLogFilter{}, fmt.Errorf("end_time 格式无效")
		}
	}
	if !startTime.IsZero() && !endTime.IsZero() {
		if !startTime.Before(endTime) {
			return parsedLogFilter{}, fmt.Errorf("start_time 必须早于 end_time")
		}
		if endTime.Sub(startTime) > 31*24*time.Hour {
			return parsedLogFilter{}, fmt.Errorf("查询时间跨度不能超过 31 天")
		}
	}

	level := strings.TrimSpace(query.Get("level"))
	if level != "" && !validLevel(level) {
		return parsedLogFilter{}, fmt.Errorf("level 无效")
	}
	requestIP := strings.TrimSpace(query.Get("request_ip"))
	if requestIP != "" && net.ParseIP(requestIP) == nil {
		return parsedLogFilter{}, fmt.Errorf("request_ip 无效")
	}

	limit := 100
	if value := strings.TrimSpace(query.Get("limit")); value != "" {
		limit, err = strconv.Atoi(value)
		if err != nil || limit < 1 || limit > 500 {
			return parsedLogFilter{}, fmt.Errorf("limit 必须在 1 到 500 之间")
		}
	}

	filter := store.LogFilter{
		StartTime:  startTime,
		EndTime:    endTime,
		ProjectKey: strings.TrimSpace(query.Get("project_key")),
		NodeKey:    strings.TrimSpace(query.Get("node_key")),
		Level:      level,
		ErrorScene: strings.TrimSpace(query.Get("error_scene")),
		RequestIP:  requestIP,
		Member:     strings.TrimSpace(query.Get("member")),
		SessionID:  strings.TrimSpace(query.Get("session_id")),
		Limit:      limit,
	}
	if cursor := strings.TrimSpace(query.Get("cursor")); cursor != "" {
		value, err := decodeCursor(cursor)
		if err != nil {
			return parsedLogFilter{}, err
		}
		filter.CursorTime = value.EventTime
		filter.CursorID = value.EventID
	}

	return parsedLogFilter{
		Value: filter,
		Form: logFilterForm{
			StartTime:  startValue,
			EndTime:    endValue,
			ProjectKey: filter.ProjectKey,
			NodeKey:    filter.NodeKey,
			Level:      filter.Level,
			ErrorScene: filter.ErrorScene,
			RequestIP:  filter.RequestIP,
			Member:     filter.Member,
			SessionID:  filter.SessionID,
		},
	}, nil
}

func (h *Handler) parseDetailQuery(r *http.Request) (string, string, time.Time, error) {
	eventID := r.PathValue("event_id")
	if !uuidFormat.MatchString(eventID) {
		return "", "", time.Time{}, fmt.Errorf("event_id 无效")
	}
	level := strings.TrimSpace(r.URL.Query().Get("level"))
	if !validLevel(level) {
		return "", "", time.Time{}, fmt.Errorf("level 无效")
	}
	eventTime, err := parseTime(r.URL.Query().Get("event_time"), h.location)
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("event_time 格式无效")
	}
	return eventID, level, eventTime, nil
}

func (h *Handler) detailURL(item store.LogSummary, backURL string) string {
	values := url.Values{}
	values.Set("level", item.Level)
	values.Set("event_time", item.EventTime.Format(time.RFC3339Nano))
	if backURL != "" {
		values.Set("back", backURL)
	}
	return "/admin/logs/" + item.EventID + "?" + values.Encode()
}

func buildLogsPageURL(form logFilterForm, cursor string, trail string) string {
	values := url.Values{}
	setIfNotEmpty(values, "start_time", form.StartTime)
	setIfNotEmpty(values, "end_time", form.EndTime)
	setIfNotEmpty(values, "project_key", form.ProjectKey)
	setIfNotEmpty(values, "node_key", form.NodeKey)
	setIfNotEmpty(values, "level", form.Level)
	setIfNotEmpty(values, "error_scene", form.ErrorScene)
	setIfNotEmpty(values, "request_ip", form.RequestIP)
	setIfNotEmpty(values, "member", form.Member)
	setIfNotEmpty(values, "session_id", form.SessionID)
	setIfNotEmpty(values, "cursor", cursor)
	setIfNotEmpty(values, "trail", trail)
	return "/admin/logs?" + values.Encode()
}

func setIfNotEmpty(values url.Values, key, value string) {
	if value != "" {
		values.Set(key, value)
	}
}

func encodeCursor(item store.LogSummary) (string, error) {
	value, err := json.Marshal(cursorValue{
		EventTime: item.EventTime,
		EventID:   item.EventID,
	})
	if err != nil {
		return "", fmt.Errorf("encode cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func decodeCursor(value string) (cursorValue, error) {
	var cursor cursorValue
	data, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return cursor, fmt.Errorf("cursor 无效")
	}
	if err := json.Unmarshal(data, &cursor); err != nil {
		return cursor, fmt.Errorf("cursor 无效")
	}
	if cursor.EventTime.IsZero() || !uuidFormat.MatchString(cursor.EventID) {
		return cursor, fmt.Errorf("cursor 无效")
	}
	return cursor, nil
}

func parseTime(value string, location *time.Location) (time.Time, error) {
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02T15:04:05",
		"2006-01-02T15:04",
	} {
		if layout == time.RFC3339Nano {
			if parsed, err := time.Parse(layout, value); err == nil {
				return parsed, nil
			}
			continue
		}
		if parsed, err := time.ParseInLocation(layout, value, location); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("time is invalid")
}

func safeBackURL(value string) string {
	if value == "" {
		return "/admin/logs"
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || parsed.Path != "/admin/logs" {
		return "/admin/logs"
	}
	return parsed.String()
}

func validLevel(level string) bool {
	for _, value := range logLevels {
		if level == value {
			return true
		}
	}
	return false
}

func validateName(value string, max int) error {
	length := utf8.RuneCountInString(value)
	if length < 1 || length > max {
		return fmt.Errorf("长度必须在 1 到 %d 个字符之间", max)
	}
	return nil
}
