import type { FormEvent } from "react";
import { Chip } from "../../components/Chip";
import { inputClass, primaryButtonClass, smallButtonClass, topButtonClass } from "../../controlClasses";
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

export function NodePanel({ state, serverInput, testing, busy, locked, onServerInput, onAddServer, onTestServer, onTestAll, onSelectServer, onDeleteServer }: Props) {
  const currentServer = state.servers.find((server) => server.current) || state.servers[0];

  return (
    <div className="flex flex-col gap-5">
      <section className="shell-window relative flex min-h-[178px] flex-col justify-center overflow-hidden p-6 md:p-8">
        <div className="pointer-events-none absolute inset-0 opacity-65" style={{ background: "linear-gradient(120deg, hsl(var(--primary) / .08), transparent 42%), repeating-linear-gradient(90deg, transparent 0 21px, hsl(var(--border) / .35) 22px)" }} />
        <div className="relative z-10">
          <div className="mb-3 font-mono text-[11px] uppercase tracking-[0.22em] text-[hsl(var(--muted-foreground))]">当前 TURN 节点</div>
          <div className="mb-5 break-all text-[26px] font-medium leading-[1.12] tracking-tight text-[hsl(var(--foreground))] sm:text-[36px] md:text-[42px]">
            {displayHost(currentServer)}
          </div>
          <div className="flex flex-wrap gap-2">
            <Chip>{currentServer?.hasAuth ? "鉴权已配置" : "无鉴权"}</Chip>
            <Chip>{state.listen || "-"}</Chip>
            <Chip>{state.servers.length} 个节点</Chip>
          </div>
        </div>
      </section>

      <section className="shell-window flex flex-col overflow-hidden">
        <div className="flex min-h-[44px] items-center justify-between border-b border-[hsl(var(--border))] bg-[hsl(var(--muted))]/70 px-4">
          <strong className="text-sm font-semibold text-[hsl(var(--foreground))]">节点管理</strong>
          <div className="flex items-center gap-2">
            <button className={topButtonClass} disabled={locked} onClick={onTestAll} type="button">测试全部节点</button>
            <Chip>首个节点为默认</Chip>
          </div>
        </div>

        <div className="p-4 md:p-[18px]">
          <form className="mb-5 border-b border-[hsl(var(--border))] pb-5" onSubmit={onAddServer}>
            <div className="mb-2 font-mono text-[11px] uppercase tracking-[0.12em] text-[hsl(var(--muted-foreground))]">添加 TURN 节点</div>
            <div className="flex flex-col gap-2.5 sm:flex-row">
              <input type="text" placeholder="host:port 或 user:pass@host:port" value={serverInput} onChange={(event) => onServerInput(event.target.value)} className={`${inputClass} flex-1`} />
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
                        <Chip>{server.hasAuth ? `鉴权：${server.username || "已配置"}` : "无鉴权"}</Chip>
                        {isTesting && <Chip warn><span className="animate-pulse">测试中</span></Chip>}
                      </div>
                    </div>
                    <div className="flex shrink-0 items-center justify-end gap-2">
                      <button disabled={busy || isTesting} onClick={() => onTestServer(server.raw)} className={smallButtonClass} type="button">测试</button>
                      {!isCurrent && (
                        <button disabled={busy} onClick={() => onSelectServer(server.raw)} className={`${primaryButtonClass} h-[34px] whitespace-nowrap px-[13px]`} type="button">切换</button>
                      )}
                      <button disabled={busy} onClick={() => onDeleteServer(server.raw)} className="h-[34px] cursor-pointer whitespace-nowrap rounded-full border border-transparent px-[13px] text-[13px] font-medium text-[hsl(var(--danger))] transition-colors hover:bg-[hsl(var(--danger))]/10 disabled:cursor-wait disabled:opacity-55" type="button">删除</button>
                    </div>
                  </div>

                  {test && !isTesting && (
                    <div className={`overflow-x-auto border-t border-[hsl(var(--border))]/60 px-4 py-3 font-mono text-[12px] leading-relaxed ${test.ok ? "bg-[hsl(var(--muted))]/30" : "bg-[hsl(var(--danger))]/10"}`}>
                      {test.ok ? (
                        <div className="flex flex-col gap-x-6 gap-y-2 text-[hsl(var(--foreground))] sm:flex-row sm:flex-wrap sm:items-center">
                          <span><span className="text-[hsl(var(--muted-foreground))]">综合评分：</span><b className="text-[hsl(var(--ok))]">{test.score || 0}</b></span>
                          <span className="hidden text-[hsl(var(--border))] sm:block">|</span>
                          <span><span className="text-[hsl(var(--muted-foreground))]">测试时间：</span>{formatTestTime(test.testedAt)}</span>
                          <span className="hidden text-[hsl(var(--border))] sm:block">|</span>
                          <span><span className="text-[hsl(var(--muted-foreground))]">TCP 延迟：</span>{test.tcpConnect?.ok ? ms(test.tcpConnect.avgMs) : "失败"}</span>
                          <span className="hidden text-[hsl(var(--border))] sm:block">|</span>
                          <span><span className="text-[hsl(var(--muted-foreground))]">UDP 转发：</span><span className={test.socksUdp?.ok ? "text-[hsl(var(--ok))]" : "text-[hsl(var(--danger))]"}>{test.socksUdp?.ok ? "可用" : "失败"}</span></span>
                          <span className="hidden text-[hsl(var(--border))] sm:block">|</span>
                          <span><span className="text-[hsl(var(--muted-foreground))]">单线程：</span>{test.singleThread?.ok ? mbps(test.singleThread.mbps) : "失败"}</span>
                          <span className="hidden text-[hsl(var(--border))] sm:block">|</span>
                          <span><span className="text-[hsl(var(--muted-foreground))]">多线程：</span>{test.multiThread?.ok ? mbps(test.multiThread.mbps) : "失败"}</span>
                        </div>
                      ) : (
                        <div className="flex items-center gap-2 text-[hsl(var(--danger))]">
                          <b>{test.message || "测试失败"}</b>
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
  );
}
