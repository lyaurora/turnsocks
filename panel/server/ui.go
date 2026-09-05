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
	raw, err := fs.ReadFile(a.ui, "ui/dist/index.html")
	if err != nil {
		http.Error(w, "panel UI not built", http.StatusInternalServerError)
		return
	}
	_, _ = w.Write(raw)
}

func (a *app) handleUIAsset(w http.ResponseWriter, r *http.Request) {
	dist, err := fs.Sub(a.ui, "ui/dist")
	if err != nil {
		http.Error(w, "panel UI not built", http.StatusInternalServerError)
		return
	}
	http.FileServer(http.FS(dist)).ServeHTTP(w, r)
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
      background-image: radial-gradient(900px at 50% -300px, hsl(var(--primary) / 0.07), transparent 70%);
      background-attachment: fixed;
      color: hsl(var(--foreground));
      font-family: Inter, "SF Pro Text", system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", "Noto Sans CJK SC", "Noto Sans SC", "PingFang SC", "Microsoft YaHei", sans-serif;
      -webkit-font-smoothing: antialiased;
    }
    .card {
      width: min(380px, 100%);
      border: 1px solid hsl(var(--border));
      border-radius: 14px;
      background: hsl(var(--card));
      box-shadow: 0 1px 2px rgba(0,0,0,.04), 0 8px 30px rgba(0,0,0,.06);
      padding: 26px;
    }
    .dark .card { box-shadow: 0 1px 2px rgba(0,0,0,.4), 0 12px 40px rgba(0,0,0,.5); }
    .brand {
      display: flex;
      align-items: center;
      gap: 11px;
      margin-bottom: 10px;
    }
    .mark {
      display: grid;
      place-items: center;
      width: 32px;
      height: 32px;
      border-radius: 9px;
      background: var(--brand-gradient);
      color: #fff;
      box-shadow: 0 2px 8px hsl(var(--brand-glow) / 0.35);
    }
    .mark svg { width: 17px; height: 17px; }
    h1 {
      margin: 0;
      font-size: 17px;
      line-height: 1;
      font-weight: 600;
      letter-spacing: 0;
    }
    p {
      margin: 0 0 18px;
      color: hsl(var(--muted-foreground));
      font-size: 13px;
    }
    label {
      display: grid;
      gap: 6px;
      margin-top: 13px;
      color: hsl(var(--foreground));
      font-size: 12.5px;
      font-weight: 500;
    }
    input {
      width: 100%;
      min-height: 38px;
      border-radius: 9px;
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
      min-height: 38px;
      margin-top: 20px;
      border: 0;
      border-radius: 9px;
      background: hsl(var(--primary));
      color: hsl(var(--primary-foreground));
      cursor: pointer;
      font-size: 13px;
      font-weight: 500;
      font-family: inherit;
      box-shadow: 0 1px 2px rgba(0,0,0,.12), inset 0 1px 0 rgba(255,255,255,.14);
      transition: background .15s;
    }
    button:hover { background: hsl(var(--primary-hover)); }
    button:focus-visible {
      outline: 2px solid hsl(var(--ring) / 0.8);
      outline-offset: 2px;
    }
    .error {
      margin: 0 0 4px;
      border-radius: 9px;
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
