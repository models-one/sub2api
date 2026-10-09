//go:build embed

package web

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	htmlpkg "html"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

const (
	// NonceHTMLPlaceholder is the placeholder for nonce in HTML script tags
	NonceHTMLPlaceholder = "__CSP_NONCE_VALUE__"
)

//go:embed all:dist
var frontendFS embed.FS

// PublicSettingsProvider is an interface to fetch public settings
type PublicSettingsProvider interface {
	GetPublicSettingsForInjection(ctx context.Context) (any, error)
}

// FrontendServer serves the embedded frontend with settings injection
type FrontendServer struct {
	distFS     fs.FS
	fileServer http.Handler
	// baseHTML 是 / 路由的 prerender 产物（dist/index.html）。请求 / 走它+缓存。
	baseHTML []byte
	// shellHTML 是 vite 出的原始空壳（dist/_spa-shell.html）。SPA fallback（未知路径，
	// 比如 /dashboard 刷新）用它，避免拿 baseHTML 把 home 闪一下再跳 dashboard。
	// 若 _spa-shell.html 不存在（未跑 prerender），退化为 baseHTML，行为同 SSG 前。
	shellHTML   []byte
	cache       *HTMLCache
	settings    PublicSettingsProvider
	overrideDir string // local file override directory
}

// NewFrontendServer creates a new frontend server with settings injection
func NewFrontendServer(settingsProvider PublicSettingsProvider) (*FrontendServer, error) {
	distFS, err := fs.Sub(frontendFS, "dist")
	if err != nil {
		return nil, err
	}

	// Read base HTML once
	file, err := distFS.Open("index.html")
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()

	baseHTML, err := io.ReadAll(file)
	if err != nil {
		return nil, err
	}

	cache := NewHTMLCache()
	cache.SetBaseHTML(baseHTML)

	// 尝试加载 SPA fallback 空壳（prerender 产物）。读不到时退化为 baseHTML。
	shellHTML := baseHTML
	if shellFile, err := distFS.Open("_spa-shell.html"); err == nil {
		if data, readErr := io.ReadAll(shellFile); readErr == nil && len(data) > 0 {
			shellHTML = data
		}
		_ = shellFile.Close()
	}

	return &FrontendServer{
		distFS:      distFS,
		fileServer:  http.FileServer(http.FS(distFS)),
		baseHTML:    baseHTML,
		shellHTML:   shellHTML,
		cache:       cache,
		settings:    settingsProvider,
		overrideDir: filepath.Join("data", "public"),
	}, nil
}

// InvalidateCache invalidates the HTML cache (call when settings change)
func (s *FrontendServer) InvalidateCache() {
	if s != nil && s.cache != nil {
		s.cache.Invalidate()
	}
}

// Middleware returns the Gin middleware handler
func (s *FrontendServer) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		path := c.Request.URL.Path

		// Skip API routes
		if shouldBypassEmbeddedFrontend(path) {
			c.Next()
			return
		}

		cleanPath := strings.TrimPrefix(path, "/")
		if cleanPath == "" {
			cleanPath = "index.html"
		}

		if cleanPath == "index.html" {
			s.serveIndexHTML(c)
			return
		}

		if cleanPath == "sitemap.xml" {
			s.serveSitemap(c)
			return
		}

		// _spa-shell.html 是后端内部用的 SPA fallback 模板，对外屏蔽
		if cleanPath == "_spa-shell.html" {
			c.Status(http.StatusNotFound)
			c.Abort()
			return
		}

		// 不是具体存在的静态资源 → 可能是 prerender 路由（dist/<path>/index.html）
		// 或 SPA fallback 路由
		if !s.fileExists(cleanPath) {
			if s.tryServePrerendered(c, cleanPath) {
				return
			}
			// SPA fallback：用原始空壳，避免把 home prerender 产物当兜底，导致刷新
			// /dashboard 等路径时先闪一下首页内容再被 Vue 跳到正确视图。
			s.servePrerenderedHTML(c, s.shellHTML)
			return
		}

		// Try local override first
		if s.tryServeOverride(c, cleanPath) {
			return
		}

		// Serve static files normally (hashed assets get long-lived cache headers)
		applyStaticAssetCacheHeaders(c.Writer.Header(), cleanPath)
		s.fileServer.ServeHTTP(c.Writer, c.Request)
		c.Abort()
	}
}

// prerenderedRoutes 列出 SSG 生成的公开页路径，对应 frontend/scripts/prerender.mjs 的 ROUTES。
// 同时作为 sitemap.xml 的内容源。新增 prerender 路由时两边一起改。
var prerenderedRoutes = []string{
	"/",
	"/models",
	"/docs/quickstart",
	"/docs/api-guide",
}

// requestScheme 推断当前请求的 scheme：TLS 优先；其次 X-Forwarded-Proto；兜底 http。
func requestScheme(c *gin.Context) string {
	if c.Request.TLS != nil {
		return "https"
	}
	if proto := c.GetHeader("X-Forwarded-Proto"); proto != "" {
		return proto
	}
	return "http"
}

// serveSitemap 按当前请求 Host 动态拼出绝对 URL 的 sitemap.xml。
// 多租户场景下每个商户域名访问会拿到对应域名的 sitemap。
func (s *FrontendServer) serveSitemap(c *gin.Context) {
	scheme := requestScheme(c)
	host := c.Request.Host
	base := scheme + "://" + host

	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">` + "\n")
	for _, p := range prerenderedRoutes {
		b.WriteString("  <url><loc>")
		b.WriteString(base)
		b.WriteString(p)
		b.WriteString("</loc></url>\n")
	}
	b.WriteString(`</urlset>` + "\n")

	c.Header("Cache-Control", "no-cache")
	c.Data(http.StatusOK, "application/xml; charset=utf-8", b.Bytes())
	c.Abort()
}

// tryServePrerendered: 检查 dist/<cleanPath>/index.html 是否存在（prerender 产物），
// 若存在则以该文件为 baseHTML 走 __APP_CONFIG__ 注入逻辑。
// prerender 产物每条路由内容不同，不能共用 index.html 的 baseHTML/缓存，
// 因此每次读盘 + 注入（小文件，开销可忽略）。
func (s *FrontendServer) tryServePrerendered(c *gin.Context, cleanPath string) bool {
	if cleanPath == "" || cleanPath == "index.html" {
		return false
	}
	candidate := strings.TrimSuffix(cleanPath, "/") + "/index.html"
	file, err := s.distFS.Open(candidate)
	if err != nil {
		return false
	}
	baseHTML, err := io.ReadAll(file)
	_ = file.Close()
	if err != nil {
		return false
	}
	s.servePrerenderedHTML(c, baseHTML)
	return true
}

// servePrerenderedHTML: 把 prerender 产物当 baseHTML 注入 __APP_CONFIG__ 后返回。
// 流程与 serveIndexHTML 一致但不走单例缓存，因为每个 path 有自己的产物。
func (s *FrontendServer) servePrerenderedHTML(c *gin.Context, baseHTML []byte) {
	nonce := middleware.GetNonceFromContext(c)

	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()

	settings, err := s.settings.GetPublicSettingsForInjection(ctx)
	if err != nil {
		content := replaceNoncePlaceholder(baseHTML, nonce)
		c.Header("Cache-Control", "no-cache")
		c.Data(http.StatusOK, "text/html; charset=utf-8", content)
		c.Abort()
		return
	}

	settingsJSON, err := json.Marshal(settings)
	if err != nil {
		content := replaceNoncePlaceholder(baseHTML, nonce)
		c.Header("Cache-Control", "no-cache")
		c.Data(http.StatusOK, "text/html; charset=utf-8", content)
		c.Abort()
		return
	}

	script := []byte(`<script nonce="` + NonceHTMLPlaceholder + `">window.__APP_CONFIG__=` + string(settingsJSON) + `;</script>`)
	headClose := []byte("</head>")
	rendered := bytes.Replace(baseHTML, headClose, append(script, headClose...), 1)

	content := replaceNoncePlaceholder(rendered, nonce)
	c.Header("Cache-Control", "no-cache")
	c.Data(http.StatusOK, "text/html; charset=utf-8", content)
	c.Abort()
}

// fileExists 仅在 path 指向一个**文件**时返回 true。
// 排除目录是为了让 prerender 路由（如 /models 对应 dist/models/ 目录）
// 落入下面的 tryServePrerendered 分支，而不是被 http.FileServer 接管 ——
// 后者对"访问目录不带斜杠"的默认行为是 301 加斜杠并返回相对 Location，
// 既多一跳又对部分爬虫不友好。
func (s *FrontendServer) fileExists(path string) bool {
	file, err := s.distFS.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return false
	}
	return !info.IsDir()
}

// tryServeOverride checks if a local override file exists and serves it.
// Files in overrideDir take precedence over embedded files.
func (s *FrontendServer) tryServeOverride(c *gin.Context, cleanPath string) bool {
	if s.overrideDir == "" {
		return false
	}
	filePath := filepath.Join(s.overrideDir, filepath.Clean("/"+cleanPath))
	info, err := os.Stat(filePath)
	if err != nil || info.IsDir() {
		return false
	}
	c.File(filePath)
	c.Abort()
	return true
}

func (s *FrontendServer) serveIndexHTML(c *gin.Context) {
	// Get nonce from context (generated by SecurityHeaders middleware)
	nonce := middleware.GetNonceFromContext(c)

	// MERCHANT-SYSTEM v1.0：商户分站请求绕过单例缓存，每个请求按 ctx 中的商户
	// 重新渲染，避免主站缓存把主站品牌注入到分站 HTML（或反过来）。
	// 商户域名总数有限、流量小，不缓存的开销可接受。
	isMerchantSite := middleware.MerchantFromContext(c) != nil

	if !isMerchantSite {
		// Check cache first (main-site only)
		cached := s.cache.Get()
		if cached != nil {
			// Check If-None-Match for 304 response
			if match := c.GetHeader("If-None-Match"); match == cached.ETag {
				c.Status(http.StatusNotModified)
				c.Abort()
				return
			}

			// Replace nonce placeholder with actual nonce before serving
			content := replaceNoncePlaceholder(cached.Content, nonce)

			c.Header("ETag", cached.ETag)
			c.Header("Cache-Control", "no-cache") // Must revalidate
			c.Data(http.StatusOK, "text/html; charset=utf-8", content)
			c.Abort()
			return
		}
	}

	// Cache miss (or merchant-site bypass) - fetch settings and render
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()

	settings, err := s.settings.GetPublicSettingsForInjection(ctx)
	if err != nil {
		// Fallback: serve without injection
		c.Data(http.StatusOK, "text/html; charset=utf-8", s.baseHTML)
		c.Abort()
		return
	}

	settingsJSON, err := json.Marshal(settings)
	if err != nil {
		// Fallback: serve without injection
		c.Data(http.StatusOK, "text/html; charset=utf-8", s.baseHTML)
		c.Abort()
		return
	}

	rendered := s.injectSettings(settingsJSON)
	if !isMerchantSite {
		// 仅主站结果写缓存；商户分站每请求一次渲染一次
		s.cache.Set(rendered, settingsJSON)
	}

	// Replace nonce placeholder with actual nonce before serving
	content := replaceNoncePlaceholder(rendered, nonce)

	if !isMerchantSite {
		if cached := s.cache.Get(); cached != nil {
			c.Header("ETag", cached.ETag)
		}
	}
	c.Header("Cache-Control", "no-cache")
	c.Data(http.StatusOK, "text/html; charset=utf-8", content)
	c.Abort()
}

func (s *FrontendServer) injectSettings(settingsJSON []byte) []byte {
	// Create the script tag to inject with nonce placeholder
	// The placeholder will be replaced with actual nonce at request time
	script := []byte(`<script nonce="` + NonceHTMLPlaceholder + `">window.__APP_CONFIG__=` + string(settingsJSON) + `;</script>`)

	// Inject before </head>
	headClose := []byte("</head>")
	result := bytes.Replace(s.baseHTML, headClose, append(script, headClose...), 1)

	// Apply custom branding before the browser paints the static defaults.
	result = injectSiteTitle(result, settingsJSON)
	result = injectSiteFavicon(result, settingsJSON)

	return result
}

// injectSiteFavicon replaces the static favicon with a configured, browser-safe image URL.
func injectSiteFavicon(html, settingsJSON []byte) []byte {
	var cfg struct {
		SiteLogo string `json:"site_logo"`
	}
	if err := json.Unmarshal(settingsJSON, &cfg); err != nil {
		return html
	}

	logoURL := safeImageURL(cfg.SiteLogo)
	if logoURL == "" {
		return html
	}

	linkStart := bytes.Index(html, []byte(`<link rel="icon"`))
	if linkStart == -1 {
		return html
	}
	linkEndOffset := bytes.IndexByte(html[linkStart:], '>')
	if linkEndOffset == -1 {
		return html
	}
	linkEnd := linkStart + linkEndOffset + 1
	replacement := []byte(`<link rel="icon" href="` + htmlpkg.EscapeString(logoURL) + `" />`)

	var buf bytes.Buffer
	buf.Write(html[:linkStart])
	buf.Write(replacement)
	buf.Write(html[linkEnd:])
	return buf.Bytes()
}

func safeImageURL(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	if strings.HasPrefix(trimmed, "/") && !strings.HasPrefix(trimmed, "//") {
		return trimmed
	}
	if strings.HasPrefix(strings.ToLower(trimmed), "data:image/") {
		return trimmed
	}

	parsed, err := url.Parse(trimmed)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return ""
	}
	return trimmed
}

// injectSiteTitle replaces the static <title> in HTML with the configured site name.
// This ensures the browser tab shows the correct title before JS executes.
func injectSiteTitle(html, settingsJSON []byte) []byte {
	var cfg struct {
		SiteName string `json:"site_name"`
	}
	if err := json.Unmarshal(settingsJSON, &cfg); err != nil || cfg.SiteName == "" {
		return html
	}

	// Find and replace the existing <title>...</title>
	titleStart := bytes.Index(html, []byte("<title>"))
	titleEnd := bytes.Index(html, []byte("</title>"))
	if titleStart == -1 || titleEnd == -1 || titleEnd <= titleStart {
		return html
	}

	newTitle := []byte("<title>" + htmlpkg.EscapeString(cfg.SiteName) + " - AI API Gateway</title>")
	var buf bytes.Buffer
	buf.Write(html[:titleStart])
	buf.Write(newTitle)
	buf.Write(html[titleEnd+len("</title>"):])
	return buf.Bytes()
}

// replaceNoncePlaceholder replaces the nonce placeholder with actual nonce value
func replaceNoncePlaceholder(html []byte, nonce string) []byte {
	return bytes.ReplaceAll(html, []byte(NonceHTMLPlaceholder), []byte(nonce))
}

// ServeEmbeddedFrontend returns a middleware for serving embedded frontend
// This is the legacy function for backward compatibility when no settings provider is available
func ServeEmbeddedFrontend() gin.HandlerFunc {
	distFS, err := fs.Sub(frontendFS, "dist")
	if err != nil {
		panic("failed to get dist subdirectory: " + err.Error())
	}
	fileServer := http.FileServer(http.FS(distFS))
	overrideDir := filepath.Join("data", "public")

	return func(c *gin.Context) {
		path := c.Request.URL.Path

		if shouldBypassEmbeddedFrontend(path) {
			c.Next()
			return
		}

		cleanPath := strings.TrimPrefix(path, "/")
		if cleanPath == "" {
			cleanPath = "index.html"
		}

		if file, err := distFS.Open(cleanPath); err == nil {
			_ = file.Close()
			// Try local override first
			if tryServeOverrideFile(c, overrideDir, cleanPath) {
				return
			}
			applyStaticAssetCacheHeaders(c.Writer.Header(), cleanPath)
			fileServer.ServeHTTP(c.Writer, c.Request)
			c.Abort()
			return
		}

		serveIndexHTML(c, distFS)
	}
}

// tryServeOverrideFile is a standalone version of tryServeOverride for legacy usage.
func tryServeOverrideFile(c *gin.Context, overrideDir, cleanPath string) bool {
	if overrideDir == "" {
		return false
	}
	filePath := filepath.Join(overrideDir, filepath.Clean("/"+cleanPath))
	info, err := os.Stat(filePath)
	if err != nil || info.IsDir() {
		return false
	}
	c.File(filePath)
	c.Abort()
	return true
}

func shouldBypassEmbeddedFrontend(path string) bool {
	trimmed := strings.TrimSpace(path)
	return strings.HasPrefix(trimmed, "/api/") ||
		strings.HasPrefix(trimmed, "/v1/") ||
		strings.HasPrefix(trimmed, "/v1beta/") ||
		strings.HasPrefix(trimmed, "/backend-api/") ||
		strings.HasPrefix(trimmed, "/antigravity/") ||
		strings.HasPrefix(trimmed, "/setup/") ||
		strings.HasPrefix(trimmed, "/internal/") ||
		strings.HasPrefix(trimmed, "/merchant-assets/") ||
		trimmed == "/health" ||
		trimmed == "/models" ||
		strings.HasPrefix(trimmed, "/models/") ||
		trimmed == "/responses" ||
		strings.HasPrefix(trimmed, "/responses/") ||
		trimmed == "/chat/completions" ||
		trimmed == "/embeddings" ||
		trimmed == "/messages/count_tokens" ||
		trimmed == "/alpha/search" ||
		trimmed == "/web_search" ||
		trimmed == "/x_search" ||
		trimmed == "/tts" ||
		trimmed == "/stt" ||
		trimmed == "/realtime" ||
		trimmed == "/custom-voices" ||
		strings.HasPrefix(trimmed, "/custom-voices/") ||
		trimmed == "/contents/generations/tasks" ||
		strings.HasPrefix(trimmed, "/contents/generations/tasks/") ||
		trimmed == "/v3/contents/generations/tasks" ||
		strings.HasPrefix(trimmed, "/v3/contents/generations/tasks/") ||
		strings.HasPrefix(trimmed, "/images/") ||
		trimmed == "/videos" ||
		strings.HasPrefix(trimmed, "/videos/")
}

func serveIndexHTML(c *gin.Context, fsys fs.FS) {
	file, err := fsys.Open("index.html")
	if err != nil {
		c.String(http.StatusNotFound, "Frontend not found")
		c.Abort()
		return
	}
	defer func() { _ = file.Close() }()

	content, err := io.ReadAll(file)
	if err != nil {
		c.String(http.StatusInternalServerError, "Failed to read index.html")
		c.Abort()
		return
	}

	c.Data(http.StatusOK, "text/html; charset=utf-8", content)
	c.Abort()
}

func HasEmbeddedFrontend() bool {
	_, err := frontendFS.ReadFile("dist/index.html")
	return err == nil
}
