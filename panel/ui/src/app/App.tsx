import { FormEvent, useCallback, useEffect, useRef, useState } from "react";
import { flushSync } from "react-dom";
import { addServer as addServerRequest, deleteServer, getState, restartProxy, selectServer, testServer as testServerRequest, updateConfig as updateConfigRequest } from "../api/client";
import { IconLogout, IconMonitor, IconMoon, IconRefresh, IconRelay, IconSun } from "../components/icons";
import { ghostButtonClass, topButtonClass } from "../controlClasses";
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
        <header className="mb-6 flex flex-col justify-between gap-4 md:mb-7 md:flex-row md:items-center">
          <div className="flex items-center gap-3">
            <div className="grid h-8 w-8 flex-none place-items-center rounded-[9px] bg-gradient-to-br from-[#6366f1] to-[#8b5cf6] text-white shadow-[0_2px_8px_rgba(99,102,241,0.35)]">
              <IconRelay className="h-[17px] w-[17px]" />
            </div>
            <h1 className="text-[17px] font-semibold leading-none text-[hsl(var(--foreground))]">turnsocks</h1>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <span className="inline-flex h-[34px] items-center gap-2 rounded-full border border-[hsl(var(--border))] bg-[hsl(var(--card))] px-[13px] text-[13px] font-medium leading-none text-[hsl(var(--foreground))] shadow-sm">
              {state.service.active ? (
                <span className="relative flex h-2 w-2">
                  <span className="absolute inline-flex h-full w-full animate-ping rounded-full bg-[hsl(var(--ok))] opacity-60 motion-reduce:animate-none" />
                  <span className="relative inline-flex h-2 w-2 rounded-full bg-[hsl(var(--ok))]" />
                </span>
              ) : (
                <span className="flex h-2 w-2 rounded-full bg-[hsl(var(--danger))]" />
              )}
              {state.service.active ? "代理运行中" : "代理已停止"}
            </span>
            <button className={topButtonClass} disabled={busy} onClick={() => run(restartProxy)} type="button">
              <IconRefresh className="h-3.5 w-3.5" />
              重启代理
            </button>
            <form action="/logout" method="post">
              <button className={ghostButtonClass} type="submit">
                <IconLogout className="h-3.5 w-3.5" />
                退出登录
              </button>
            </form>
            <div className="flex gap-0.5 rounded-full border border-[hsl(var(--border))] bg-[hsl(var(--card))] p-[3px] shadow-sm">
              {(["light", "system", "dark"] as ThemeMode[]).map((mode) => {
                const Icon = mode === "light" ? IconSun : mode === "system" ? IconMonitor : IconMoon;
                const label = mode === "light" ? "浅色" : mode === "system" ? "跟随系统" : "深色";
                return (
                  <button key={mode} aria-label={label} aria-pressed={theme === mode} title={label} onClick={(event) => changeTheme(mode, event.currentTarget)} className={`grid h-[26px] w-[30px] cursor-pointer place-items-center rounded-full transition-colors ${theme === mode ? "bg-[hsl(var(--primary))]/10 text-[hsl(var(--primary))]" : "text-[hsl(var(--muted-foreground))] hover:text-[hsl(var(--foreground))]"}`} type="button">
                    <Icon className="h-3.5 w-3.5" />
                  </button>
                );
              })}
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

      <div className={`pointer-events-none fixed bottom-5 left-1/2 z-50 max-w-[min(560px,calc(100%-28px))] -translate-x-1/2 rounded-[11px] border border-[hsl(var(--border))] bg-[hsl(var(--card))]/95 px-4 py-3 text-[13px] font-medium text-[hsl(var(--foreground))] shadow-[0_8px_30px_rgba(0,0,0,.12)] transition-all ${toast ? "opacity-100" : "opacity-0"}`}>
        {toast}
      </div>
    </div>
  );
}

export default App;
