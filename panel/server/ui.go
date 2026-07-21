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
    :root {
      color-scheme: light;
      --background: 240 7% 97%;
      --foreground: 240 6% 10%;
      --card: 0 0% 100%;
      --muted-foreground: 240 4% 46%;
      --primary: 239 82% 62%;
      --primary-hover: 243 75% 55%;
      --primary-foreground: 0 0% 100%;
      --border: 240 6% 90%;
      --input: 240 5% 84%;
      --danger: 0 72% 51%;
    }
    .dark {
      color-scheme: dark;
      --background: 240 9% 4%;
      --foreground: 240 5% 96%;
      --card: 240 7% 8%;
      --muted-foreground: 240 5% 65%;
      --primary: 234 89% 74%;
      --primary-hover: 231 92% 80%;
      --primary-foreground: 240 9% 8%;
      --border: 240 6% 16%;
      --input: 240 6% 23%;
      --danger: 0 91% 71%;
    }
    * { box-sizing: border-box; }
    body {
      min-height: 100vh;
      margin: 0;
      display: grid;
      place-items: center;
      padding: 24px;
      background-color: hsl(var(--background));
      background-image: radial-gradient(700px at 50% -180px, hsl(var(--primary) / .08), transparent 70%);
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
      background: linear-gradient(135deg, #6366f1, #8b5cf6);
      color: #fff;
      box-shadow: 0 2px 8px rgba(99,102,241,.35);
    }
    .mark svg { width: 17px; height: 17px; }
    h1 {
      margin: 0;
      font-size: 18px;
      line-height: 1;
      font-weight: 650;
      letter-spacing: 0;
    }
    p {
      margin: 0 0 18px;
      color: hsl(var(--muted-foreground));
      font-size: 13.5px;
    }
    label {
      display: grid;
      gap: 6px;
      margin-top: 13px;
      color: hsl(var(--foreground));
      font-size: 12.5px;
      font-weight: 550;
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
      font-size: 14px;
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
      font-size: 14px;
      font-weight: 600;
      font-family: inherit;
      box-shadow: 0 1px 2px rgba(0,0,0,.12), inset 0 1px 0 rgba(255,255,255,.14);
      transition: background .15s;
    }
    button:hover { background: hsl(var(--primary-hover)); }
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
