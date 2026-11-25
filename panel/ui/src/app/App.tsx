import { useCallback, useEffect, useRef, useState, type SubmitEvent } from "react";
import { flushSync } from "react-dom";
import { addServer as addServerRequest, deleteServer, getState, restartProxy, selectServer, testServer as testServerRequest, updateConfig as updateConfigRequest, updateServerNote } from "../api/client";
import { Chip } from "../components/Chip";
import { IconAlert, IconCheck, IconCopy, IconLogout, IconMonitor, IconMoon, IconRefresh, IconRelay, IconSun, IconTrash } from "../components/icons";
import { topButtonClass } from "../controlClasses";
import { NodePanel } from "../features/nodes/NodePanel";
import { SettingsPanel, type SideTab } from "../features/settings/SettingsPanel";
import { copyText, displayDohHost, errorMessage } from "../lib/format";
import type { ActiveProbe, ApiResponse, BusyAction, ConfigForm, PanelState, ProbeMode, ThemeMode } from "../types/panel";

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

function configFromState(state: PanelState): ConfigForm {
  return {
    listen: state.listen || "",
    doh: state.doh || "",
    panelAuthEnabled: !!state.panelAuthEnabled,
    panelUsername: state.panelUsername || "",
    panelPassword: ""
  };
}

function isConfigDirty(config: ConfigForm, state: PanelState) {
  return (
    config.listen.trim() !== (state.listen || "") ||
    config.doh.trim() !== (state.doh || "") ||
    config.panelAuthEnabled !== !!state.panelAuthEnabled ||
    config.panelUsername.trim() !== (state.panelUsername || "") ||
    config.panelPassword.trim() !== ""
  );
}

function applyTheme(theme: ThemeMode) {
  const dark = theme === "dark" || (theme === "system" && window.matchMedia?.("(prefers-color-scheme: dark)").matches);
  document.documentElement.classList.toggle("dark", dark);
  localStorage.setItem("turnsocks-theme", theme);
}

function App() {
  const [state, setState] = useState<PanelState>(emptyState);
  const [serverInput, setServerInput] = useState("");
  const [config, setConfig] = useState<ConfigForm>(emptyConfig);
  const [sideTab, setSideTab] = useState<SideTab>("events");
  const [theme, setTheme] = useState<ThemeMode>(() => (localStorage.getItem("turnsocks-theme") as ThemeMode) || "system");
  const [testing, setTesting] = useState<ActiveProbe | null>(null);
  const [busyAction, setBusyAction] = useState<BusyAction>(null);
  const [loaded, setLoaded] = useState(false);
  const [loadError, setLoadError] = useState("");
  const [copiedKey, setCopiedKey] = useState("");
  const [toast, setToast] = useState<{ message: string; tone: "info" | "danger" }>({ message: "", tone: "info" });
  const [deleteTarget, setDeleteTarget] = useState("");
  const settingsDirty = useRef(false);
  const busyRef = useRef(false);
  const testController = useRef<AbortController | null>(null);
  const refreshVersion = useRef(0);
  const toastTimer = useRef<number>(undefined);
  const copyTimer = useRef<number>(undefined);
  const deleteDialog = useRef<HTMLDialogElement>(null);
  const sidePanelRef = useRef<HTMLElement>(null);

  const showToast = useCallback((message: string, tone: "info" | "danger" = "info") => {
    setToast({ message, tone });
    window.clearTimeout(toastTimer.current);
    toastTimer.current = window.setTimeout(() => setToast((prev) => ({ ...prev, message: "" })), tone === "danger" ? 4500 : 2400);
  }, []);

  const handleCopy = useCallback(async (text: string, key: string, label: string) => {
    const ok = await copyText(text);
    if (!ok) {
      showToast("复制失败，请手动选择", "danger");
      return;
    }
    setCopiedKey(key);
    window.clearTimeout(copyTimer.current);
    copyTimer.current = window.setTimeout(() => setCopiedKey((prev) => (prev === key ? "" : prev)), 1800);
    showToast(`已复制${label}`);
  }, [showToast]);

  useEffect(() => () => {
    window.clearTimeout(toastTimer.current);
    window.clearTimeout(copyTimer.current);
    testController.current?.abort();
  }, []);

  useEffect(() => {
    const dialog = deleteDialog.current;
    if (!dialog) return;
    if (deleteTarget && !dialog.open) dialog.showModal();
    if (!deleteTarget && dialog.open) dialog.close();
  }, [deleteTarget]);

  const refresh = useCallback(async () => {
    const version = ++refreshVersion.current;
    const next = await getState();
    if (version !== refreshVersion.current) return next;
    setState(next);
    setLoadError("");
    setLoaded(true);
    if (!settingsDirty.current) {
      setConfig(configFromState(next));
    }
    return next;
  }, []);

  const loadInitial = useCallback(() => {
    setLoadError("");
    return refresh().catch((err) => {
      const msg = errorMessage(err);
      setLoadError(msg);
      showToast(msg, "danger");
    });
  }, [refresh, showToast]);

  useEffect(() => {
    void loadInitial();
  }, [loadInitial]);

  useEffect(() => {
    const poll = () => {
      if (!document.hidden && !busyRef.current && !testController.current) {
        refresh().catch(() => {});
      }
    };
    const timer = window.setInterval(poll, 5000);
    const onVisibilityChange = () => {
      if (!document.hidden) poll();
    };
    document.addEventListener("visibilitychange", onVisibilityChange);
    return () => {
      window.clearInterval(timer);
      document.removeEventListener("visibilitychange", onVisibilityChange);
    };
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

  async function run(kind: Exclude<BusyAction, null>, action: () => Promise<ApiResponse>, refreshAfter = true) {
    if (busyRef.current || testController.current) return false;
    busyRef.current = true;
    refreshVersion.current++;
    setBusyAction(kind);
    try {
      const res = await action();
      showToast(res.message || "操作完成");
      if (refreshAfter) await refresh();
      return true;
    } catch (err) {
      showToast(errorMessage(err), "danger");
      return false;
    } finally {
      busyRef.current = false;
      setBusyAction(null);
    }
  }

  async function addServer(event: SubmitEvent<HTMLFormElement>) {
    event.preventDefault();
    const server = serverInput.trim();
    if (!server) {
      showToast("节点不能为空", "danger");
      return;
    }
    await run("add", async () => {
      const res = await addServerRequest(server);
      setServerInput("");
      return res;
    });
  }

  async function testServers(servers: string[], mode: ProbeMode) {
    if (!servers.length || testController.current || busyRef.current) return;
    const controller = new AbortController();
    testController.current = controller;
    refreshVersion.current++;
    try {
      for (const [idx, server] of servers.entries()) {
        controller.signal.throwIfAborted();
        setTesting({ server, mode, index: idx + 1, total: servers.length });
        const result = await testServerRequest(server, mode, controller.signal);
        setState((prev) => ({
          ...prev,
          servers: prev.servers.map((item) => item.raw === server ? { ...item, [mode === "check" ? "check" : "test"]: result } : item)
        }));
        if (servers.length === 1) showToast(result.message || "检测完成", result.ok ? "info" : "danger");
      }
      if (servers.length > 1) showToast("全部检测完成，请查看各节点结果");
    } catch (err) {
      if (controller.signal.aborted) showToast("检测已停止，已完成的结果已保留");
      else showToast(errorMessage(err), "danger");
    } finally {
      testController.current = null;
      setTesting(null);
      refresh().catch(() => {});
    }
  }

  async function updateConfig(event: SubmitEvent<HTMLFormElement>) {
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
    const ok = await run("config", async () => {
      const res = await updateConfigRequest(payload);
      setConfig((prev) => ({ ...prev, panelPassword: "" }));
      settingsDirty.current = false;
      return res;
    }, !loginChanged);
    if (ok && loginChanged) window.location.assign("/login");
  }

  function updateConfigField<K extends keyof ConfigForm>(key: K, value: ConfigForm[K]) {
    setConfig((prev) => {
      const next = { ...prev, [key]: value };
      settingsDirty.current = isConfigDirty(next, state);
      return next;
    });
  }

  function resetConfig() {
    settingsDirty.current = false;
    setConfig(configFromState(state));
  }

  function removeServer(server: string) {
    setDeleteTarget(server);
  }

  function confirmRemoveServer() {
    const server = deleteTarget;
    setDeleteTarget("");
    if (server) void run("delete", () => deleteServer(server));
  }

  const locked = busyAction !== null || testing !== null;
  const dirty = loaded && isConfigDirty(config, state);
  const restartsProxy = loaded && (config.listen.trim() !== (state.listen || "") || config.doh.trim() !== (state.doh || ""));

  const gatewayTile = (
    <section className="bento-tile flex flex-col justify-between p-5">
      <div>
        <div className="flex items-center justify-between gap-2 text-[12px] text-[hsl(var(--muted-foreground))]">
          <span className="font-medium">本地代理入口</span>
          <Chip mono>PID {state.service.pid || "-"}</Chip>
        </div>

        <button
          type="button"
          disabled={!state.listen}
          onClick={() => void handleCopy(state.listen, "listen", " SOCKS5 地址")}
          aria-label={`复制 SOCKS5 地址 ${state.listen || ""}`}
          className="mt-3.5 flex w-full cursor-pointer items-center justify-between gap-2.5 rounded-[12px] border border-[hsl(var(--border))] bg-[hsl(var(--muted))] px-3.5 py-2.5 text-left transition-colors hover:border-[hsl(var(--primary))]/45 disabled:cursor-default disabled:opacity-60"
        >
          <div className="min-w-0">
            <div className="text-[10.5px] font-medium text-[hsl(var(--muted-foreground))]">
              {copiedKey === "listen" ? "已复制到剪贴板" : "SOCKS5 监听 · 点击复制"}
            </div>
            <div className="mt-0.5 truncate font-mono text-[14.5px] font-semibold tabular-nums text-[hsl(var(--foreground))]">
              {state.listen || "-"}
            </div>
          </div>
          {copiedKey === "listen"
            ? <IconCheck className="h-4 w-4 shrink-0 text-[hsl(var(--ok))]" />
            : <IconCopy className="h-4 w-4 shrink-0 text-[hsl(var(--primary-text))]" />}
        </button>

        <div className="mt-3 grid grid-cols-[minmax(0,1.55fr)_minmax(0,0.85fr)] gap-2">
          <div
            className={`min-w-0 rounded-[10px] bg-[hsl(var(--muted))] px-3 py-2 ${state.doh ? "ui-tooltip" : ""}`}
            data-tooltip={state.doh || undefined}
            data-tooltip-side="top"
            data-tooltip-align="left"
            tabIndex={state.doh ? 0 : undefined}
          >
            <div className="text-[10.5px] text-[hsl(var(--muted-foreground))]">DoH 解析</div>
            <div className="mt-0.5 truncate font-mono text-[11px] font-semibold text-[hsl(var(--foreground))]">{displayDohHost(state.doh)}</div>
          </div>

          <button
            type="button"
            onClick={() => {
              setSideTab("config");
              sidePanelRef.current?.scrollIntoView({ behavior: "smooth", block: "nearest" });
            }}
            className="min-w-0 cursor-pointer rounded-[10px] bg-[hsl(var(--muted))] px-2.5 py-2 text-left transition-colors hover:bg-[hsl(var(--border))]/55"
          >
            <div className="text-[10.5px] text-[hsl(var(--muted-foreground))]">面板认证</div>
            <div className={`mt-0.5 truncate text-[12px] font-semibold ${state.panelAuthEnabled ? "text-[hsl(var(--ok))]" : "text-[hsl(var(--muted-foreground))]"}`}>
              {state.panelAuthEnabled ? "已开启" : "已关闭"}
            </div>
          </button>
        </div>
      </div>

      <button className={`${topButtonClass} mt-3.5 w-full`} disabled={locked} onClick={() => void run("restart", restartProxy)} type="button">
        <IconRefresh className={`h-3.5 w-3.5 ${busyAction === "restart" ? "animate-spin motion-reduce:animate-none" : ""}`} />
        {busyAction === "restart" ? "重启中…" : "重启代理服务"}
      </button>
    </section>
  );

  const sidePanel = (
    <SettingsPanel
      state={state}
      config={config}
      busy={locked}
      saving={busyAction === "config"}
      dirty={dirty}
      restartsProxy={restartsProxy}
      activeTab={sideTab}
      panelRef={sidePanelRef}
      onTabChange={setSideTab}
      onSubmit={updateConfig}
      onReset={resetConfig}
      onFieldChange={updateConfigField}
    />
  );

  return (
    <div className="min-h-screen px-3 py-4 pb-12 sm:px-5 md:p-6">
      {loaded && (
        <div className="relative z-10 mx-auto max-w-[1180px]">
          <header className="mb-4 flex flex-wrap items-center justify-between gap-2 md:mb-5">
            <div className="flex shrink-0 items-center gap-2 sm:gap-2.5">
              <div className="brand-mark h-[30px] w-[30px] sm:h-[34px] sm:w-[34px]">
                <IconRelay className="h-[15px] w-[15px] sm:h-[16px] sm:w-[16px]" />
              </div>
              <h1 className="whitespace-nowrap text-[16px] font-bold leading-none tracking-[-0.015em] text-[hsl(var(--foreground))] sm:text-[16.5px]">turnsocks</h1>
            </div>

            <div className="flex shrink-0 items-center gap-1 sm:gap-2">
              <span className="inline-flex h-[32px] shrink-0 items-center gap-1.5 whitespace-nowrap rounded-full border border-[hsl(var(--border))] bg-[hsl(var(--card))] px-2.5 text-[12px] font-medium leading-none text-[hsl(var(--foreground))] shadow-xs sm:px-3">
                {state.service.active ? (
                  <span className="relative flex h-2 w-2 shrink-0">
                    <span className="absolute inline-flex h-full w-full animate-ping rounded-full bg-[hsl(var(--ok))] opacity-60 motion-reduce:animate-none" />
                    <span className="relative inline-flex h-2 w-2 rounded-full bg-[hsl(var(--ok))]" />
                  </span>
                ) : (
                  <span className="flex h-2 w-2 shrink-0 rounded-full bg-[hsl(var(--danger))]" />
                )}
                <span className={`whitespace-nowrap ${state.service.active ? "text-[hsl(var(--ok))]" : "text-[hsl(var(--danger))]"}`}>
                  {state.service.active ? "运行中" : "已停止"}
                </span>
              </span>

              <div className="flex shrink-0 gap-0.5 rounded-full border border-[hsl(var(--border))] bg-[hsl(var(--card))] p-[3px] shadow-xs">
                {(["light", "system", "dark"] as ThemeMode[]).map((mode, idx) => {
                  const Icon = mode === "light" ? IconSun : mode === "system" ? IconMonitor : IconMoon;
                  const label = mode === "light" ? "浅色" : mode === "system" ? "跟随系统" : "深色";
                  return (
                    <button
                      key={mode}
                      aria-label={label}
                      aria-pressed={theme === mode}
                      data-tooltip={label}
                      data-tooltip-align={idx === 2 ? "right" : undefined}
                      onClick={(event) => changeTheme(mode, event.currentTarget)}
                      className={`ui-tooltip grid h-[24px] w-[24px] cursor-pointer place-items-center rounded-full transition-colors sm:w-[26px] ${theme === mode ? "bg-[hsl(var(--primary))]/12 text-[hsl(var(--primary-text))]" : "text-[hsl(var(--muted-foreground))] hover:text-[hsl(var(--foreground))]"}`}
                      type="button"
                    >
                      <Icon className="h-3.5 w-3.5" />
                    </button>
                  );
                })}
              </div>

              {state.panelAuthEnabled && (
                <form className="shrink-0" action="/logout" method="post">
                  <button
                    className="inline-flex h-[32px] w-[32px] cursor-pointer items-center justify-center gap-1.5 whitespace-nowrap rounded-[10px] border border-transparent bg-transparent text-[12.5px] font-medium leading-none text-[hsl(var(--muted-foreground))] transition-colors hover:bg-[hsl(var(--card))] hover:text-[hsl(var(--foreground))] sm:h-[34px] sm:w-auto sm:px-2.5"
                    aria-label="退出登录"
                    type="submit"
                  >
                    <IconLogout className="h-3.5 w-3.5" />
                    <span className="hidden sm:inline">退出</span>
                  </button>
                </form>
              )}
            </div>
          </header>

          <NodePanel
            state={state}
            serverInput={serverInput}
            testing={testing}
            busyAction={busyAction}
            locked={locked}
            copiedKey={copiedKey}
            gatewaySlot={gatewayTile}
            sideSlot={sidePanel}
            onCopy={(text, key, label) => void handleCopy(text, key, label)}
            onServerInput={setServerInput}
            onAddServer={addServer}
            onTestServer={(server, mode) => void testServers([server], mode)}
            onTestAll={(mode) => void testServers(state.servers.map((server) => server.raw), mode)}
            onStopTesting={() => testController.current?.abort()}
            onSelectServer={(server) => void run("select", () => selectServer(server))}
            onDeleteServer={removeServer}
            onUpdateNote={(server, note) => run("note", () => updateServerNote(server, note))}
          />
        </div>
      )}

      {!loaded && (
        <div className="fixed inset-0 z-40 grid place-items-center bg-[hsl(var(--background))] p-6">
          {loadError ? (
            <div className="bento-tile flex w-[min(360px,100%)] flex-col items-center gap-3 p-6 text-center">
              <div className="grid h-10 w-10 place-items-center rounded-[12px] bg-[hsl(var(--danger))]/10 text-[hsl(var(--danger))]">
                <IconAlert className="h-5 w-5" />
              </div>
              <div className="text-[14px] font-semibold text-[hsl(var(--foreground))]">面板加载失败</div>
              <div className="break-all text-[12.5px] leading-5 text-[hsl(var(--muted-foreground))]">{loadError}</div>
              <button className={`${topButtonClass} mt-1`} onClick={() => void loadInitial()} type="button">
                <IconRefresh className="h-3.5 w-3.5" />
                重试
              </button>
            </div>
          ) : (
            <div className="flex items-center gap-2.5 text-[13px] font-medium text-[hsl(var(--muted-foreground))]">
              <span className="h-4 w-4 animate-spin rounded-full border-2 border-[hsl(var(--border))] border-t-[hsl(var(--primary))] motion-reduce:animate-none" />
              正在加载面板
            </div>
          )}
        </div>
      )}

      <div aria-atomic="true" aria-live="polite" role="status" className={`pointer-events-none fixed bottom-5 left-1/2 z-50 flex max-w-[min(560px,calc(100%-28px))] -translate-x-1/2 items-center gap-2.5 rounded-[12px] border px-4 py-3 text-[13px] font-medium shadow-[0_10px_32px_rgba(0,0,0,.16)] transition-all ${toast.message ? "opacity-100" : "opacity-0"} ${toast.tone === "danger" ? "border-[hsl(var(--danger))]/30 bg-[hsl(var(--card))]/95 text-[hsl(var(--danger))]" : "border-[hsl(var(--border))] bg-[hsl(var(--card))]/95 text-[hsl(var(--foreground))]"}`}>
        {toast.tone === "danger" && <IconAlert className="h-[15px] w-[15px] flex-none" />}
        <span className="min-w-0 wrap-break-word">{toast.message}</span>
      </div>

      <dialog
        ref={deleteDialog}
        aria-describedby="delete-node-description"
        aria-labelledby="delete-node-title"
        className="m-auto w-[min(420px,calc(100%-32px))] rounded-[20px] border border-[hsl(var(--border))] bg-[hsl(var(--card))] p-0 text-[hsl(var(--foreground))] shadow-[0_24px_64px_rgba(0,0,0,.26)] backdrop:bg-black/45 backdrop:backdrop-blur-[3px]"
        onCancel={(event) => {
          event.preventDefault();
          setDeleteTarget("");
        }}
        onClose={() => setDeleteTarget("")}
        onClick={(event) => {
          if (event.target === event.currentTarget) setDeleteTarget("");
        }}
      >
        <div className="p-5">
          <div className="flex items-center gap-3">
            <div className="grid h-9 w-9 flex-none place-items-center rounded-[10px] bg-[hsl(var(--danger))]/10 text-[hsl(var(--danger))]">
              <IconAlert className="h-[17px] w-[17px]" />
            </div>
            <div className="min-w-0">
              <h2 id="delete-node-title" className="text-[14.5px] font-semibold">删除节点</h2>
              <p id="delete-node-description" className="mt-1 text-[13px] leading-5 text-[hsl(var(--muted-foreground))]">确定从节点列表中删除此节点？</p>
            </div>
          </div>

          <div className="mt-4 break-all rounded-[10px] border border-[hsl(var(--border))] bg-[hsl(var(--muted))] px-3 py-2.5 font-mono text-[12px] leading-5">
            {deleteTarget}
          </div>

          <div className="mt-5 flex justify-end gap-2">
            <button className={topButtonClass} onClick={() => setDeleteTarget("")} type="button">取消</button>
            <button className="inline-flex h-[34px] cursor-pointer items-center justify-center gap-1.5 rounded-[10px] border border-[hsl(var(--danger))]/20 bg-[hsl(var(--danger))]/10 px-[13px] text-[13px] font-medium leading-none text-[hsl(var(--danger))] transition-colors hover:bg-[hsl(var(--danger))]/15" onClick={confirmRemoveServer} type="button">
              <IconTrash className="h-3.5 w-3.5" />
              删除节点
            </button>
          </div>
        </div>
      </dialog>
    </div>
  );
}

export default App;
