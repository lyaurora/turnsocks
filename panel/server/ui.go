package server

import (
	_ "embed"
	"html"
	"io/fs"
	"net/http"
	"strings"
)

func (a *app) handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	raw, err := fs.ReadFile(a.ui, "ui/dist/index.html")
	if err != nil {
		http.Error(w, "panel UI not built", http.StatusInternalServerError)
		return
	}
	_, _ = w.Write(raw)
}

func (a *app) assetHandler() http.Handler {
	if a.ui == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "panel UI not built", http.StatusInternalServerError)
		})
	}
	dist, err := fs.Sub(a.ui, "ui/dist")
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "panel UI not built", http.StatusInternalServerError)
		})
	}
	files := http.FileServer(http.FS(dist))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		files.ServeHTTP(w, r)
	})
}

func writeLoginPage(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	errorHTML := ""
	if message != "" {
		errorHTML = `<div class="error">` + html.EscapeString(message) + `</div>`
	}
	_, _ = w.Write([]byte(strings.Replace(loginHTML, "{{ERROR}}", errorHTML, 1)))
}

//go:embed theme.css
var themeCSS string

var loginHTML = `<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>登录 turnsocks</title>
  <script>
    (() => {
      const theme = localStorage.getItem("turnsocks-theme") || "system";
      const media = matchMedia("(prefers-color-scheme: dark)");
      const apply = () => document.documentElement.classList.toggle("dark", theme === "dark" || (theme === "system" && media.matches));
      apply();
      if (theme === "system") media.addEventListener?.("change", apply);
    })();
  </script>
  <style>
` + themeCSS + `
    * { box-sizing: border-box; }
    body {
      min-height: 100vh;
      margin: 0;
      display: grid;
      place-items: center;
      padding: 24px;
      background-color: hsl(var(--background));
      background-image: radial-gradient(880px at 50% -260px, hsl(var(--anchor-accent) / 0.11), transparent 70%);
      background-attachment: fixed;
      color: hsl(var(--foreground));
      font-family: Inter, "SF Pro Text", system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", "Noto Sans CJK SC", "Noto Sans SC", "PingFang SC", "Microsoft YaHei", sans-serif;
      -webkit-font-smoothing: antialiased;
    }
    .card {
      width: min(390px, 100%);
      border: 1px solid hsl(var(--border));
      border-radius: 20px;
      background: hsl(var(--card));
      box-shadow: 0 2px 10px rgba(17, 38, 34, .05), 0 12px 34px rgba(17, 38, 34, .06);
      padding: 28px;
    }
    .dark .card {
      box-shadow: inset 0 1px 0 rgba(255, 255, 255, .05), 0 16px 48px rgba(0, 0, 0, .55);
    }
    .brand {
      display: flex;
      align-items: center;
      gap: 11px;
      margin-bottom: 10px;
    }
    .mark {
      display: grid;
      place-items: center;
      width: 34px;
      height: 34px;
      border-radius: 10px;
      background: var(--brand-gradient);
      color: hsl(var(--anchor-accent));
      box-shadow: 0 3px 10px hsl(var(--brand-glow) / 0.25), inset 0 1px 0 rgba(255, 255, 255, .14);
    }
    .mark svg { width: 17px; height: 17px; }
    h1 {
      margin: 0;
      font-size: 17px;
      line-height: 1;
      font-weight: 700;
      letter-spacing: -0.01em;
    }
    p {
      margin: 0 0 18px;
      color: hsl(var(--muted-foreground));
      font-size: 13px;
    }
    label {
      display: grid;
      gap: 6px;
      margin-top: 14px;
      color: hsl(var(--foreground));
      font-size: 12.5px;
      font-weight: 500;
    }
    input {
      width: 100%;
      min-height: 40px;
      border-radius: 10px;
      border: 1px solid hsl(var(--input));
      background: hsl(var(--card));
      color: hsl(var(--foreground));
      outline: none;
      padding: 0 12px;
      font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", "Courier New", monospace;
      font-size: 13px;
      transition: border-color .15s, box-shadow .15s;
    }
    input:focus {
      border-color: hsl(var(--primary));
      box-shadow: 0 0 0 3px hsl(var(--primary) / .14);
    }
    button {
      width: 100%;
      min-height: 40px;
      margin-top: 22px;
      border: 0;
      border-radius: 10px;
      background: hsl(var(--anchor));
      color: #fff;
      cursor: pointer;
      font-size: 13px;
      font-weight: 600;
      font-family: inherit;
      box-shadow: 0 2px 6px rgba(17, 38, 34, .18), inset 0 1px 0 rgba(255, 255, 255, .14);
      transition: background .15s;
    }
    button:hover { background: hsl(var(--anchor-hover)); }
    .dark button { background: hsl(var(--primary)); }
    .dark button:hover { background: hsl(var(--primary-hover)); }
    button:focus-visible {
      outline: 2px solid hsl(var(--ring) / 0.8);
      outline-offset: 2px;
    }
    .error {
      margin: 0 0 4px;
      border-radius: 10px;
      background: hsl(var(--danger) / .08);
      color: hsl(var(--danger));
      padding: 10px 12px;
      font-size: 13px;
      font-weight: 500;
    }
  </style>
</head>
<body>
  <form class="card" method="post" action="/login">
    <div class="brand">
      <span class="mark">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="m16 3 4 4-4 4"/><path d="M20 7H4"/><path d="m8 21-4-4 4-4"/><path d="M4 17h16"/></svg>
      </span>
      <h1>turnsocks</h1>
    </div>
    <p>登录以管理 TURN 节点和代理配置。</p>
    {{ERROR}}
    <label>用户名
      <input name="username" autocomplete="username" autofocus>
    </label>
    <label>密码
      <input name="password" type="password" autocomplete="current-password">
    </label>
    <button type="submit">登录</button>
  </form>
</body>
</html>`
