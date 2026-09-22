package web

import (
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"time"
)

//go:embed templates/*.html
var templateFiles embed.FS

//go:embed assets/*
var assetFiles embed.FS

type Renderer struct {
	pages map[string]*template.Template
}

func New() (*Renderer, error) {
	pages := make(map[string]*template.Template)
	for _, page := range []string{
		"login",
		"logs",
		"detail",
		"settings",
	} {
		parsed, err := template.New("layout").
			Funcs(template.FuncMap{
				"formatTime": formatTime,
				"prettyJSON": prettyJSON,
				"enabled":    enabled,
			}).
			ParseFS(
				templateFiles,
				"templates/base.html",
				fmt.Sprintf("templates/%s.html", page),
			)
		if err != nil {
			return nil, fmt.Errorf("parse %s template: %w", page, err)
		}
		pages[page] = parsed
	}
	return &Renderer{pages: pages}, nil
}

func (r *Renderer) Render(
	w http.ResponseWriter,
	page string,
	status int,
	value any,
) error {
	parsed, ok := r.pages[page]
	if !ok {
		return fmt.Errorf("unknown page template %q", page)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set(
		"Content-Security-Policy",
		"default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; img-src 'self' data:;",
	)
	w.WriteHeader(status)
	return parsed.ExecuteTemplate(w, "layout", value)
}

func Assets() fs.FS {
	assets, _ := fs.Sub(assetFiles, "assets")
	return assets
}

func formatTime(value time.Time) string {
	return value.In(time.Local).Format("2006-01-02 15:04:05.000")
}

func prettyJSON(value json.RawMessage) string {
	if len(value) == 0 {
		return ""
	}
	var formatted any
	if err := json.Unmarshal(value, &formatted); err != nil {
		return string(value)
	}
	encoded, err := json.MarshalIndent(formatted, "", "  ")
	if err != nil {
		return string(value)
	}
	return string(encoded)
}

func enabled(value bool) string {
	if value {
		return "已启用"
	}
	return "已禁用"
}
