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
				"formatTime":   formatTime,
				"prettyJSON":   prettyJSON,
				"enabled":      enabled,
				"channelLabel": ChannelLabel,
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

// channelLabels 是日志来源端（channel）的展示名。
//
// 取值集合由写入方（PHP 端 app\common\library\Log::CHANNEL_*）固定，
// 这里只做展示映射，不做校验，遇到未知值原样返回。
var channelLabels = map[string]string{
	"admin":            "后台",
	"user":             "用户端",
	"cli":              "命令行",
	"agent":            "代理商",
	"system_api":       "系统接口",
	"payment_callback": "支付回调",
	"system":           "共享层",
}

func ChannelLabel(value string) string {
	if label, ok := channelLabels[value]; ok {
		return label
	}
	return value
}
