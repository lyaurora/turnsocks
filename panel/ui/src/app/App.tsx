import { FormEvent, useCallback, useEffect, useRef, useState } from "react";
import { flushSync } from "react-dom";
import { addServer as addServerRequest, deleteServer, getState, restartProxy, selectServer, testServer as testServerRequest, updateConfig as updateConfigRequest } from "../api/client";
import { Chip, IconDot } from "../components/Chip";
import { topButtonClass } from "../controlClasses";
import { NodePanel } from "../features/nodes/NodePanel";
import { SettingsPanel } from "../features/settings/SettingsPanel";
import { errorMessage } from "../lib/format";
import type { ApiResponse, ConfigForm, PanelState, ThemeMode } from "../types/panel";

const emptyState: PanelState = {
  listen: "",
  doh: "",
  panelUsername: "",
  panelAuthEnabled: false,
  servers: [],
  service: { active: false }
};

const emptyConfig: ConfigForm = {
  listen: "",
  doh: "",
  panelAuthEnabled: false,
  panelUsername: "",
  panelPassword: ""
};

function applyTheme(theme: ThemeMode) {
  const dark = theme === "dark" || (theme === "system" && window.matchMedia?.("(prefers-color-scheme: dark)").matches);
  document.documentElement.classList.toggle("dark", dark);
  localStorage.setItem("turnsocks-theme", theme);
}

function App() {
  const [state, setState] = useState<PanelState>(emptyState);
  const [serverInput, setServerInput] = useState("");
  const [config, setConfig] = useState<ConfigForm>(emptyConfig);
  const [theme, setTheme] = useState<ThemeMode>(() => (localStorage.getItem("turnsocks-theme") as ThemeMode) || "system");
  const [testing, setTesting] = useState<Set<string>>(() => new Set());
  const [busy, setBusy] = useState(false);
  const [toast, setToast] = useState("");
  const settingsDirty = useRef(false);
  const busyRef = useRef(false);
  const testingRef = useRef<Set<string>>(new Set());
  const refreshVersion = useRef(0);
  const toastTimer = useRef<number>();

  const showToast = useCallback((message: string) => {
    setToast(message);
    window.clearTimeout(toastTimer.current);
    toastTimer.current = window.setTimeout(() => setToast(""), 2200);
  }, []);

  useEffect(() => () => window.clearTimeout(toastTimer.current), []);

  const refresh = useCallback(async () => {
    const version = ++refreshVersion.current;
    const next = await getState();
    if (version !== refreshVersion.current) return next;
    setState(next);
    if (!settingsDirty.current) {
      setConfig({
        listen: next.listen || "",
        doh: next.doh || "",
        panelAuthEnabled: !!next.panelAuthEnabled,
        panelUsername: next.panelUsername || "",
        panelPassword: ""
      });
    }
    return next;
  }, []);

  useEffect(() => {
    refresh().catch((err) => showToast(errorMessage(err)));
  }, [refresh, showToast]);

  useEffect(() => {
    const timer = window.setInterval(() => {
      if (!busyRef.current && testingRef.current.size === 0) {
        refresh().catch(() => {});
      }
    }, 5000);
    return () => window.clearInterval(timer);
  }, [refresh]);

  useEffect(() => {
    const syncSystemTheme = () => applyTheme(theme);
    syncSystemTheme();
    const media = window.matchMedia?.("(prefers-color-scheme: dark)");
    media?.addEventListener("change", syncSystemTheme);
    return () => media?.removeEventListener("change", syncSystemTheme);
  }, [theme]);

  function changeTheme(mode: ThemeMode, button: HTMLButtonElement) {
    if (mode === theme) return;
    const rect = button.getBoundingClientRect();
    document.documentElement.style.setProperty("--theme-x", `${rect.left + rect.width / 2}px`);
    document.documentElement.style.setProperty("--theme-y", `${rect.top + rect.height / 2}px`);
    const update = () => {
      flushSync(() => setTheme(mode));
      applyTheme(mode);
    };
    const transitionDocument = document as Document & { startViewTransition?: (callback: () => void) => void };
    if (transitionDocument.startViewTransition && !window.matchMedia("(prefers-reduced-motion: reduce)").matches) {
      transitionDocument.startViewTransition(update);
    } else {
      update();
    }
  }

  async function run(action: () => Promise<ApiResponse>, refreshAfter = true) {
    if (busyRef.current) return false;
    busyRef.current = true;
    refreshVersion.current++;
    setBusy(true);
    try {
      const res = await action();
      showToast(res.message || "操作完成");
      if (refreshAfter) await refresh();
      return true;
    } catch (err) {
      showToast(errorMessage(err));
      return false;
    } finally {
      busyRef.current = false;
      setBusy(false);
    }
  }

  async function addServer(event: FormEvent) {
    event.preventDefault();
    const server = serverInput.trim();
    if (!server) {
      showToast("节点不能为空");
      return;
    }
    await run(async () => {
      const res = await addServerRequest(server);
      setServerInput("");
      return res;
    });
  }

  async function testServer(server: string) {
    if (!server || testingRef.current.has(server)) return;
    refreshVersion.current++;
    const active = new Set(testingRef.current);
    active.add(server);
    testingRef.current = active;
    setTesting(active);
    try {
      const result = await testServerRequest(server);
      setState((prev) => ({
        ...prev,
        servers: prev.servers.map((item) => item.raw === server ? { ...item, test: result } : item)
      }));
      showToast(result.message || "测试完成");
    } catch (err) {
      setState((prev) => ({
        ...prev,
        servers: prev.servers.map((item) => item.raw === server ? { ...item, test: { ok: false, message: errorMessage(err), testedAt: new Date().toISOString() } } : item)
      }));
      showToast(errorMessage(err));
    } finally {
      const next = new Set(testingRef.current);
      next.delete(server);
      testingRef.current = next;
      setTesting(next);
    }
  }

  async function testAllServers() {
    if (testingRef.current.size > 0) return;
    for (const server of state.servers) await testServer(server.raw);
  }

  async function updateConfig(event: FormEvent) {
    event.preventDefault();
    const payload: ConfigForm = {
      listen: config.listen.trim(),
      doh: config.doh.trim(),
      panelAuthEnabled: config.panelAuthEnabled,
      panelUsername: config.panelUsername.trim(),
      panelPassword: config.panelPassword.trim()
    };
    const loginChanged = payload.panelAuthEnabled && (
      !state.panelAuthEnabled ||
      payload.panelUsername !== state.panelUsername ||
      payload.panelPassword !== ""
    );
    const ok = await run(async () => {
      const res = await updateConfigRequest(payload);
      setConfig((prev) => ({ ...prev, panelPassword: "" }));
      settingsDirty.current = false;
      return res;
    }, !loginChanged);
    if (ok && loginChanged) window.location.assign("/login");
  }

  function updateConfigField<K extends keyof ConfigForm>(key: K, value: ConfigForm[K]) {
    settingsDirty.current = true;
    setConfig((prev) => ({ ...prev, [key]: value }));
  }

  function removeServer(server: string) {
    if (window.confirm("确定要删除此节点吗？")) {
      void run(() => deleteServer(server));
    }
  }

  const locked = busy || testing.size > 0;

  return (
    <div className="min-h-screen p-4 pb-12 md:p-6">
      <div className="relative z-10 mx-auto max-w-[1180px]">
        <header className="mb-6 flex flex-col justify-between gap-4 md:mb-8 md:flex-row md:items-center">
          <h1 className="text-[32px] font-bold leading-none tracking-tight text-[hsl(var(--foreground))] md:text-[42px]">turnsocks</h1>
          <div className="flex flex-wrap items-center gap-2">
            <span className={`inline-flex min-h-[30px] items-center justify-center gap-1.5 rounded-full border px-[11px] pt-[2px] font-mono text-[11px] uppercase leading-none tracking-[0.12em] ${state.service.active ? "border-[hsl(var(--warn))]/30 bg-[hsl(var(--warn))]/10 text-[hsl(var(--warn))]" : "border-[hsl(var(--danger))]/30 bg-[hsl(var(--danger))]/10 text-[hsl(var(--danger))]"}`}>
              <IconDot />
              {state.service.active ? "服务运行中" : "服务已停止"}
            </span>
            <button className={topButtonClass} disabled={busy} onClick={() => run(restartProxy)} type="button">重启代理</button>
            <form action="/logout" method="post">
              <button className={topButtonClass} type="submit">退出登录</button>
            </form>
            <div className="flex rounded-full border border-[hsl(var(--border))] bg-[hsl(var(--muted))]/80 p-1 shadow-[inset_0_1px_0_rgba(255,255,255,0.35)] dark:shadow-none">
              {(["light", "system", "dark"] as ThemeMode[]).map((mode) => (
                <button key={mode} onClick={(event) => changeTheme(mode, event.currentTarget)} className={`inline-flex min-h-[24px] cursor-pointer items-center justify-center rounded-full px-[9px] pt-[2px] font-mono text-[11px] leading-none transition-all ${theme === mode ? "bg-[hsl(var(--card))] text-[hsl(var(--foreground))] shadow-[0_1px_3px_rgba(0,0,0,0.08)]" : "text-[hsl(var(--muted-foreground))] hover:text-[hsl(var(--foreground))]"}`} type="button">
                  {mode === "light" ? "浅色" : mode === "system" ? "系统" : "深色"}
                </button>
              ))}
            </div>
          </div>
        </header>

        <div className="grid grid-cols-1 items-start gap-5 md:grid-cols-[minmax(0,1fr)_340px] lg:grid-cols-[minmax(0,1fr)_420px]">
          <NodePanel
            state={state}
            serverInput={serverInput}
            testing={testing}
            busy={busy}
            locked={locked}
            onServerInput={setServerInput}
            onAddServer={addServer}
            onTestServer={testServer}
            onTestAll={testAllServers}
            onSelectServer={(server) => void run(() => selectServer(server))}
            onDeleteServer={removeServer}
          />
          <SettingsPanel state={state} config={config} busy={busy} onSubmit={updateConfig} onFieldChange={updateConfigField} />
        </div>
      </div>

      <div className={`pointer-events-none fixed bottom-5 left-1/2 z-50 max-w-[min(560px,calc(100%-28px))] -translate-x-1/2 rounded-[1rem] border border-[hsl(var(--border))] bg-[hsl(var(--card))]/95 px-4 py-3 text-[hsl(var(--foreground))] shadow-[0_24px_60px_rgba(57,63,51,.16)] transition-all ${toast ? "opacity-100" : "opacity-0"}`}>
        {toast}
      </div>
    </div>
  );
}

export default App;
