import type { FormEvent } from "react";
import { Chip } from "../../components/Chip";
import { IconAlert, IconPlus, IconTrash, IconZap } from "../../components/icons";
import { iconDangerButtonClass, inputClass, primaryButtonClass, smallButtonClass, softButtonClass } from "../../controlClasses";
import { displayHost, formatTestTime, mbps, ms } from "../../lib/format";
import type { PanelState } from "../../types/panel";

type Props = {
  state: PanelState;
  serverInput: string;
  testing: Set<string>;
  busy: boolean;
  locked: boolean;
  onServerInput: (value: string) => void;
  onAddServer: (event: FormEvent) => void;
  onTestServer: (server: string) => void;
  onTestAll: () => void;
  onSelectServer: (server: string) => void;
  onDeleteServer: (server: string) => void;
};

const toneText: Record<string, string> = {
  ok: "text-[hsl(var(--ok))]",
  warn: "text-[hsl(var(--warn))]",
  danger: "text-[hsl(var(--danger))]"
};

function latencyTone(avgMs?: number) {
  if (!Number.isFinite(avgMs)) return "danger";
  return avgMs! <= 80 ? "ok" : avgMs! <= 160 ? "warn" : "danger";
}

export function NodePanel({ state, serverInput, testing, busy, locked, onServerInput, onAddServer, onTestServer, onTestAll, onSelectServer, onDeleteServer }: Props) {
  const currentServer = state.servers.find((server) => server.current) || state.servers[0];

  return (
    <div className="flex flex-col gap-5">
      <section className="shell-window relative overflow-hidden p-6 md:p-7">
        <div className="pointer-events-none absolute inset-0" style={{ background: "radial-gradient(460px at 94% -60%, hsl(var(--primary) / 0.08), transparent 65%)" }} />
        <div className="relative">
          <div className="text-[12.5px] font-medium text-[hsl(var(--muted-foreground))]">当前 TURN 节点</div>
          <div className="mb-4 mt-1.5 break-all font-mono text-[24px] font-semibold leading-[1.2] text-[hsl(var(--foreground))] sm:text-[28px] md:text-[30px]">
            {currentServer ? displayHost(currentServer) : "暂无节点"}
          </div>
          <div className="flex flex-wrap gap-[7px]">
            {currentServer && <Chip>{currentServer.hasAuth ? "鉴权已配置" : "无鉴权"}</Chip>}
            <Chip mono>{state.listen || "-"}</Chip>
            <Chip>{state.servers.length} 个节点</Chip>
          </div>
        </div>
      </section>

      <section className="shell-window overflow-hidden">
        <div className="flex items-center justify-between gap-3 border-b border-[hsl(var(--border))] px-4 py-3.5 md:px-[18px]">
          <h2 className="text-[14.5px] font-semibold text-[hsl(var(--foreground))]">节点管理</h2>
          <button className={smallButtonClass} disabled={locked} onClick={onTestAll} type="button">
            <IconZap className="h-3.5 w-3.5" />
            测试全部
          </button>
        </div>

        <div className="p-4 md:p-[18px]">
          <form className="mb-4 flex flex-col gap-2.5 sm:flex-row" onSubmit={onAddServer}>
            <input type="text" placeholder="host:port 或 user:pass@host:port" value={serverInput} onChange={(event) => onServerInput(event.target.value)} className={`${inputClass} flex-1`} />
            <button className={`${primaryButtonClass} min-h-[38px] px-4`} disabled={busy} type="submit">
              <IconPlus className="h-3.5 w-3.5" />
              添加节点
            </button>
          </form>

          <div className="grid gap-3">
            {state.servers.map((server) => {
              const isCurrent = server.current;
              const isTesting = testing.has(server.raw);
              const test = server.test;
              const tcpTone = test?.tcpConnect?.ok ? latencyTone(test.tcpConnect.avgMs) : "danger";
              return (
                <article key={server.raw} className={`rounded-xl border p-[14px] transition-shadow md:px-4 ${isCurrent ? "border-[hsl(var(--primary))]/45 bg-[hsl(var(--primary))]/[0.03]" : "border-[hsl(var(--border))] hover:border-[hsl(var(--input))] hover:shadow-md"}`}>
                  <div className="flex flex-col justify-between gap-3 sm:flex-row sm:items-start">
                    <div className="min-w-0">
                      <div className="break-all font-mono text-[13px] leading-[1.5] text-[hsl(var(--foreground))]">{server.raw}</div>
                      <div className="mt-2 flex flex-wrap gap-[7px]">
                        {isCurrent ? <Chip active>当前</Chip> : server.default ? <Chip>默认</Chip> : <Chip>备用</Chip>}
                        <Chip>{server.hasAuth ? `鉴权：${server.username || "已配置"}` : "无鉴权"}</Chip>
                        {isTesting && <Chip warn><span className="animate-pulse">测试中</span></Chip>}
                      </div>
                    </div>
                    <div className="flex shrink-0 items-center gap-[7px] sm:justify-end">
                      <button disabled={busy || isTesting} onClick={() => onTestServer(server.raw)} className={smallButtonClass} type="button">测试</button>
                      {!isCurrent && (
                        <button disabled={busy} onClick={() => onSelectServer(server.raw)} className={softButtonClass} type="button">切换</button>
                      )}
                      <button disabled={busy} onClick={() => onDeleteServer(server.raw)} className={iconDangerButtonClass} aria-label="删除" title="删除" type="button">
                        <IconTrash className="h-4 w-4" />
                      </button>
                    </div>
                  </div>

                  {test && !isTesting && (
                    test.ok ? (
                      <div className="mt-3 grid grid-cols-2 gap-x-5 gap-y-3 border-t border-[hsl(var(--border))] pt-3 sm:grid-cols-3 xl:grid-cols-5">
                        <div className="min-w-0">
                          <div className="mb-1 text-[11px] text-[hsl(var(--muted-foreground))]">TCP 延迟</div>
                          <div className={`font-mono text-[13px] font-semibold ${test.tcpConnect?.ok ? toneText[tcpTone] : "text-[hsl(var(--danger))]"}`}>{test.tcpConnect?.ok ? ms(test.tcpConnect.avgMs) : "失败"}</div>
                        </div>
                        <div className="min-w-0">
                          <div className="mb-1 text-[11px] text-[hsl(var(--muted-foreground))]">UDP 转发</div>
                          <div className={`font-mono text-[13px] font-semibold ${test.socksUdp?.ok ? "text-[hsl(var(--ok))]" : "text-[hsl(var(--danger))]"}`}>{test.socksUdp?.ok ? "可用" : "失败"}</div>
                        </div>
                        <div className="min-w-0">
                          <div className="mb-1 text-[11px] text-[hsl(var(--muted-foreground))]">单线程</div>
                          <div className={`font-mono text-[13px] font-semibold ${test.singleThread?.ok ? "text-[hsl(var(--foreground))]" : "text-[hsl(var(--danger))]"}`}>{test.singleThread?.ok ? mbps(test.singleThread.mbps) : "失败"}</div>
                        </div>
                        <div className="min-w-0">
                          <div className="mb-1 text-[11px] text-[hsl(var(--muted-foreground))]">多线程</div>
                          <div className={`font-mono text-[13px] font-semibold ${test.multiThread?.ok ? "text-[hsl(var(--foreground))]" : "text-[hsl(var(--danger))]"}`}>{test.multiThread?.ok ? mbps(test.multiThread.mbps) : "失败"}</div>
                        </div>
                        <div className="min-w-0">
                          <div className="mb-1 text-[11px] text-[hsl(var(--muted-foreground))]">测试时间</div>
                          <div className="whitespace-nowrap font-mono text-[12px] text-[hsl(var(--foreground))]">{formatTestTime(test.testedAt)}</div>
                        </div>
                      </div>
                    ) : (
                      <div className="mt-3 flex flex-wrap items-center gap-x-2.5 gap-y-1 rounded-[10px] bg-[hsl(var(--danger))]/[0.08] px-3 py-2.5 text-[hsl(var(--danger))]">
                        <IconAlert className="h-[15px] w-[15px] flex-none" />
                        <span className="min-w-0 break-all text-[12.5px] font-medium">{test.message || "测试失败"}</span>
                        <span className="ml-auto whitespace-nowrap font-mono text-[11.5px] opacity-70">{formatTestTime(test.testedAt)}</span>
                      </div>
                    )
                  )}
                </article>
              );
            })}
            {state.servers.length === 0 && (
              <div className="rounded-xl border border-dashed border-[hsl(var(--border))] p-6 text-center text-[13px] text-[hsl(var(--muted-foreground))]">暂无节点</div>
            )}
          </div>
        </div>
      </section>
    </div>
  );
}
