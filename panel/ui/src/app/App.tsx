import { FormEvent, useCallback, useEffect, useMemo, useState } from "react";
import { getState, postJSON } from "../api/client";
import { Chip, IconDot } from "../components/Chip";
import { displayHost, errorMessage, formatTestTime, mbps, ms } from "../lib/format";
import type { ApiResponse, ConfigForm, PanelState, ServerTest, ThemeMode } from "../types/panel";

const emptyState: PanelState = {
  listen: "",
  doh: "",
  panelUsername: "",
  panelAuthEnabled: false,
  servers: [],
  service: { active: false }
};

const topButtonClass = "h-[30px] cursor-pointer rounded-full border border-[hsl(var(--border))] bg-[hsl(var(--card))]/85 px-[11px] font-mono text-xs text-[hsl(var(--foreground))] transition-colors hover:bg-[hsl(var(--accent))]/70 disabled:cursor-wait disabled:opacity-55";
const smallButtonClass = "h-[34px] cursor-pointer whitespace-nowrap rounded-full border border-[hsl(var(--border))] bg-[hsl(var(--card))]/85 px-[13px] text-[13px] font-medium text-[hsl(var(--foreground))] transition-colors hover:bg-[hsl(var(--accent))]/70 disabled:cursor-wait disabled:opacity-55";
const primaryButtonClass = "cursor-pointer rounded-full border border-[hsl(var(--primary))] bg-[hsl(var(--primary))] text-[13px] font-medium text-[hsl(var(--primary-foreground))] transition-all hover:brightness-110 disabled:cursor-wait disabled:opacity-55";
const inputClass = "min-h-[42px] rounded-[calc(0.95rem-2px)] border border-[hsl(var(--input))] bg-[hsl(var(--card))]/85 px-[14px] font-mono text-[13px] text-[hsl(var(--foreground))] transition-all focus:border-[hsl(var(--ring))] focus:shadow-[0_0_0_3px_hsl(var(--ring)/0.16)] focus:outline-none";
const labelClass = "grid grid-cols-[94px_minmax(0,1fr)] items-center gap-[10px] max-[560px]:grid-cols-1";
const labelTextClass = "font-mono text-[11px] uppercase tracking-[0.22em] text-[hsl(var(--muted-foreground))]";

function App() {
  const [state, setState] = useState<PanelState>(emptyState);
  const [serverInput, setServerInput] = useState("");
  const [config, setConfig] = useState<ConfigForm>({ listen: "", doh: "", panelAuthEnabled: false, panelUsername: "", panelPassword: "" });
  const [settingsDirty, setSettingsDirty] = useState(false);
  const [theme, setTheme] = useState<ThemeMode>(() => (localStorage.getItem("turnsocks-theme") as ThemeMode) || "system");
  const [testing, setTesting] = useState<Set<string>>(() => new Set());
  const [busy, setBusy] = useState(false);
  const [toast, setToast] = useState("");

  const currentServer = useMemo(() => state.servers.find((s) => s.current) || state.servers[0], [state.servers]);

  const showToast = useCallback((message: string) => {
    setToast(message);
    window.clearTimeout(window.__turnsocksToastTimer);
    window.__turnsocksToastTimer = window.setTimeout(() => setToast(""), 2200);
  }, []);

  const syncConfig = useCallback((next: PanelState) => {
    if (settingsDirty) return;
    setConfig({
      listen: next.listen || "",
      doh: next.doh || "",
      panelAuthEnabled: !!next.panelAuthEnabled,
      panelUsername: next.panelUsername || "",
      panelPassword: ""
    });
  }, [settingsDirty]);

  const refresh = useCallback(async () => {
    const next = await getState();
    setState(next);
    syncConfig(next);
    return next;
  }, [syncConfig]);

  useEffect(() => {
    refresh().catch((err) => showToast(errorMessage(err)));
  }, [refresh, showToast]);

  useEffect(() => {
    const timer = window.setInterval(() => {
      if (!busy && testing.size === 0) {
        refresh().catch(() => {});
      }
    }, 5000);
    return () => window.clearInterval(timer);
  }, [busy, refresh, testing.size]);

  useEffect(() => {
    const applyTheme = () => {
      const dark = theme === "dark" || (theme === "system" && window.matchMedia?.("(prefers-color-scheme: dark)").matches);
      document.documentElement.classList.toggle("dark", dark);
      localStorage.setItem("turnsocks-theme", theme);
    };
    applyTheme();
    const media = window.matchMedia?.("(prefers-color-scheme: dark)");
    media?.addEventListener("change", applyTheme);
    return () => media?.removeEventListener("change", applyTheme);
  }, [theme]);

  async function run(action: () => Promise<ApiResponse>) {
    if (busy) return;
    setBusy(true);
    try {
      const res = await action();
      showToast(res.message || "完成");
      await refresh();
    } catch (err) {
      showToast(errorMessage(err));
    } finally {
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
      const res = await postJSON<ApiResponse>("/api/servers/add", { server });
      setServerInput("");
      return res;
    });
  }

  async function testServer(server: string) {
    if (!server || testing.has(server)) return;
    setTesting((prev) => new Set(prev).add(server));
    try {
      const result = await postJSON<ServerTest>("/api/servers/test", { server });
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
      setTesting((prev) => {
        const next = new Set(prev);
        next.delete(server);
        return next;
      });
    }
  }

  async function testAllServers() {
    if (testing.size > 0) return;
    for (const server of state.servers) {
      await testServer(server.raw);
    }
  }

  async function updateConfig(event: FormEvent) {
    event.preventDefault();
    await run(async () => {
      const res = await postJSON<ApiResponse>("/api/config/update", {
        listen: config.listen.trim(),
        doh: config.doh.trim(),
        panelAuthEnabled: config.panelAuthEnabled,
        panelUsername: config.panelUsername.trim(),
        panelPassword: config.panelPassword.trim()
      });
      setConfig((prev) => ({ ...prev, panelPassword: "" }));
      setSettingsDirty(false);
      return res;
    });
  }

  function updateConfigField<K extends keyof ConfigForm>(key: K, value: ConfigForm[K]) {
    setSettingsDirty(true);
    setConfig((prev) => ({ ...prev, [key]: value }));
  }

  const locked = busy || testing.size > 0;

  return (
    <div className="min-h-screen p-4 pb-12 md:p-6">
      <div className="relative z-10 mx-auto max-w-[1180px]">
        <header className="mb-6 flex flex-col justify-between gap-4 md:mb-8 md:flex-row md:items-center">
          <h1 className="text-[32px] font-bold leading-none tracking-tight text-[hsl(var(--foreground))] md:text-[42px]">turnsocks</h1>
          <div className="flex flex-wrap items-center gap-2">
            <span className={`inline-flex min-h-[30px] items-center gap-1.5 rounded-full border px-[11px] font-mono text-[11px] uppercase tracking-[0.12em] ${state.service.active ? "border-[hsl(var(--warn))]/30 bg-[hsl(var(--warn))]/10 text-[hsl(var(--warn))]" : "border-[hsl(var(--danger))]/30 bg-[hsl(var(--danger))]/10 text-[hsl(var(--danger))]"}`}>
              <IconDot />
              {state.service.active ? "RUNNING" : "STOPPED"}
            </span>
            <button className={topButtonClass} disabled={busy} onClick={() => run(() => postJSON<ApiResponse>("/api/restart"))} type="button">重启代理</button>
            <form action="/logout" method="post">
              <button className={topButtonClass} type="submit">退出</button>
            </form>
            <div className="flex rounded-full border border-[hsl(var(--border))] bg-[hsl(var(--muted))]/80 p-1 shadow-[inset_0_1px_0_rgba(255,255,255,0.35)] dark:shadow-none">
              {(["light", "system", "dark"] as ThemeMode[]).map((mode) => (
                <button key={mode} onClick={() => setTheme(mode)} className={`cursor-pointer rounded-full px-[9px] py-1 font-mono text-[11px] transition-all ${theme === mode ? "bg-[hsl(var(--card))] text-[hsl(var(--foreground))] shadow-[0_1px_3px_rgba(0,0,0,0.08)]" : "text-[hsl(var(--muted-foreground))] hover:text-[hsl(var(--foreground))]"}`} type="button">
                  {mode === "light" ? "浅色" : mode === "system" ? "跟随" : "深色"}
                </button>
              ))}
            </div>
          </div>
        </header>

        <div className="grid grid-cols-1 items-start gap-5 md:grid-cols-[minmax(0,1fr)_340px] lg:grid-cols-[minmax(0,1fr)_420px]">
          <div className="flex flex-col gap-5">
            <section className="shell-window relative flex min-h-[178px] flex-col justify-center overflow-hidden p-6 md:p-8">
              <div className="pointer-events-none absolute inset-0 opacity-65" style={{ background: "linear-gradient(120deg, hsl(var(--primary) / .08), transparent 42%), repeating-linear-gradient(90deg, transparent 0 21px, hsl(var(--border) / .35) 22px)" }} />
              <div className="relative z-10">
                <div className="mb-3 font-mono text-[11px] uppercase tracking-[0.22em] text-[hsl(var(--muted-foreground))]">current turn</div>
                <div className="mb-5 break-all text-[26px] font-medium leading-[1.12] tracking-tight text-[hsl(var(--foreground))] sm:text-[36px] md:text-[42px]">
                  {displayHost(currentServer)}
                </div>
                <div className="flex flex-wrap gap-2">
                  <Chip>{currentServer?.hasAuth ? "鉴权已配置" : "无鉴权"}</Chip>
                  <Chip>{state.listen || "-"}</Chip>
                  <Chip>{state.servers.length} 节点</Chip>
                </div>
              </div>
            </section>

            <section className="shell-window flex flex-col overflow-hidden">
              <div className="flex min-h-[44px] items-center justify-between border-b border-[hsl(var(--border))] bg-[hsl(var(--muted))]/70 px-4">
                <strong className="text-sm font-semibold text-[hsl(var(--foreground))]">节点管理</strong>
                <div className="flex items-center gap-2">
                  <button className={topButtonClass} disabled={locked} onClick={testAllServers} type="button">测试全部</button>
                  <Chip>首节点为默认</Chip>
                </div>
              </div>

              <div className="p-4 md:p-[18px]">
                <form className="mb-5 border-b border-[hsl(var(--border))] pb-5" onSubmit={addServer}>
                  <div className="mb-2 font-mono text-[11px] uppercase tracking-[0.12em] text-[hsl(var(--muted-foreground))]">添加 TURN 节点</div>
                  <div className="flex flex-col gap-2.5 sm:flex-row">
                    <input type="text" placeholder="host:port 或 user:pass@host:port" value={serverInput} onChange={(event) => setServerInput(event.target.value)} className={`${inputClass} flex-1`} />
                    <button className={`${primaryButtonClass} min-h-[42px] whitespace-nowrap px-5`} disabled={busy} type="submit">添加</button>
                  </div>
                </form>

                <div className="grid gap-4">
                  {state.servers.map((server) => {
                    const isCurrent = server.current;
                    const isTesting = testing.has(server.raw);
                    const test = server.test;
                    return (
                      <article key={server.raw} className={`overflow-hidden rounded-[1rem] border transition-colors ${isCurrent ? "border-[hsl(var(--ok))]/35 bg-[hsl(var(--ok))]/[0.06]" : "border-[hsl(var(--border))] bg-[hsl(var(--card))]/80"}`}>
                        <div className="flex flex-col justify-between gap-4 p-4 sm:flex-row sm:items-center">
                          <div>
                            <div className="mb-2.5 break-all font-mono text-[14px] leading-[1.55] text-[hsl(var(--foreground))]">{server.raw}</div>
                            <div className="flex flex-wrap gap-[7px]">
                              {isCurrent ? <Chip active>运行中</Chip> : server.default ? <Chip>默认</Chip> : <Chip>备用</Chip>}
                              <Chip>{server.hasAuth ? `鉴权: ${server.username || "已配置"}` : "无鉴权"}</Chip>
                              {isTesting && <Chip warn><span className="animate-pulse">测试中...</span></Chip>}
                            </div>
                          </div>
                          <div className="flex shrink-0 items-center justify-end gap-2">
                            <button disabled={busy || isTesting} onClick={() => testServer(server.raw)} className={smallButtonClass} type="button">测试</button>
                            {!isCurrent && (
                              <button disabled={busy} onClick={() => run(() => postJSON<ApiResponse>("/api/servers/select", { server: server.raw }))} className={`${primaryButtonClass} h-[34px] whitespace-nowrap px-[13px]`} type="button">切换至此</button>
                            )}
                            <button disabled={busy} onClick={() => window.confirm("确定要删除此节点吗？") && run(() => postJSON<ApiResponse>("/api/servers/delete", { server: server.raw }))} className="h-[34px] cursor-pointer whitespace-nowrap rounded-full border border-transparent px-[13px] text-[13px] font-medium text-[hsl(var(--danger))] transition-colors hover:bg-[hsl(var(--danger))]/10 disabled:cursor-wait disabled:opacity-55" type="button">删除</button>
                          </div>
                        </div>

                        {test && !isTesting && (
                          <div className={`overflow-x-auto border-t border-[hsl(var(--border))]/60 px-4 py-3 font-mono text-[12px] leading-relaxed ${test.ok ? "bg-[hsl(var(--muted))]/30" : "bg-[hsl(var(--danger))]/10"}`}>
                            {test.ok ? (
                              <div className="flex flex-col gap-x-6 gap-y-2 text-[hsl(var(--foreground))] sm:flex-row sm:flex-wrap sm:items-center">
                                <span><span className="text-[hsl(var(--muted-foreground))]">综合评分: </span><b className="text-[hsl(var(--ok))]">{test.score || 0}</b></span>
                                <span className="hidden text-[hsl(var(--border))] sm:block">|</span>
                                <span><span className="text-[hsl(var(--muted-foreground))]">已测: </span>{formatTestTime(test.testedAt)}</span>
                                <span className="hidden text-[hsl(var(--border))] sm:block">|</span>
                                <span><span className="text-[hsl(var(--muted-foreground))]">TCP: </span>{test.tcpConnect?.ok ? ms(test.tcpConnect.avgMs) : "失败"}</span>
                                <span className="hidden text-[hsl(var(--border))] sm:block">|</span>
                                <span><span className="text-[hsl(var(--muted-foreground))]">UDP转发: </span><span className={test.socksUdp?.ok ? "text-[hsl(var(--ok))]" : "text-[hsl(var(--danger))]"}>{test.socksUdp?.ok ? "OK" : "失败"}</span></span>
                                <span className="hidden text-[hsl(var(--border))] sm:block">|</span>
                                <span><span className="text-[hsl(var(--muted-foreground))]">单线: </span>{test.singleThread?.ok ? `${mbps(test.singleThread.mbps)}${test.singleThread.source ? ` · ${test.singleThread.source}` : ""}` : "失败"}</span>
                                <span className="hidden text-[hsl(var(--border))] sm:block">|</span>
                                <span><span className="text-[hsl(var(--muted-foreground))]">多线: </span>{test.multiThread?.ok ? `${mbps(test.multiThread.mbps)}${test.multiThread.source ? ` · ${test.multiThread.source}` : ""}` : "失败"}</span>
                              </div>
                            ) : (
                              <div className="flex items-center gap-2 text-[hsl(var(--danger))]">
                                <b>[测试失败]</b> {test.message || "未知错误"}
                                <span className="ml-auto text-[11px] text-[hsl(var(--muted-foreground))]">{formatTestTime(test.testedAt)}</span>
                              </div>
                            )}
                          </div>
                        )}
                      </article>
                    );
                  })}
                  {state.servers.length === 0 && (
                    <div className="rounded-[1rem] border border-dashed border-[hsl(var(--border))] p-6 text-center text-[hsl(var(--muted-foreground))]">暂无节点</div>
                  )}
                </div>
              </div>
            </section>
          </div>

          <aside className="flex flex-col gap-5">
            <section className="shell-window overflow-hidden">
              <div className="flex min-h-[44px] items-center justify-between border-b border-[hsl(var(--border))] bg-[hsl(var(--muted))]/70 px-4">
                <strong className="text-sm font-semibold text-[hsl(var(--foreground))]">概览</strong>
                <Chip>PID {state.service.pid || "-"}</Chip>
              </div>
              <div className="grid gap-[10px] p-4 md:p-[18px]">
                <div className="rounded-[1rem] border border-[hsl(var(--border))] bg-[hsl(var(--muted))]/45 p-[14px]">
                  <div className="font-mono text-[11px] uppercase tracking-[0.22em] text-[hsl(var(--muted-foreground))]">DOH</div>
                  <div className="mt-2 break-all font-mono text-[14px] leading-[1.55] text-[hsl(var(--foreground))]">{state.doh || "-"}</div>
                </div>
              </div>
            </section>

            <section className="shell-window overflow-hidden">
              <div className="flex min-h-[44px] items-center justify-between border-b border-[hsl(var(--border))] bg-[hsl(var(--muted))]/70 px-4">
                <strong className="text-sm font-semibold text-[hsl(var(--foreground))]">配置</strong>
                <Chip>CONFIG.ENV</Chip>
              </div>
              <form className="grid gap-[10px] p-4 md:p-[18px]" onSubmit={updateConfig}>
                <label className={labelClass}>
                  <span className={labelTextClass}>SOCKS5</span>
                  <input type="text" value={config.listen} onChange={(event) => updateConfigField("listen", event.target.value)} className={inputClass} />
                </label>
                <label className={labelClass}>
                  <span className={labelTextClass}>DOH</span>
                  <input type="text" value={config.doh} onChange={(event) => updateConfigField("doh", event.target.value)} className={inputClass} />
                </label>
                <label className="inline-flex min-h-[34px] cursor-pointer items-center gap-[10px]">
                  <input type="checkbox" checked={config.panelAuthEnabled} onChange={(event) => updateConfigField("panelAuthEnabled", event.target.checked)} className="h-[18px] w-[18px] cursor-pointer accent-[hsl(var(--primary))]" />
                  <span className="text-[13px] text-[hsl(var(--muted-foreground))]">面板登录</span>
                </label>
                <label className={labelClass}>
                  <span className={labelTextClass}>用户</span>
                  <input type="text" value={config.panelUsername} onChange={(event) => updateConfigField("panelUsername", event.target.value)} className={inputClass} />
                </label>
                <label className={labelClass}>
                  <span className={labelTextClass}>密码</span>
                  <input type="password" placeholder="留空不修改" value={config.panelPassword} onChange={(event) => updateConfigField("panelPassword", event.target.value)} className={inputClass} />
                </label>
                <button className={`${primaryButtonClass} mt-[10px] min-h-[42px] w-full`} disabled={busy} type="submit">保存配置</button>
              </form>
            </section>
          </aside>
        </div>
      </div>

      <div className={`pointer-events-none fixed bottom-5 left-1/2 z-50 max-w-[min(560px,calc(100%-28px))] -translate-x-1/2 rounded-[1rem] border border-[hsl(var(--border))] bg-[hsl(var(--card))]/95 px-4 py-3 text-[hsl(var(--foreground))] shadow-[0_24px_60px_rgba(57,63,51,.16)] transition-all ${toast ? "opacity-100" : "opacity-0"}`}>
        {toast}
      </div>
    </div>
  );
}

export default App;

declare global {
  interface Window {
    __turnsocksToastTimer?: number;
  }
}
