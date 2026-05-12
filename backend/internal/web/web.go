package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist
var distFS embed.FS

// FS returns the embedded frontend assets rooted at "dist".
// When the frontend hasn't been built yet, the returned FS is empty and Handler
// will fall through to a small placeholder page.
func FS() fs.FS {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		return nil
	}
	return sub
}

// Handler serves the embedded SPA. Unknown paths fall back to index.html.
func Handler() http.Handler {
	sub := FS()
	if sub == nil {
		return placeholderHandler()
	}
	fileServer := http.FileServer(http.FS(sub))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		if _, err := fs.Stat(sub, path); err != nil {
			// SPA fallback: serve index.html for any non-asset path.
			r2 := r.Clone(r.Context())
			r2.URL.Path = "/"
			fileServer.ServeHTTP(w, r2)
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}

func placeholderHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(placeholderHTML))
	})
}

const placeholderHTML = `<!doctype html>
<html lang="zh-CN">
<head><meta charset="utf-8"><title>OpenTheOne</title>
<style>body{font-family:system-ui,sans-serif;max-width:640px;margin:80px auto;padding:0 24px;color:#222}
code{background:#f3f3f3;padding:2px 6px;border-radius:4px}</style></head>
<body>
<h1>OpenTheOne</h1>
<p>后端已启动，但前端尚未构建。请执行：</p>
<pre><code>cd frontend
pnpm install
pnpm build</code></pre>
<p>然后重新编译 Go 后端，即可在此处看到完整的 Web 控制台。</p>
<p>API 文档参见 <code>doc.md</code>，所有接口均为 <code>POST /api/*</code>。</p>
</body></html>`
