import { useEffect, useRef, useState, type FormEvent } from "react";
import { Chip } from "../../components/Chip";
import { IconAlert, IconEdit, IconPlus, IconTrash, IconZap } from "../../components/icons";
import { iconDangerButtonClass, inputClass, primaryButtonClass, smallButtonClass, softButtonClass, topButtonClass } from "../../controlClasses";
import { displayHost, displayPort, formatTestTime, mbps, ms } from "../../lib/format";
import type { PanelState, ServerTest } from "../../types/panel";

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
  onUpdateNote: (server: string, note: string) => Promise<boolean>;
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

function Metric({ label, value, unit, valueClass = "text-[13px] font-semibold text-[hsl(var(--foreground))]" }: { label: string; value: string; unit?: string; valueClass?: string }) {
  return (
    <div className="min-w-0">
      <div className="mb-1 text-[11px] text-[hsl(var(--muted-foreground))]">{label}</div>
      <div className={`font-mono ${valueClass}`}>
        {value}
        {unit && <span className="ml-1 text-[11px] font-normal text-[hsl(var(--muted-foreground))]">{unit}</span>}
      </div>
    </div>
  );
}

function TestMetrics({ test, dim }: { test: ServerTest; dim?: boolean }) {
  const tcpTone = test.tcpConnect?.ok ? latencyTone(test.tcpConnect.avgMs) : "danger";
  return (
    <div className={`grid grid-cols-2 gap-x-5 gap-y-3 transition-opacity sm:grid-cols-3 xl:grid-cols-5 ${dim ? "opacity-45" : ""}`}>
      <Metric label="TCP 延迟" value={test.tcpConnect?.ok ? ms(test.tcpConnect.avgMs) : "失败"} valueClass={`text-[13px] font-semibold ${test.tcpConnect?.ok ? toneText[tcpTone] : "text-[hsl(var(--danger))]"}`} />
      <Metric label="UDP 转发" value={test.socksUdp?.ok ? "可用" : "失败"} valueClass={`text-[13px] font-semibold ${test.socksUdp?.ok ? "text-[hsl(var(--ok))]" : "text-[hsl(var(--danger))]"}`} />
      <Metric label="单线程" value={test.singleThread?.ok ? mbps(test.singleThread.mbps) : "失败"} unit={test.singleThread?.ok ? "Mbps" : undefined} valueClass={test.singleThread?.ok ? undefined : "text-[13px] font-semibold text-[hsl(var(--danger))]"} />
      <Metric label="多线程" value={test.multiThread?.ok ? mbps(test.multiThread.mbps) : "失败"} unit={test.multiThread?.ok ? "Mbps" : undefined} valueClass={test.multiThread?.ok ? undefined : "text-[13px] font-semibold text-[hsl(var(--danger))]"} />
      <Metric label="测试时间" value={formatTestTime(test.testedAt)} valueClass="whitespace-nowrap text-[12px] text-[hsl(var(--foreground))]" />
    </div>
  );
}

function TestFailure({ test, dim }: { test: ServerTest; dim?: boolean }) {
  return (
    <div className={`flex flex-wrap items-center gap-x-2.5 gap-y-1 rounded-[9px] bg-[hsl(var(--danger))]/[0.08] px-3 py-2.5 text-[hsl(var(--danger))] transition-opacity ${dim ? "opacity-45" : ""}`}>
      <IconAlert className="h-[15px] w-[15px] flex-none" />
      <span className="min-w-0 break-all text-[12px] font-medium">{test.message || "测试失败"}</span>
      <span className="ml-auto whitespace-nowrap font-mono text-[11px] opacity-70">{formatTestTime(test.testedAt)}</span>
    </div>
  );
}

function NoteChip({ note }: { note: string }) {
  return (
    <span className="ui-tooltip inline-flex min-w-0" data-tooltip={note} tabIndex={0}>
      <Chip accent><span className="block max-w-[180px] truncate sm:max-w-[260px]">{note}</span></Chip>
    </span>
  );
}

export function NodePanel({ state, serverInput, testing, busy, locked, onServerInput, onAddServer, onTestServer, onTestAll, onSelectServer, onDeleteServer, onUpdateNote }: Props) {
  const currentServer = state.servers.find((server) => server.current) || state.servers[0];
  const [editing, setEditing] = useState("");
  const [note, setNote] = useState("");
  const noteDialog = useRef<HTMLDialogElement>(null);
  const hasExistingNote = !!state.servers.find((server) => server.raw === editing)?.note;

  useEffect(() => {
    const dialog = noteDialog.current;
    if (!dialog) return;
    if (editing && !dialog.open) dialog.showModal();
    if (!editing && dialog.open) dialog.close();
  }, [editing]);

  function editNote(server: string, value?: string) {
    setEditing(server);
    setNote(value || "");
  }

  async function saveNote() {
    if (editing && await onUpdateNote(editing, note.trim())) setEditing("");
  }

  return (
    <div className="flex flex-col gap-5">
      <section className="shell-window relative overflow-hidden p-6 md:p-7">
        <div className="pointer-events-none absolute inset-0" style={{ background: "radial-gradient(460px at 94% -60%, hsl(var(--primary) / 0.08), transparent 65%)" }} />
        <div className="relative">
          <div className="text-[12px] font-medium text-[hsl(var(--muted-foreground))]">当前 TURN 节点</div>
          <div className="mb-3 mt-1.5 break-all font-mono text-[24px] font-semibold leading-[1.2] text-[hsl(var(--foreground))] sm:text-[28px] md:text-[30px]">
            {currentServer ? (
              <>
                {displayHost(currentServer)}
                {displayPort(currentServer) && <span className="text-[0.6em] font-medium text-[hsl(var(--muted-foreground))]">:{displayPort(currentServer)}</span>}
              </>
            ) : "暂无节点"}
          </div>
          {currentServer && (
            <div className="mb-4 flex flex-wrap gap-[7px]">
              {currentServer.current && (state.service.active
                ? <Chip active>正在使用</Chip>
                : <Chip warn>代理已停止</Chip>)}
              <Chip>{currentServer.hasAuth ? `鉴权：${currentServer.username || "已配置"}` : "无鉴权"}</Chip>
              {currentServer.note && <NoteChip note={currentServer.note} />}
            </div>
          )}
          {currentServer?.test ? (
            currentServer.test.ok
              ? <TestMetrics test={currentServer.test} dim={testing.has(currentServer.raw)} />
              : <TestFailure test={currentServer.test} dim={testing.has(currentServer.raw)} />
          ) : currentServer ? (
            <div className="text-[12px] text-[hsl(var(--muted-foreground))]">尚未测速，点击下方“测试”查看节点质量</div>
          ) : null}
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
              return (
                <article key={server.raw} className={`rounded-xl border p-[14px] transition-shadow md:px-4 ${isCurrent ? "border-[hsl(var(--primary))]/45 bg-[hsl(var(--primary))]/[0.03]" : "border-[hsl(var(--border))] hover:border-[hsl(var(--input))] hover:shadow-md"}`}>
                  <div className="flex flex-col justify-between gap-3 sm:flex-row sm:items-start">
                    <div className="min-w-0">
                      <div className="break-all font-mono text-[13px] leading-[1.5] text-[hsl(var(--foreground))]">{server.raw}</div>
                      <div className="mt-2 flex flex-wrap gap-[7px]">
                        {isCurrent ? <Chip active>当前</Chip> : server.default ? <Chip>默认</Chip> : <Chip>备用</Chip>}
                        <Chip>{server.hasAuth ? `鉴权：${server.username || "已配置"}` : "无鉴权"}</Chip>
                        {server.note && <NoteChip note={server.note} />}
                        <button disabled={busy} onClick={() => editNote(server.raw, server.note)} className="ui-tooltip inline-grid h-6 w-7 flex-none cursor-pointer place-items-center rounded-[7px] border border-[hsl(var(--border))] bg-[hsl(var(--muted))] text-[hsl(var(--muted-foreground))] transition-colors before:absolute before:-inset-2.5 hover:border-[hsl(var(--input))] hover:text-[hsl(var(--foreground))] disabled:cursor-wait disabled:opacity-55" aria-label={server.note ? "修改备注" : "添加备注"} data-tooltip={server.note ? "修改备注" : "添加备注"} type="button">
                          {server.note ? <IconEdit className="h-3 w-3" /> : <IconPlus className="h-3 w-3" />}
                        </button>
                        {isTesting && <Chip warn><span className="animate-pulse motion-reduce:animate-none">测试中</span></Chip>}
                      </div>
                    </div>
                    <div className="flex shrink-0 items-center gap-[7px] sm:justify-end">
                      <button disabled={busy || isTesting} onClick={() => onTestServer(server.raw)} className={smallButtonClass} type="button">测试</button>
                      {!isCurrent && (
                        <button disabled={busy} onClick={() => onSelectServer(server.raw)} className={softButtonClass} type="button">切换</button>
                      )}
                      <button disabled={busy || isTesting} onClick={() => onDeleteServer(server.raw)} className={`${iconDangerButtonClass} ui-tooltip`} aria-label="删除" data-tooltip="删除" data-tooltip-side="top" type="button">
                        <IconTrash className="h-4 w-4" />
                      </button>
                    </div>
                  </div>

                  <div className="mt-3 border-t border-[hsl(var(--border))] pt-3">
                    {test ? (
                      test.ok ? <TestMetrics test={test} dim={isTesting} /> : <TestFailure test={test} dim={isTesting} />
                    ) : (
                      <div className="text-[12px] text-[hsl(var(--muted-foreground))]">{isTesting ? "正在测试…" : "未测试"}</div>
                    )}
                  </div>
                </article>
              );
            })}
            {state.servers.length === 0 && (
              <div className="rounded-xl border border-dashed border-[hsl(var(--border))] p-6 text-center text-[13px] text-[hsl(var(--muted-foreground))]">暂无节点</div>
            )}
          </div>
        </div>
      </section>

      <dialog
        ref={noteDialog}
        aria-labelledby="note-dialog-title"
        className="m-auto w-[min(420px,calc(100%-32px))] rounded-[14px] border border-[hsl(var(--border))] bg-[hsl(var(--card))] p-0 text-[hsl(var(--foreground))] shadow-[0_20px_60px_rgba(0,0,0,.22)] backdrop:bg-black/40 backdrop:backdrop-blur-[2px]"
        onCancel={(event) => { event.preventDefault(); setEditing(""); }}
        onClose={() => setEditing("")}
        onClick={(event) => { if (event.target === event.currentTarget) setEditing(""); }}
      >
        <form className="p-5" onSubmit={(event) => { event.preventDefault(); void saveNote(); }}>
          <h2 id="note-dialog-title" className="text-[14.5px] font-semibold">{hasExistingNote ? "修改备注" : "添加备注"}</h2>
          <div className="mt-1 break-all font-mono text-[12px] text-[hsl(var(--muted-foreground))]">{editing}</div>
          <input autoFocus maxLength={60} value={note} onChange={(event) => setNote(event.target.value)} placeholder="输入节点备注" className={`${inputClass} mt-4 w-full font-sans`} />
          <div className="mt-5 flex justify-end gap-2">
            <button className={topButtonClass} onClick={() => setEditing("")} type="button">取消</button>
            <button className={`${primaryButtonClass} h-[34px] px-[13px]`} disabled={busy} type="submit">保存备注</button>
          </div>
        </form>
      </dialog>
    </div>
  );
}
