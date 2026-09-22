package ingest

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"logd/internal/monitor"
	"logd/internal/queue"
	"logd/internal/store"
)

const (
	maxBodyBytes    = 5 << 20
	maxBatchEvents  = 500
	maxHeadersBytes = 64 << 10
	maxParamsBytes  = 64 << 10
	maxStackBytes   = 64 << 10
)

var uuidPattern = regexp.MustCompile(
	`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`,
)

type Handler struct {
	queue         *queue.Queue
	tokens        *store.TokenCache
	metrics       *monitor.Metrics
	retentionDays atomic.Int64
}

type batchRequest struct {
	NodeKey string       `json:"node_key"`
	Logs    []logRequest `json:"logs"`
}

type logRequest struct {
	EventID        string          `json:"event_id"`
	EventTime      string          `json:"event_time"`
	Level          string          `json:"level"`
	RequestIP      string          `json:"request_ip"`
	Member         string          `json:"member"`
	SessionID      string          `json:"session_id"`
	RequestMethod  string          `json:"request_method"`
	RequestURL     string          `json:"request_url"`
	RequestHeaders json.RawMessage `json:"request_headers"`
	RequestParams  json.RawMessage `json:"request_params"`
	ErrorScene     string          `json:"error_scene"`
	ErrorMessage   string          `json:"error_message"`
	ErrorFile      string          `json:"error_file"`
	ErrorLine      *int            `json:"error_line"`
	ErrorStack     string          `json:"error_stack"`
}

type validationError struct {
	message string
	field   string
	index   int
}

type errorEnvelope struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Index   *int   `json:"index,omitempty"`
	Field   string `json:"field,omitempty"`
}

func New(
	events *queue.Queue,
	tokens *store.TokenCache,
	retentionDays int,
	metrics *monitor.Metrics,
) *Handler {
	handler := &Handler{
		queue:   events,
		tokens:  tokens,
		metrics: metrics,
	}
	handler.SetRetentionDays(retentionDays)
	return handler
}

func (h *Handler) SetRetentionDays(days int) {
	h.retentionDays.Store(int64(days))
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	h.serve(recorder, r)
	if h.metrics != nil {
		h.metrics.RecordIngest(recorder.status)
	}
}

func (h *Handler) serve(w http.ResponseWriter, r *http.Request) {
	token, ok := parseBearerToken(r.Header.Get("Authorization"))
	if !ok {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "invalid bearer token", -1, "")
		return
	}

	sum := sha256.Sum256([]byte(token))
	project, ok := h.tokens.Lookup(hex.EncodeToString(sum[:]))
	if !ok {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "invalid bearer token", -1, "")
		return
	}
	if !project.Enabled {
		writeError(w, http.StatusForbidden, "PROJECT_DISABLED", "project token is disabled", -1, "")
		return
	}

	body, status, code, message := readRequestBody(r)
	if status != 0 {
		writeError(w, status, code, message, -1, "")
		return
	}

	var request batchRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", "request body is not valid JSON", -1, "")
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", "request body must contain one JSON value", -1, "")
		return
	}

	events, validation := validateBatch(
		request,
		project.ProjectKey,
		int(h.retentionDays.Load()),
	)
	if validation != nil {
		writeError(
			w,
			http.StatusBadRequest,
			"INVALID_LOG",
			validation.message,
			validation.index,
			validation.field,
		)
		return
	}
	if !h.queue.Enqueue(events) {
		if h.metrics != nil {
			h.metrics.RecordQueueRejected()
		}
		writeError(w, http.StatusServiceUnavailable, "QUEUE_FULL", "ingest queue is full", -1, "")
		return
	}
	if h.metrics != nil {
		h.metrics.RecordIngestEvents(events)
	}

	writeJSON(w, http.StatusAccepted, map[string]int{"accepted": len(events)})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (r *statusRecorder) WriteHeader(status int) {
	if r.wrote {
		return
	}
	r.status = status
	r.wrote = true
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(body []byte) (int, error) {
	if !r.wrote {
		r.WriteHeader(http.StatusOK)
	}
	return r.ResponseWriter.Write(body)
}

func parseBearerToken(header string) (string, bool) {
	const prefix = "Bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return "", false
	}
	token := strings.TrimSpace(header[len(prefix):])
	return token, token != ""
}

func readRequestBody(r *http.Request) ([]byte, int, string, string) {
	defer r.Body.Close()

	reader := io.Reader(r.Body)
	encoding := strings.TrimSpace(r.Header.Get("Content-Encoding"))
	if encoding != "" && !strings.EqualFold(encoding, "gzip") {
		return nil, http.StatusBadRequest, "INVALID_ENCODING", "unsupported content encoding"
	}
	if strings.EqualFold(encoding, "gzip") {
		gzipReader, err := gzip.NewReader(r.Body)
		if err != nil {
			return nil, http.StatusBadRequest, "INVALID_BODY", "request body is not valid gzip"
		}
		defer gzipReader.Close()
		reader = gzipReader
	}

	body, err := io.ReadAll(io.LimitReader(reader, maxBodyBytes+1))
	if err != nil {
		return nil, http.StatusBadRequest, "INVALID_BODY", "read request body failed"
	}
	if len(body) > maxBodyBytes {
		return nil, http.StatusRequestEntityTooLarge, "BODY_TOO_LARGE", "decompressed request body exceeds 5MB"
	}
	return body, 0, "", ""
}

func validateBatch(
	request batchRequest,
	projectKey string,
	retentionDays int,
) ([]queue.Event, *validationError) {
	if err := validateLength(request.NodeKey, 1, 64); err != nil {
		return nil, &validationError{
			message: "node_key is invalid",
			field:   "node_key",
			index:   -1,
		}
	}
	if len(request.Logs) < 1 || len(request.Logs) > maxBatchEvents {
		return nil, &validationError{
			message: "logs must contain between 1 and 500 events",
			field:   "logs",
			index:   -1,
		}
	}

	now := time.Now()
	events := make([]queue.Event, 0, len(request.Logs))
	eventIDs := make(map[string]struct{}, len(request.Logs))

	for index, item := range request.Logs {
		if !uuidPattern.MatchString(item.EventID) {
			return nil, invalid(index, "event_id", "event_id is not a valid UUID")
		}
		eventID := strings.ToLower(item.EventID)
		if _, exists := eventIDs[eventID]; exists {
			return nil, invalid(index, "event_id", "event_id is duplicated in batch")
		}
		eventIDs[eventID] = struct{}{}

		eventTime, err := time.Parse(time.RFC3339Nano, item.EventTime)
		if err != nil {
			return nil, invalid(index, "event_time", "event_time is invalid")
		}
		if eventTime.After(now.Add(5 * time.Minute)) {
			return nil, invalid(index, "event_time", "event_time is too far in the future")
		}
		if item.Level == "ERROR" {
			if eventTime.Before(now.AddDate(0, 0, -31)) {
				return nil, invalid(index, "event_time", "ERROR event_time is older than 31 days")
			}
		} else if eventTime.Before(now.AddDate(0, 0, -retentionDays)) {
			return nil, invalid(index, "event_time", "event_time is outside the retention window")
		}

		if !validLevel(item.Level) {
			return nil, invalid(index, "level", "level must be INFO, WARN, ERROR or DEBUG")
		}
		requestIP := net.ParseIP(item.RequestIP)
		if requestIP == nil {
			return nil, invalid(index, "request_ip", "request_ip is invalid")
		}
		if utf8.RuneCountInString(item.Member) > 255 {
			return nil, invalid(index, "member", "member is too long")
		}
		if utf8.RuneCountInString(item.SessionID) > 255 {
			return nil, invalid(index, "session_id", "session_id is too long")
		}
		if err := validateLength(item.RequestMethod, 1, 16); err != nil {
			return nil, invalid(index, "request_method", "request_method is invalid")
		}
		if err := validateLength(item.RequestURL, 1, 8192); err != nil {
			return nil, invalid(index, "request_url", "request_url is invalid")
		}
		if err := validateJSONObject(item.RequestHeaders, maxHeadersBytes); err != nil {
			return nil, invalid(index, "request_headers", err.Error())
		}
		if err := validateJSONObject(item.RequestParams, maxParamsBytes); err != nil {
			return nil, invalid(index, "request_params", err.Error())
		}
		if err := validateLength(item.ErrorScene, 1, 255); err != nil {
			return nil, invalid(index, "error_scene", "error_scene is invalid")
		}
		if err := validateLength(item.ErrorMessage, 1, 16<<10); err != nil {
			return nil, invalid(index, "error_message", "error_message is invalid")
		}
		if len(item.ErrorFile) > 2048 {
			return nil, invalid(index, "error_file", "error_file is too long")
		}
		if item.ErrorLine != nil && *item.ErrorLine < 1 {
			return nil, invalid(index, "error_line", "error_line must be positive")
		}
		if len(item.ErrorStack) > maxStackBytes {
			return nil, invalid(index, "error_stack", "error_stack is too long")
		}

		event := queue.Event{
			EventID:        eventID,
			EventTime:      eventTime,
			Level:          item.Level,
			ProjectKey:     projectKey,
			NodeKey:        request.NodeKey,
			RequestIP:      requestIP.String(),
			Member:         item.Member,
			SessionID:      item.SessionID,
			RequestMethod:  strings.ToUpper(item.RequestMethod),
			RequestURL:     item.RequestURL,
			RequestHeaders: item.RequestHeaders,
			RequestParams:  item.RequestParams,
			ErrorScene:     item.ErrorScene,
			ErrorMessage:   item.ErrorMessage,
			ErrorFile:      item.ErrorFile,
			ErrorStack:     item.ErrorStack,
		}
		if item.ErrorLine != nil {
			event.ErrorLine = *item.ErrorLine
		}
		events = append(events, event)
	}
	return events, nil
}

func validateLength(value string, minLength, maxLength int) error {
	length := utf8.RuneCountInString(value)
	if length < minLength || length > maxLength {
		return fmt.Errorf("length must be between %d and %d", minLength, maxLength)
	}
	return nil
}

func validateJSONObject(value json.RawMessage, maxLength int) error {
	if len(value) == 0 {
		return nil
	}
	if len(value) > maxLength {
		return fmt.Errorf("JSON object is too large")
	}
	trimmed := bytes.TrimSpace(value)
	if len(trimmed) < 2 || trimmed[0] != '{' || trimmed[len(trimmed)-1] != '}' {
		return fmt.Errorf("must be a JSON object")
	}
	if !json.Valid(value) {
		return fmt.Errorf("must be a valid JSON object")
	}
	return nil
}

func validLevel(level string) bool {
	switch level {
	case "INFO", "WARN", "ERROR", "DEBUG":
		return true
	default:
		return false
	}
}

func invalid(index int, field, message string) *validationError {
	return &validationError{
		message: message,
		field:   field,
		index:   index,
	}
}

func writeError(
	w http.ResponseWriter,
	status int,
	code string,
	message string,
	index int,
	field string,
) {
	body := errorBody{
		Code:    code,
		Message: message,
		Field:   field,
	}
	if index >= 0 {
		body.Index = &index
	}
	writeJSON(w, status, errorEnvelope{Error: body})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
