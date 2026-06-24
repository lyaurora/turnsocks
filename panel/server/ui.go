package server

import (
	"html"
	"io/fs"
	"net/http"
	"strings"
)

func (a *app) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
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

const loginHTML = `<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>登录 turnsocks</title>
  <style>
    :root {
      color-scheme: light;
      --background: 42 20% 95%;
      --foreground: 195 16% 16%;
      --card: 40 29% 98%;
      --muted: 42 17% 90%;
      --muted-foreground: 200 11% 38%;
      --primary: 184 27% 25%;
      --primary-foreground: 42 20% 95%;
      --border: 36 15% 78%;
      --input: 36 15% 82%;
      --ring: 184 27% 25%;
      --danger: 5 62% 48%;
    }
    * { box-sizing: border-box; }
    body {
      min-height: 100vh;
      margin: 0;
      display: grid;
      place-items: center;
      padding: 24px;
      background:
        radial-gradient(circle at top left, rgba(24, 92, 87, .08), transparent 32%),
        linear-gradient(rgba(67, 73, 61, .06) 1px, transparent 1px),
        linear-gradient(90deg, rgba(67, 73, 61, .06) 1px, transparent 1px),
        hsl(var(--background));
      background-size: auto, 26px 26px, 26px 26px;
      color: hsl(var(--foreground));
      font-family: system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", "Noto Sans CJK SC", "Noto Sans SC", "PingFang SC", "Microsoft YaHei", sans-serif;
    }
    .card {
      width: min(380px, 100%);
      border: 1px solid hsl(var(--border));
      border-radius: 18px;
      background: hsl(var(--card) / .94);
      box-shadow: 0 24px 60px rgba(57,63,51,.10);
      padding: 24px;
    }
    h1 {
      margin: 0 0 8px;
      font-size: 32px;
      line-height: 1;
      font-weight: 700;
      letter-spacing: 0;
    }
    p {
      margin: 0 0 20px;
      color: hsl(var(--muted-foreground));
      font-size: 14px;
    }
    label {
      display: grid;
      gap: 8px;
      margin-top: 14px;
      color: hsl(var(--muted-foreground));
      font-size: 12px;
      font-family: ui-monospace, "SFMono-Regular", "Cascadia Mono", "Noto Sans Mono CJK SC", Consolas, Menlo, monospace;
      text-transform: uppercase;
      letter-spacing: .12em;
    }
    input {
      width: 100%;
      min-height: 44px;
      border-radius: 10px;
      border: 1px solid hsl(var(--input));
      background: hsl(var(--card));
      color: hsl(var(--foreground));
      outline: none;
      padding: 0 13px;
      font: 14px ui-monospace, "SFMono-Regular", "Cascadia Mono", "Noto Sans Mono CJK SC", Consolas, Menlo, monospace;
    }
    input:focus {
      border-color: hsl(var(--ring));
      box-shadow: 0 0 0 3px hsl(var(--ring) / .16);
    }
    button {
      width: 100%;
      min-height: 42px;
      margin-top: 18px;
      border: 1px solid hsl(var(--primary));
      border-radius: 999px;
      background: hsl(var(--primary));
      color: hsl(var(--primary-foreground));
      cursor: pointer;
      font: 600 14px system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
    }
    .error {
      margin: 0 0 14px;
      border: 1px solid hsl(var(--danger) / .30);
      border-radius: 10px;
      background: hsl(var(--danger) / .08);
      color: hsl(var(--danger));
      padding: 10px 12px;
      font-size: 13px;
    }
  </style>
</head>
<body>
  <form class="card" method="post" action="/login">
    <h1>turnsocks</h1>
    <p>登录后继续管理 TURN 节点和本地代理配置。</p>
    {{ERROR}}
    <label>用户
      <input name="username" autocomplete="username" autofocus>
    </label>
    <label>密码
      <input name="password" type="password" autocomplete="current-password">
    </label>
    <button type="submit">登录</button>
  </form>
</body>
</html>`
