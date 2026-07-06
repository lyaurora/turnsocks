import { FormEvent, useMemo, useState } from "react";
import { postJSON } from "../api/client";
import { Chip, IconDot } from "../components/Chip";
import { WindowSection, Button, Input, labelClass, labelTextClass } from "../components/UI";
import { displayHost, formatTestTime, mbps, ms } from "../lib/format";
import type { ApiResponse, ConfigForm, ThemeMode } from "../types/panel";
import { usePanel } from "../hooks/usePanel";
import { useTheme } from "../hooks/useTheme";

function App() {
  const panel = usePanel();
  const { theme, setTheme } = useTheme();
  const [serverInput, setServerInput] = useState("");

  const currentServer = useMemo(() => panel.state.servers.find((s) => s.current) || panel.state.servers[0], [panel.state.servers]);
  const locked = panel.busy || panel.testing.size > 0;

  async function addServer(event: FormEvent) {
    event.preventDefault();
    const server = serverInput.trim();
    if (!server) {
      panel.showToast("节点不能为空");
      return;
    }
    await panel.run(async () => {
      const res = await postJSON<ApiResponse>("/api/servers/add", { server });
      setServerInput("");
      return res;
    });
  }

  async function testAllServers() {
    if (panel.testing.size > 0) return;
    for (const server of panel.state.servers) {
      await panel.testServer(server.raw);
    }
  }

  async function updateConfig(event: FormEvent) {
    event.preventDefault();
    await panel.run(async () => {
      const res = await postJSON<ApiResponse>("/api/config/update", {
        listen: panel.config.listen.trim(),
        doh: panel.config.doh.trim(),
        panelAuthEnabled: panel.config.panelAuthEnabled,
        panelUsername: panel.config.panelUsername.trim(),
        panelPassword: panel.config.panelPassword.trim()
      });
      panel.setConfig((prev) => ({ ...prev, panelPassword: "" }));
      panel.setSettingsDirty(false);
      return res;
    });
  }

  function updateConfigField<K extends keyof ConfigForm>(key: K, value: ConfigForm[K]) {
    panel.setSettingsDirty(true);
    panel.setConfig((prev) => ({ ...prev, [key]: value }));
  }

  return (
    <div className="min-h-screen p-4 md:p-8 flex flex-col font-sans">
      <div className="mx-auto w-full max-w-5xl flex-1 flex flex-col gap-8">
        <header className="flex flex-col sm:flex-row sm:items-center justify-between gap-4 py-2 border-b border-[hsl(var(--border))] pb-6">
          <div className="flex items-center gap-3">
            <h1 className="text-[22px] font-bold tracking-tight text-[hsl(var(--foreground))]">turnsocks</h1>
            <span className={`inline-flex items-center gap-1.5 px-2 py-0.5 rounded text-[10px] uppercase font-bold tracking-widest transition-colors ${panel.state.service.active ? "bg-[hsl(var(--ok))]/15 text-[hsl(var(--ok))]" : "bg-[hsl(var(--danger))]/15 text-[hsl(var(--danger))]"}`}>
              <IconDot />
              {panel.state.service.active ? "运行中" : "已停止"}
            </span>
          </div>
          <div className="flex flex-wrap items-center gap-2.5 sm:justify-end">
            <Button variant="top" disabled={panel.busy} onClick={() => panel.run(() => postJSON<ApiResponse>("/api/restart"))}>重启代理</Button>
            <form action="/logout" method="post">
              <Button variant="top" type="submit">退出</Button>
            </form>
            <div className="flex rounded-[var(--radius)] border border-[hsl(var(--border))] bg-[hsl(var(--card))] p-[3px] shadow-sm">
              {(["light", "system", "dark"] as ThemeMode[]).map((mode) => (
                <button key={mode} onClick={() => setTheme(mode)} className={`cursor-pointer rounded-[4px] px-3 py-1 text-[13px] font-medium transition-all ${theme === mode ? "bg-[hsl(var(--muted))] text-[hsl(var(--foreground))] shadow-[0_1px_2px_rgba(0,0,0,0.05)]" : "text-[hsl(var(--muted-foreground))] hover:text-[hsl(var(--foreground))]"}`} type="button">
                  {mode === "light" ? "浅色" : mode === "system" ? "跟随" : "深色"}
                </button>
              ))}
            </div>
          </div>
        </header>

        <div className="grid grid-cols-1 lg:grid-cols-[1fr_320px] gap-10 items-start">
          <div className="flex flex-col gap-10">
            <div>
              <div className="text-[11px] uppercase tracking-widest text-[hsl(var(--muted-foreground))] mb-3 font-medium">当前节点</div>
              <div className="text-4xl sm:text-[3.25rem] font-mono font-medium tracking-tight break-all leading-none text-[hsl(var(--foreground))]">
                {displayHost(currentServer)}
              </div>
              <div className="flex flex-wrap gap-2 mt-5">
                <Chip>{currentServer?.hasAuth ? "鉴权已配置" : "无鉴权"}</Chip>
                <Chip>{panel.state.listen || "-"}</Chip>
              </div>
            </div>

            <WindowSection
              header="节点"
              right={<Button variant="top" disabled={locked} onClick={testAllServers}>测试全部</Button>}
            >
              <div className="p-0">
                <div className="p-4 border-b border-[hsl(var(--border))]/80 bg-[hsl(var(--card))]">
                  <form className="flex gap-2" onSubmit={addServer}>
                    <Input className="min-w-0 flex-1" type="text" placeholder="域名/IP:端口 或 用户:密码@域名/IP:端口" value={serverInput} onChange={(event) => setServerInput(event.target.value)} />
                    <Button variant="primary" className="shrink-0 min-h-[36px]" disabled={panel.busy} type="submit">添加</Button>
                  </form>
                </div>

                <div className="flex flex-col divide-y divide-[hsl(var(--border))]/60">
                  {panel.state.servers.length === 0 ? (
                    <div className="p-10 text-center text-[13px] text-[hsl(var(--muted-foreground))]">暂无节点</div>
                  ) : (
                    panel.state.servers.map((server) => {
                      const isCurrent = server.current;
                      const isTesting = panel.testing.has(server.raw);
                      const test = server.test;

                      return (
                        <div key={server.raw} className={`p-4 flex flex-col gap-3 transition-colors hover:bg-[hsl(var(--muted))]/40 ${isCurrent ? "bg-[hsl(var(--ok))]/[0.03]" : ""}`}>
                          <div className="flex flex-col sm:flex-row sm:items-center justify-between gap-3">
                            <div>
                              <div className="font-mono text-[13px] font-medium mb-2 break-all text-[hsl(var(--foreground))]">{server.raw}</div>
                              <div className="flex flex-wrap gap-2">
                                {isCurrent ? <Chip active>运行中</Chip> : server.default ? <Chip>默认</Chip> : <Chip>备用</Chip>}
                                <Chip>{server.hasAuth ? `鉴权: ${server.username || "已配置"}` : "无鉴权"}</Chip>
                                {isTesting && <Chip warn>测试中...</Chip>}
                              </div>
                            </div>
                            <div className="flex flex-wrap items-center gap-2 sm:justify-end">
                              <Button disabled={panel.busy || isTesting} onClick={() => panel.testServer(server.raw)}>测试</Button>
                              {!isCurrent && (
                                <Button variant="small" disabled={panel.busy} onClick={() => panel.run(() => postJSON<ApiResponse>("/api/servers/select", { server: server.raw }))}>切换</Button>
                              )}
                              <Button variant="danger" disabled={panel.busy} onClick={() => window.confirm("确定要删除此节点吗？") && panel.run(() => postJSON<ApiResponse>("/api/servers/delete", { server: server.raw }))}>删除</Button>
                            </div>
                          </div>

                          {test && !isTesting && (
                            <div className="rounded-[var(--radius)] border border-[hsl(var(--border))]/60 bg-[hsl(var(--background))]/50 p-3 mt-1 shadow-sm">
                              {test.ok ? (
                                <div className="grid grid-cols-2 sm:grid-cols-5 gap-y-4 gap-x-4 text-[12px] font-mono">
                                  <div className="flex flex-col gap-1.5">
                                    <span className="text-[10px] uppercase text-[hsl(var(--muted-foreground))] tracking-wider">评分</span>
                                    <span className="font-medium text-[hsl(var(--ok))]">{test.score || 0}/100</span>
                                  </div>
                                  <div className="flex flex-col gap-1.5">
                                    <span className="text-[10px] uppercase text-[hsl(var(--muted-foreground))] tracking-wider">TCP / UDP</span>
                                    <span className="text-[hsl(var(--foreground))]">{test.tcpConnect?.ok ? ms(test.tcpConnect.avgMs) : "失败"} <span className="text-[hsl(var(--border))]">/</span> {test.socksUdp?.ok ? "正常" : "失败"}</span>
                                  </div>
                                  <div className="flex flex-col gap-1.5">
                                    <span className="text-[10px] uppercase text-[hsl(var(--muted-foreground))] tracking-wider">单线</span>
                                    <span className="text-[hsl(var(--foreground))]">{test.singleThread?.ok ? mbps(test.singleThread.mbps) : "失败"}</span>
                                  </div>
                                  <div className="flex flex-col gap-1.5">
                                    <span className="text-[10px] uppercase text-[hsl(var(--muted-foreground))] tracking-wider">多线</span>
                                    <span className="text-[hsl(var(--foreground))]">{test.multiThread?.ok ? mbps(test.multiThread.mbps) : "失败"}</span>
                                  </div>
                                  <div className="flex flex-col gap-1.5">
                                    <span className="text-[10px] uppercase text-[hsl(var(--muted-foreground))] tracking-wider">已测</span>
                                    <span className="text-[hsl(var(--foreground))]">{formatTestTime(test.testedAt) || "-"}</span>
                                  </div>
                                </div>
                              ) : (
                                <div className="text-[12px] text-[hsl(var(--danger))] font-medium">
                                  {test.message || "测试失败"}
                                </div>
                              )}
                            </div>
                          )}
                        </div>
                      );
                    })
                  )}
                </div>
              </div>
            </WindowSection>
          </div>

          <aside className="flex flex-col gap-8">
            <div>
              <div className="text-[11px] uppercase tracking-widest text-[hsl(var(--muted-foreground))] mb-3 font-medium">概览</div>
              <div className="flex flex-col gap-4 text-[13px]">
                <div className="flex justify-between border-b border-[hsl(var(--border))]/80 pb-2.5">
                  <span className="text-[hsl(var(--muted-foreground))] font-medium">服务 PID</span>
                  <span className="font-mono text-[hsl(var(--foreground))]">{panel.state.service.pid || "-"}</span>
                </div>
              </div>
            </div>

            <WindowSection header="配置">
              <form className="flex flex-col gap-5 p-5 bg-[hsl(var(--card))]" onSubmit={updateConfig}>
                <label className={labelClass}>
                  <span className={labelTextClass}>SOCKS5 监听</span>
                  <Input type="text" value={panel.config.listen} onChange={(event) => updateConfigField("listen", event.target.value)} />
                </label>
                <label className={labelClass}>
                  <span className={labelTextClass}>DoH</span>
                  <Input type="text" value={panel.config.doh} onChange={(event) => updateConfigField("doh", event.target.value)} />
                </label>
                <div className="pt-2 border-t border-[hsl(var(--border))]/60">
                  <label className="flex items-center gap-2.5 cursor-pointer mt-1">
                    <input type="checkbox" checked={panel.config.panelAuthEnabled} onChange={(event) => updateConfigField("panelAuthEnabled", event.target.checked)} className="h-[14px] w-[14px] rounded-[3px] border-[hsl(var(--primary))] text-[hsl(var(--primary))] focus:ring-[hsl(var(--primary))] transition-colors" />
                    <span className={labelTextClass}>面板登录</span>
                  </label>
                </div>
                {panel.config.panelAuthEnabled && (
                  <div className="flex flex-col gap-4 pl-4 border-l-2 border-[hsl(var(--border))]/80 mt-1">
                    <label className={labelClass}>
                      <span className={labelTextClass}>用户名</span>
                      <Input type="text" value={panel.config.panelUsername} onChange={(event) => updateConfigField("panelUsername", event.target.value)} />
                    </label>
                    <label className={labelClass}>
                      <span className={labelTextClass}>密码</span>
                      <Input type="password" placeholder="留空不修改" value={panel.config.panelPassword} onChange={(event) => updateConfigField("panelPassword", event.target.value)} />
                    </label>
                  </div>
                )}
                <Button variant="primary" className="mt-3 min-h-[36px]" disabled={panel.busy} type="submit">保存配置</Button>
              </form>
            </WindowSection>
          </aside>
        </div>
      </div>

      <div className={`fixed bottom-8 left-1/2 -translate-x-1/2 z-50 flex items-center gap-3 rounded-full border border-[hsl(var(--border))] bg-[hsl(var(--card))]/95 backdrop-blur-md px-5 py-2.5 text-[13px] font-medium text-[hsl(var(--foreground))] shadow-[0_12px_40px_-8px_rgba(0,0,0,0.12)] transition-all duration-300 ${panel.toast ? "translate-y-0 opacity-100 scale-100" : "translate-y-4 opacity-0 scale-95 pointer-events-none"}`}>
        {panel.toast}
      </div>
    </div>
  );
}

export default App;
