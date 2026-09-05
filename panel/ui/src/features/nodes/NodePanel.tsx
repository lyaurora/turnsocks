import { useEffect, useRef, useState, type FormEvent } from "react";
import { Chip } from "../../components/Chip";
import { IconAlert, IconEdit, IconPlus, IconTrash, IconZap } from "../../components/icons";
import { iconDangerButtonClass, inputClass, primaryButtonClass, smallButtonClass, softButtonClass, topButtonClass } from "../../controlClasses";
import { displayHost, displayPort, formatTestTime, mbps, ms } from "../../lib/format";
import type { ActiveProbe, PanelState, ProbeMode, ServerInfo } from "../../types/panel";

type Props = {
  state: PanelState;
  serverInput: string;
  testing: ActiveProbe | null;
  locked: boolean;
  onServerInput: (value: string) => void;
  onAddServer: (event: FormEvent) => void;
  onTestServer: (server: string, mode: ProbeMode) => void;
  onTestAll: (mode: ProbeMode) => void;
  onStopTesting: () => void;
  onSelectServer: (server: string) => void;
  onDeleteServer: (server: string) => void;
  onUpdateNote: (server: string, note: string) => Promise<boolean>;
};

const toneText: Record<string, string> = {
  ok: "text-[hsl(var(--ok))]",
  warn: "text-[hsl(var(--warn))]",
  danger: "text-[hsl(var(--danger))]",
  muted: "text-[hsl(var(--muted-foreground))]"
};

function latencyTone(avgMs?: number) {
  if (!Number.isFinite(avgMs)) return "danger";
  return avgMs! <= 80 ? "ok" : avgMs! <= 160 ? "warn" : "danger";
}

function Metric({ label, value, unit, tooltip, valueClass = "text-[13px] font-semibold text-[hsl(var(--foreground))]" }: { label: string; value: string; unit?: string; tooltip?: string; valueClass?: string }) {
  return (
    <div className={`min-w-0 ${tooltip ? "ui-tooltip [--tooltip-max-width:100%]" : ""}`} data-tooltip={tooltip || undefined} data-tooltip-side="top" tabIndex={tooltip ? 0 : undefined}>
      <div className="mb-1 text-[11px] text-[hsl(var(--muted-foreground))]">{label}</div>
      <div className={`font-mono ${valueClass}`}>
        {value}
        {unit && <span className="ml-1 text-[11px] font-normal text-[hsl(var(--muted-foreground))]">{unit}</span>}
      </div>
    </div>
  );
}

function NodeResults({ server, dim }: { server: ServerInfo; dim?: boolean }) {
  // Results saved before the two probe modes include connectivity in the speed result.
  const check = server.check || (server.test?.mode ? undefined : server.test);
  const latest = (Date.parse(server.check?.testedAt || "") || 0) >= (Date.parse(server.test?.testedAt || "") || 0)
    ? server.check || server.test
    : server.test;
  if (!latest) {
    return <div className="text-[12px] text-[hsl(var(--muted-foreground))]">{dim ? "正在检测…" : "尚未检测，点击“检查”验证连通性，或“测速”测量带宽"}</div>;
  }
  const tcpTone = !check ? "muted" : check.tcpConnect?.ok ? latencyTone(check.tcpConnect.avgMs) : "danger";
  const checkTime = check?.testedAt ? `检查时间：${formatTestTime(check.testedAt)}` : undefined;
  const checkError = [
    { label: "TCP 转发", result: check?.socksTcp },
    { label: "UDP 转发", result: check?.socksUdp }
  ].filter(({ result }) => result && !result.ok).map(({ label, result }) => `${label}：${result?.message || "检查失败"}`).join("；")
    || (server.check && !server.check.ok ? server.check.message || "检查失败" : "");
  const failures = [
    { message: checkError, testedAt: check?.testedAt },
    { message: server.test && (!server.test.ok || !server.test.singleThread?.ok || !server.test.multiThread?.ok) ? server.test.message || "测速失败" : "", testedAt: server.test?.testedAt }
  ];
  return (
    <div className={`space-y-3 transition-opacity ${dim ? "opacity-45" : ""}`}>
      <div className="grid grid-cols-2 gap-x-5 gap-y-3 sm:grid-cols-3 xl:grid-cols-5">
        <Metric label="TCP 延迟" value={!check ? "未检查" : check.tcpConnect?.ok ? ms(check.tcpConnect.avgMs) : "失败"} tooltip={checkTime} valueClass={`text-[13px] font-semibold ${toneText[tcpTone]}`} />
        <Metric label="UDP 转发" value={!check ? "未检查" : check.socksUdp?.ok ? "可用" : "失败"} tooltip={checkTime} valueClass={`text-[13px] font-semibold ${toneText[!check ? "muted" : check.socksUdp?.ok ? "ok" : "danger"]}`} />
        {[
          { label: "单线程", speed: server.test?.singleThread },
          { label: "多线程", speed: server.test?.multiThread }
        ].map(({ label, speed }) => {
          const measured = speed?.ok || (speed?.bytes || 0) > 0;
          const incomplete = measured && !speed?.ok;
          return (
            <Metric key={label} label={incomplete ? `${label}（未完成）` : label} value={measured ? mbps(speed?.mbps) : server.test ? "失败" : "未测速"} unit={measured ? "Mbps" : undefined} tooltip={[speed?.message, server.test?.testedAt && `测速时间：${formatTestTime(server.test.testedAt)}`].filter(Boolean).join("\n")} valueClass={!server.test ? "text-[13px] text-[hsl(var(--muted-foreground))]" : speed?.ok ? undefined : `text-[13px] font-semibold ${toneText[incomplete ? "warn" : "danger"]}`} />
          );
        })}
        <Metric label="测试时间" value={formatTestTime(latest.testedAt)} valueClass="whitespace-nowrap text-[12px] text-[hsl(var(--foreground))]" />
      </div>
      {failures.filter(({ message }) => message).map(({ message, testedAt }, index) => (
        <div key={index} className="grid grid-cols-[15px_minmax(0,1fr)] items-center gap-x-2.5 gap-y-1 rounded-[9px] bg-[hsl(var(--danger))]/[0.08] px-3 py-2.5 text-[hsl(var(--danger))] sm:grid-cols-[15px_minmax(0,1fr)_auto]">
          <IconAlert className="h-[15px] w-[15px] flex-none" />
          <span className="min-w-0 break-all text-[12px] font-medium">{message}</span>
          <span className="col-start-2 whitespace-nowrap text-right font-mono text-[11px] opacity-70 sm:col-start-auto">{formatTestTime(testedAt)}</span>
        </div>
      ))}
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

export function NodePanel({ state, serverInput, testing, locked, onServerInput, onAddServer, onTestServer, onTestAll, onStopTesting, onSelectServer, onDeleteServer, onUpdateNote }: Props) {
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
      <section className="shell-window relative p-6 md:p-7">
        <div className="pointer-events-none absolute inset-0 rounded-[inherit]" style={{ background: "radial-gradient(460px at 94% -60%, hsl(var(--primary) / 0.08), transparent 65%)" }} />
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
          {currentServer && <NodeResults server={currentServer} dim={testing?.server === currentServer.raw} />}
        </div>
      </section>

      <section className="shell-window">
        <div className="flex flex-wrap items-center justify-between gap-3 border-b border-[hsl(var(--border))] px-4 py-3.5 md:px-[18px]">
          <h2 className="text-[14.5px] font-semibold text-[hsl(var(--foreground))]">节点管理</h2>
          <div className="flex flex-wrap gap-2">
            {testing && <button className={smallButtonClass} onClick={onStopTesting} type="button">停止检测</button>}
            <button className={`${smallButtonClass} ui-tooltip`} disabled={locked || !state.servers.length} onClick={() => onTestAll("check")} aria-label="检查全部" data-tooltip="检查延迟与连通性" type="button">检查全部</button>
            <button className={`${smallButtonClass} ui-tooltip`} disabled={locked || !state.servers.length} onClick={() => onTestAll("speed")} aria-label="测速全部" data-tooltip="约 112 MiB / 节点" type="button">
              <IconZap className="h-3.5 w-3.5" />
              测速全部
            </button>
          </div>
        </div>

        <div className="p-4 md:p-[18px]">
          <form className="mb-4 flex flex-col gap-2.5 sm:flex-row" onSubmit={onAddServer}>
            <input type="text" placeholder="host:port 或 user:pass@host:port" value={serverInput} onChange={(event) => onServerInput(event.target.value)} className={`${inputClass} flex-1`} />
            <button className={`${primaryButtonClass} min-h-[38px] px-4`} disabled={locked} type="submit">
              <IconPlus className="h-3.5 w-3.5" />
              添加节点
            </button>
          </form>

          <div className="grid gap-3">
            {state.servers.map((server) => {
              const isCurrent = server.current;
              const isTesting = testing?.server === server.raw;
              return (
                <article key={server.raw} className={`rounded-xl border p-[14px] transition-shadow md:px-4 ${isCurrent ? "border-[hsl(var(--primary))]/45 bg-[hsl(var(--primary))]/[0.03]" : "border-[hsl(var(--border))] hover:border-[hsl(var(--input))] hover:shadow-md"}`}>
                  <div className="flex flex-col justify-between gap-3 sm:flex-row sm:items-start">
                    <div className="min-w-0">
                      <div className="break-all font-mono text-[13px] leading-[1.5] text-[hsl(var(--foreground))]">{server.raw}</div>
                      <div className="mt-2 flex flex-wrap gap-[7px]">
                        {isCurrent ? <Chip active>当前</Chip> : server.default ? <Chip>默认</Chip> : <Chip>备用</Chip>}
                        <Chip>{server.hasAuth ? `鉴权：${server.username || "已配置"}` : "无鉴权"}</Chip>
                        {server.note && <NoteChip note={server.note} />}
                        <button disabled={locked} onClick={() => editNote(server.raw, server.note)} className="ui-tooltip inline-grid h-6 w-7 flex-none cursor-pointer place-items-center rounded-[7px] border border-[hsl(var(--border))] bg-[hsl(var(--muted))] text-[hsl(var(--muted-foreground))] transition-colors before:absolute before:-inset-2.5 hover:border-[hsl(var(--input))] hover:text-[hsl(var(--foreground))] disabled:cursor-wait disabled:opacity-55" aria-label={server.note ? "修改备注" : "添加备注"} data-tooltip={server.note ? "修改备注" : "添加备注"} type="button">
                          {server.note ? <IconEdit className="h-3 w-3" /> : <IconPlus className="h-3 w-3" />}
                        </button>
                        {isTesting && <Chip warn><span className="animate-pulse motion-reduce:animate-none">{testing.mode === "check" ? "检查中" : "测速中"}</span></Chip>}
                      </div>
                    </div>
                    <div className="flex shrink-0 items-center gap-[7px] sm:justify-end">
                      <button disabled={locked} onClick={() => onTestServer(server.raw, "check")} className={`${smallButtonClass} ui-tooltip`} aria-label="检查" data-tooltip="延迟与连通性检查" type="button">检查</button>
                      <button disabled={locked} onClick={() => onTestServer(server.raw, "speed")} className={`${smallButtonClass} ui-tooltip`} aria-label="测速" data-tooltip="仅测带宽，约 112 MiB" type="button">测速</button>
                      {!isCurrent && (
                        <button disabled={locked} onClick={() => onSelectServer(server.raw)} className={softButtonClass} type="button">切换</button>
                      )}
                      <button disabled={locked} onClick={() => onDeleteServer(server.raw)} className={`${iconDangerButtonClass} ui-tooltip`} aria-label="删除" data-tooltip="删除" data-tooltip-side="top" type="button">
                        <IconTrash className="h-4 w-4" />
                      </button>
                    </div>
                  </div>

                  <div className="mt-3 border-t border-[hsl(var(--border))] pt-3">
                    <NodeResults server={server} dim={isTesting} />
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
            <button className={`${primaryButtonClass} h-[34px] px-[13px]`} disabled={locked} type="submit">保存备注</button>
          </div>
        </form>
      </dialog>
    </div>
  );
}
