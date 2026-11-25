import { useEffect, useRef, useState, type ReactNode, type SubmitEvent } from "react";
import { Chip } from "../../components/Chip";
import { IconActivity, IconAlert, IconCheck, IconCopy, IconEdit, IconPlus, IconTrash, IconX, IconZap } from "../../components/icons";
import { iconButtonClass, iconDangerButtonClass, inputClass, primaryButtonClass, smallButtonClass, softButtonClass, topButtonClass } from "../../controlClasses";
import { displayHost, displayPort, formatBytesMiB, formatRelativeTime, formatTestTime, mbps, ms, msNum } from "../../lib/format";
import type { ActiveProbe, BusyAction, PanelState, ProbeMode, ServerInfo, SpeedMetric } from "../../types/panel";

type Props = {
  state: PanelState;
  serverInput: string;
  testing: ActiveProbe | null;
  busyAction?: BusyAction;
  locked: boolean;
  copiedKey?: string;
  gatewaySlot?: ReactNode;
  sideSlot?: ReactNode;
  onCopy?: (text: string, key: string, label: string) => void;
  onServerInput: (value: string) => void;
  onAddServer: (event: SubmitEvent<HTMLFormElement>) => void;
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

const anchorToneText: Record<string, string> = {
  ok: "text-[hsl(var(--anchor-accent))]",
  warn: "text-amber-300",
  danger: "text-red-300",
  muted: "text-[hsl(var(--anchor-muted))]"
};

function latencyTone(avgMs?: number) {
  if (!Number.isFinite(avgMs)) return "danger";
  return avgMs! <= 80 ? "ok" : avgMs! <= 160 ? "warn" : "danger";
}

function resolveProbe(server: ServerInfo) {
  // Results saved before the two probe modes include connectivity in the speed result.
  const check = server.check || (server.test?.mode ? undefined : server.test);
  const latest = (Date.parse(server.check?.testedAt || "") || 0) >= (Date.parse(server.test?.testedAt || "") || 0)
    ? server.check || server.test
    : server.test;
  const checkError = [
    { label: "TCP 转发", result: check?.socksTcp },
    { label: "UDP 转发", result: check?.socksUdp }
  ].filter(({ result }) => result && !result.ok)
    .map(({ label, result }) => `${label}：${result?.message || "检查失败"}`)
    .join("；")
    || (server.check && !server.check.ok ? server.check.message || "检查失败" : "");
  const speedError = server.test && (!server.test.ok || !server.test.singleThread?.ok || !server.test.multiThread?.ok)
    ? server.test.message || "测速失败"
    : "";
  const failures = [
    { message: checkError, testedAt: check?.testedAt },
    { message: speedError, testedAt: server.test?.testedAt }
  ].filter(({ message }) => !!message);
  return { check, latest, failures };
}

function speedDisplay(label: string, test: ServerInfo["test"], speed?: SpeedMetric) {
  const measured = Boolean(speed?.ok || (speed?.bytes || 0) > 0);
  const incomplete = measured && !speed?.ok;
  const title = incomplete ? `${label}（未完成）` : label;
  const value = measured ? mbps(speed?.mbps) : test ? "失败" : "未测速";
  const tone = !test ? "muted" : speed?.ok ? "" : incomplete ? "warn" : "danger";
  const tooltip = [speed?.message, test?.testedAt && `测速时间：${formatTestTime(test.testedAt)}`].filter(Boolean).join("\n") || undefined;
  return { measured, incomplete, title, value, tone, tooltip, rawMbps: measured && Number.isFinite(speed?.mbps) ? speed!.mbps! : 0 };
}

function FailureNotice({ message, testedAt, inverted }: { message: string; testedAt?: string; inverted?: boolean }) {
  return (
    <div
      data-probe-failure="true"
      className={`grid grid-cols-[15px_minmax(0,1fr)] items-center gap-x-2.5 gap-y-1 rounded-[10px] border px-3 py-2 sm:grid-cols-[15px_minmax(0,1fr)_auto] ${
        inverted
          ? "border-red-400/25 bg-red-400/14 text-red-200"
          : "border-[hsl(var(--danger))]/15 bg-[hsl(var(--danger))]/[0.08] text-[hsl(var(--danger))]"
      }`}
    >
      <IconAlert className="h-[15px] w-[15px] flex-none" />
      <span className="min-w-0 wrap-break-word text-[12px] font-medium leading-5">{message}</span>
      {testedAt && <span className="col-start-2 whitespace-nowrap text-right font-mono text-[11px] tabular-nums opacity-75 sm:col-start-auto">{formatTestTime(testedAt)}</span>}
    </div>
  );
}

function AnchorProtoPill({ label, ok }: { label: string; ok?: boolean }) {
  if (ok === undefined) return null;
  return (
    <span className={`inline-flex items-center gap-1 text-[11.5px] font-semibold ${ok ? "text-[hsl(var(--anchor-accent))]" : "text-red-300"}`}>
      {ok ? <IconCheck className="h-3 w-3" /> : <IconX className="h-3 w-3" />}
      {label}
    </span>
  );
}

function AnchorHeroTile({
  server,
  active,
  testing,
  locked,
  onTestServer
}: {
  server?: ServerInfo;
  active: boolean;
  testing: ActiveProbe | null;
  locked: boolean;
  onTestServer: (server: string, mode: ProbeMode) => void;
}) {
  const probe = server ? resolveProbe(server) : null;
  const check = probe?.check;
  const isTesting = Boolean(server && testing?.server === server.raw);
  const tcpTone = !check ? "muted" : check.tcpConnect?.ok ? latencyTone(check.tcpConnect.avgMs) : "danger";
  const hasRange = check?.tcpConnect?.ok && Number.isFinite(check.tcpConnect.minMs) && Number.isFinite(check.tcpConnect.maxMs);
  const checkTooltip = check?.testedAt ? `检查时间：${formatTestTime(check.testedAt)}` : undefined;

  return (
    <section className="bento-anchor relative flex flex-col justify-between p-5 sm:p-6 md:col-span-2 xl:col-span-1">
      <div>
        <div className="flex flex-wrap items-center justify-between gap-2">
          <span className="text-[12px] font-medium text-[hsl(var(--anchor-muted))]">当前活跃 TURN 节点</span>
          {server && (
            <div
              className={`inline-flex shrink-0 items-center gap-2 whitespace-nowrap rounded-full border border-white/10 bg-white/[0.06] px-2.5 py-1 leading-none ${checkTooltip ? "ui-tooltip" : ""}`}
              data-tooltip={checkTooltip}
              data-tooltip-align="right"
              tabIndex={checkTooltip ? 0 : undefined}
            >
              {!check ? (
                <span className="text-[11.5px] font-medium text-[hsl(var(--anchor-muted))]">未检查</span>
              ) : (
                <>
                  <AnchorProtoPill label="TCP" ok={check.socksTcp?.ok} />
                  {check.socksTcp !== undefined && check.socksUdp !== undefined && (
                    <span className="text-white/20" aria-hidden="true">·</span>
                  )}
                  <AnchorProtoPill label="UDP" ok={check.socksUdp?.ok} />
                  {check.socksTcp === undefined && check.socksUdp === undefined && (
                    <span className={`text-[11.5px] font-semibold ${check.ok ? "text-[hsl(var(--anchor-accent))]" : "text-red-300"}`}>
                      {check.ok ? "转发可用" : "转发失败"}
                    </span>
                  )}
                </>
              )}
            </div>
          )}
        </div>

        <div className="mt-3 break-all font-mono text-[23px] font-semibold leading-[1.2] tracking-[-0.02em] text-[hsl(var(--anchor-fg))] sm:text-[26px]">
          {server ? (
            <>
              {displayHost(server)}
              {displayPort(server) && (
                <span className="whitespace-nowrap text-[0.65em] font-normal text-[hsl(var(--anchor-muted))]">:{displayPort(server)}</span>
              )}
            </>
          ) : "暂无节点"}
        </div>

        {server && (
          <div className="mt-2.5 flex flex-wrap items-center gap-2 text-[12px] text-[hsl(var(--anchor-muted))]">
            {server.current && !active && (
              <span className="inline-flex items-center gap-1 rounded-[6px] bg-amber-400/15 px-2 py-0.5 text-[11.5px] font-medium text-amber-200">
                代理已停止
              </span>
            )}
            {server.note && (
              <span
                className="ui-tooltip inline-flex max-w-[180px] rounded-[6px] bg-amber-300/14 px-2 py-0.5 text-[11.5px] font-medium text-amber-200 sm:max-w-[220px]"
                data-tooltip={server.note}
                data-tooltip-align="left"
                tabIndex={0}
              >
                <span className="truncate">{server.note}</span>
              </span>
            )}
            <span>{server.hasAuth ? `鉴权：${server.username || "已配置"}` : "无鉴权"}</span>
            {check?.testedAt && (
              <>
                <span className="opacity-35" aria-hidden="true">·</span>
                <span>{formatRelativeTime(check.testedAt)}检查</span>
              </>
            )}
          </div>
        )}
      </div>

      {server && (
        <div className={`mt-5 space-y-3 transition-opacity ${isTesting ? "opacity-55" : ""}`}>
          <div className="flex flex-col gap-3.5 border-t border-white/10 pt-4 sm:flex-row sm:items-end sm:justify-between">
            <div className="min-w-0">
              <div className={`font-mono font-semibold leading-none tracking-[-0.02em] tabular-nums ${check?.tcpConnect?.ok ? "text-[31px]" : "text-[22px]"} ${anchorToneText[tcpTone]}`}>
                {!check ? "未检查" : check.tcpConnect?.ok ? (
                  <>
                    {msNum(check.tcpConnect.avgMs)}
                    <span className="ml-1 text-[13px] font-normal text-[hsl(var(--anchor-muted))]">ms</span>
                  </>
                ) : "失败"}
              </div>
              <div className="mt-1.5 font-mono text-[11px] tabular-nums text-[hsl(var(--anchor-muted))]">
                TCP 延迟{hasRange ? ` · ${msNum(check!.tcpConnect!.minMs)}~${msNum(check!.tcpConnect!.maxMs)}ms` : ""}
              </div>
            </div>

            <div className="grid grid-cols-2 gap-2 sm:flex sm:shrink-0">
              <button
                disabled={locked}
                onClick={() => onTestServer(server.raw, "check")}
                className="inline-flex h-[33px] cursor-pointer items-center justify-center gap-1.5 whitespace-nowrap rounded-[9px] border border-white/15 bg-white/[0.08] px-3 text-[12.5px] font-medium text-white transition-colors hover:bg-white/[0.15] disabled:cursor-not-allowed disabled:opacity-45"
                type="button"
              >
                <IconActivity className="h-3.5 w-3.5" />
                {isTesting && testing?.mode === "check" ? "检查中…" : "检查"}
              </button>
              <button
                disabled={locked}
                onClick={() => onTestServer(server.raw, "speed")}
                className="inline-flex h-[33px] cursor-pointer items-center justify-center gap-1.5 whitespace-nowrap rounded-[9px] border border-transparent bg-[hsl(var(--anchor-accent))] px-3.5 text-[12.5px] font-semibold text-[#062219] shadow-[0_2px_10px_rgba(52,211,153,0.28)] transition-all hover:brightness-105 disabled:cursor-not-allowed disabled:opacity-45"
                type="button"
              >
                <IconZap className="h-3.5 w-3.5" />
                {isTesting && testing?.mode === "speed" ? "测速中…" : "测速"}
              </button>
            </div>
          </div>

          {probe && probe.failures.length > 0 && (
            <div className="space-y-2">
              {probe.failures.map(({ message, testedAt }, index) => (
                <FailureNotice key={index} message={message} testedAt={testedAt} inverted />
              ))}
            </div>
          )}
        </div>
      )}
    </section>
  );
}

function BandwidthTile({ server, dim }: { server?: ServerInfo; dim?: boolean }) {
  const single = speedDisplay("单线程", server?.test, server?.test?.singleThread);
  const multi = speedDisplay("多线程", server?.test, server?.test?.multiThread);
  const maxRef = Math.max(single.rawMbps, multi.rawMbps, 100) * 1.08;
  const source = server?.test?.multiThread?.source || server?.test?.singleThread?.source;
  const testedAt = server?.test?.testedAt;
  const downloaded = formatBytesMiB(server?.test?.downloadBytes);

  return (
    <section className={`bento-tile flex flex-col justify-between p-5 transition-opacity ${dim ? "opacity-55" : ""}`}>
      <div className="flex items-center justify-between gap-2 text-[12px] text-[hsl(var(--muted-foreground))]">
        <span className="font-medium">吞吐带宽</span>
        <span className="font-mono text-[11px]">{source || (server?.test ? "自定义测速" : "未测速")}</span>
      </div>

      <div className="my-auto flex flex-col justify-center space-y-4 py-4">
        {[
          { key: "single", info: single, multiBar: false },
          { key: "multi", info: multi, multiBar: true }
        ].map(({ key, info, multiBar }) => {
          const pct = info.measured ? Math.min(100, Math.max(6, (info.rawMbps / maxRef) * 100)) : 0;
          const barClass = info.incomplete
            ? "bg-[hsl(var(--warn))]"
            : multiBar
              ? "bg-gradient-to-r from-[hsl(var(--primary))] to-[hsl(var(--anchor-accent))]"
              : "bg-[hsl(var(--primary))]";
          return (
            <div
              key={key}
              className={info.tooltip ? "ui-tooltip" : undefined}
              data-tooltip={info.tooltip}
              data-tooltip-side="top"
              tabIndex={info.tooltip ? 0 : undefined}
            >
              <div className="mb-1.5 flex items-baseline justify-between gap-2">
                <span className="text-[12px] text-[hsl(var(--muted-foreground))]">{info.title}</span>
                <span className={`font-mono font-semibold leading-none tabular-nums ${info.measured ? "text-[19px]" : "text-[13.5px]"} ${info.tone ? toneText[info.tone] : "text-[hsl(var(--foreground))]"}`}>
                  {info.value}
                  {info.measured && <span className="ml-1 text-[11.5px] font-normal text-[hsl(var(--muted-foreground))]">Mbps</span>}
                </span>
              </div>
              <div className="h-[8px] w-full overflow-hidden rounded-full bg-[hsl(var(--muted))]">
                <div className={`h-full rounded-full transition-[width] duration-500 ease-out ${barClass}`} style={{ width: `${pct}%` }} />
              </div>
            </div>
          );
        })}
      </div>

      <div className="flex items-center justify-between gap-2 border-t border-[hsl(var(--border))]/70 pt-3 text-[11px] text-[hsl(var(--muted-foreground))]">
        {testedAt ? (
          <span
            className="ui-tooltip truncate"
            data-tooltip={`测速时间：${formatTestTime(testedAt)}`}
            data-tooltip-side="top"
            data-tooltip-align="left"
            tabIndex={0}
          >
            最近测速：{formatRelativeTime(testedAt)}
          </span>
        ) : (
          <span>点击「测速」获取带宽</span>
        )}
        <span className="shrink-0 font-mono tabular-nums">{downloaded || "约 112 MiB / 次"}</span>
      </div>
    </section>
  );
}

function CompactNodeResults({ server, dim }: { server: ServerInfo; dim?: boolean }) {
  const { check, latest, failures } = resolveProbe(server);
  if (!latest) {
    return (
      <div className="text-[12px] text-[hsl(var(--muted-foreground))]">
        {dim ? "正在检测…" : "尚未检测，点击“检查”或“测速”获取数据"}
      </div>
    );
  }

  const tcpTone = !check ? "muted" : check.tcpConnect?.ok ? latencyTone(check.tcpConnect.avgMs) : "danger";
  const udpTone = !check ? "muted" : check.socksUdp?.ok ? "ok" : "danger";
  const single = speedDisplay("单线程", server.test, server.test?.singleThread);
  const multi = speedDisplay("多线程", server.test, server.test?.multiThread);

  return (
    <div className={`space-y-2.5 transition-opacity ${dim ? "opacity-45" : ""}`}>
      <div className="flex flex-wrap items-center gap-x-4 gap-y-1.5 text-[12px]">
        <div className="inline-flex items-baseline gap-1.5">
          <span className="text-[11px] text-[hsl(var(--muted-foreground))]">TCP 延迟</span>
          <span className={`font-mono font-semibold tabular-nums ${toneText[tcpTone]}`}>
            {!check ? "未检查" : check.tcpConnect?.ok ? ms(check.tcpConnect.avgMs) : "失败"}
          </span>
        </div>

        <div className="inline-flex items-baseline gap-1.5">
          <span className="text-[11px] text-[hsl(var(--muted-foreground))]">UDP 转发</span>
          <span className={`font-mono font-semibold ${toneText[udpTone]}`}>
            {!check ? "未检查" : check.socksUdp?.ok ? "可用" : "失败"}
          </span>
        </div>

        {[single, multi].map((item) => (
          <div
            key={item.title}
            className={`inline-flex items-baseline gap-1.5 ${item.tooltip ? "ui-tooltip" : ""}`}
            data-tooltip={item.tooltip}
            data-tooltip-side="top"
            tabIndex={item.tooltip ? 0 : undefined}
          >
            <span className="text-[11px] text-[hsl(var(--muted-foreground))]">{item.title}</span>
            <span className={`font-mono font-semibold tabular-nums ${item.tone ? toneText[item.tone] : "text-[hsl(var(--foreground))]"}`}>
              {item.value}
              {item.measured && <span className="ml-0.5 text-[10.5px] font-normal text-[hsl(var(--muted-foreground))]">Mbps</span>}
            </span>
          </div>
        ))}

        <div className="inline-flex items-baseline gap-1.5 sm:ml-auto">
          <span className="text-[11px] text-[hsl(var(--muted-foreground))]">测试时间</span>
          <span className="whitespace-nowrap font-mono text-[11.5px] tabular-nums text-[hsl(var(--muted-foreground))]">
            {formatTestTime(latest.testedAt)}
          </span>
        </div>
      </div>

      {failures.map(({ message, testedAt }, index) => (
        <FailureNotice key={index} message={message} testedAt={testedAt} />
      ))}
    </div>
  );
}

function NoteChip({ note }: { note: string }) {
  return (
    <span className="ui-tooltip inline-flex min-w-0" data-tooltip={note} tabIndex={0}>
      <Chip accent><span className="block max-w-[160px] truncate sm:max-w-[240px]">{note}</span></Chip>
    </span>
  );
}

export function NodePanel({
  state,
  serverInput,
  testing,
  busyAction = null,
  locked,
  copiedKey = "",
  gatewaySlot,
  sideSlot,
  onCopy,
  onServerInput,
  onAddServer,
  onTestServer,
  onTestAll,
  onStopTesting,
  onSelectServer,
  onDeleteServer,
  onUpdateNote
}: Props) {
  const currentServer = state.servers.find((server) => server.current) || state.servers[0];
  const [editing, setEditing] = useState("");
  const [note, setNote] = useState("");
  const noteDialog = useRef<HTMLDialogElement>(null);
  const noteInput = useRef<HTMLInputElement>(null);
  const hasExistingNote = !!state.servers.find((server) => server.raw === editing)?.note;

  useEffect(() => {
    const dialog = noteDialog.current;
    if (!dialog) return;
    if (editing && !dialog.open) {
      dialog.showModal();
      noteInput.current?.focus();
      noteInput.current?.select();
    }
    if (!editing && dialog.open) dialog.close();
  }, [editing]);

  function editNote(server: string, value?: string) {
    setEditing(server);
    setNote(value || "");
  }

  async function saveNote() {
    if (editing && await onUpdateNote(editing, note.trim())) setEditing("");
  }

  const isCurrentTesting = Boolean(currentServer && testing?.server === currentServer.raw);

  return (
    <div className="space-y-4">
      <div className={`grid grid-cols-1 items-stretch gap-4 ${gatewaySlot ? "md:grid-cols-2 xl:grid-cols-[1.4fr_1fr_0.92fr]" : "md:grid-cols-[1.45fr_1fr]"}`}>
        <AnchorHeroTile
          server={currentServer}
          active={state.service.active}
          testing={testing}
          locked={locked}
          onTestServer={onTestServer}
        />
        <BandwidthTile server={currentServer} dim={isCurrentTesting} />
        {gatewaySlot}
      </div>

      <div className={`grid grid-cols-1 items-start gap-4 ${sideSlot ? "xl:grid-cols-[1.52fr_1fr]" : ""}`}>
        <section className="bento-tile p-4 sm:p-5">
          <div className="flex flex-wrap items-center justify-between gap-2.5 pb-4">
            <h2 className="flex items-center gap-2 text-[14.5px] font-semibold text-[hsl(var(--foreground))]">
              <span>节点管理</span>
              <span className="inline-flex h-5 min-w-5 items-center justify-center rounded-full bg-[hsl(var(--muted))] px-1.5 font-mono text-[11.5px] font-medium tabular-nums text-[hsl(var(--muted-foreground))]">
                {state.servers.length}
              </span>
            </h2>
            <div className="flex flex-wrap items-center gap-2">
              {testing && (
                <button className="inline-flex h-[30px] cursor-pointer items-center justify-center gap-1.5 whitespace-nowrap rounded-[8px] border border-[hsl(var(--danger))]/25 bg-[hsl(var(--danger))]/10 px-[11px] text-[12px] font-medium leading-none text-[hsl(var(--danger))] transition-colors hover:bg-[hsl(var(--danger))]/15" onClick={onStopTesting} type="button">
                  {testing.total && testing.total > 1 && testing.index ? `停止检测 (${testing.index}/${testing.total})` : "停止检测"}
                </button>
              )}
              <button className={`${smallButtonClass} ui-tooltip`} disabled={locked || !state.servers.length} onClick={() => onTestAll("check")} aria-label="检查全部" data-tooltip="检查延迟与连通性" type="button">检查全部</button>
              <button className={`${smallButtonClass} ui-tooltip`} disabled={locked || !state.servers.length} onClick={() => onTestAll("speed")} aria-label="测速全部" data-tooltip="约 112 MiB / 节点" data-tooltip-align="right" type="button">
                <IconZap className="h-3.5 w-3.5" />
                测速全部
              </button>
            </div>
          </div>

          <form className="mb-4 flex flex-col gap-2.5 sm:flex-row" onSubmit={onAddServer}>
            <input type="text" placeholder="host:port 或 user:pass@host:port" value={serverInput} onChange={(event) => onServerInput(event.target.value)} className={`${inputClass} flex-1`} />
            <button className={`${primaryButtonClass} min-h-[38px] px-4`} disabled={locked} type="submit">
              <IconPlus className="h-3.5 w-3.5" />
              {busyAction === "add" ? "添加中…" : "添加节点"}
            </button>
          </form>

          <div className="grid gap-3">
            {state.servers.map((server) => {
              const isCurrent = server.current;
              const isTesting = testing?.server === server.raw;
              const copyKey = `node:${server.raw}`;
              return (
                <article
                  key={server.raw}
                  className={`rounded-[14px] border p-3.5 transition-colors sm:px-4 ${
                    isCurrent
                      ? "border-[hsl(var(--primary))]/35 bg-[hsl(var(--primary))]/[0.038]"
                      : "border-[hsl(var(--border))] bg-[hsl(var(--card))] hover:border-[hsl(var(--input))]"
                  }`}
                >
                  <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
                    <div className="min-w-0 flex-1">
                      <div className="flex items-start gap-1.5">
                        <div className="min-w-0 break-all font-mono text-[13px] font-medium leading-[1.5] text-[hsl(var(--foreground))]">{server.raw}</div>
                        {onCopy && (
                          <button
                            type="button"
                            onClick={() => onCopy(server.raw, copyKey, "节点地址")}
                            aria-label="复制节点地址"
                            data-tooltip="复制节点"
                            data-tooltip-side="top"
                            className="ui-tooltip mt-0.5 inline-grid h-5 w-5 flex-none cursor-pointer place-items-center rounded-[5px] text-[hsl(var(--muted-foreground))] transition-colors hover:bg-[hsl(var(--muted))] hover:text-[hsl(var(--foreground))]"
                          >
                            {copiedKey === copyKey ? <IconCheck className="h-3 w-3 text-[hsl(var(--ok))]" /> : <IconCopy className="h-3 w-3" />}
                          </button>
                        )}
                      </div>
                      <div className="mt-2 flex flex-wrap items-center gap-1.5">
                        {isCurrent ? <Chip active>当前</Chip> : server.default ? <Chip>默认</Chip> : <Chip>备用</Chip>}
                        <Chip>{server.hasAuth ? `鉴权：${server.username || "已配置"}` : "无鉴权"}</Chip>
                        {server.note && <NoteChip note={server.note} />}
                        {isTesting && <Chip warn><span className="animate-pulse motion-reduce:animate-none">{testing.mode === "check" ? "检查中" : "测速中"}</span></Chip>}
                      </div>
                    </div>

                    <div className="flex flex-wrap items-center gap-1.5 border-t border-[hsl(var(--border))]/70 pt-2.5 sm:shrink-0 sm:border-t-0 sm:pt-0">
                      <button disabled={locked} onClick={() => onTestServer(server.raw, "check")} className={`${smallButtonClass} ui-tooltip`} aria-label="检查" data-tooltip="延迟与连通性检查" type="button">检查</button>
                      <button disabled={locked} onClick={() => onTestServer(server.raw, "speed")} className={`${smallButtonClass} ui-tooltip`} aria-label="测速" data-tooltip="仅测带宽，约 112 MiB" type="button">测速</button>
                      {!isCurrent && (
                        <button disabled={locked} onClick={() => onSelectServer(server.raw)} className={softButtonClass} type="button">切换</button>
                      )}
                      <span className="flex-1 sm:hidden" aria-hidden="true" />
                      <button disabled={locked} onClick={() => editNote(server.raw, server.note)} className={`${iconButtonClass} ui-tooltip`} aria-label={server.note ? "修改备注" : "添加备注"} data-tooltip={server.note ? "修改备注" : "添加备注"} data-tooltip-side="top" data-tooltip-align="right" type="button">
                        <IconEdit className="h-3.5 w-3.5" />
                      </button>
                      <button disabled={locked} onClick={() => onDeleteServer(server.raw)} className={`${iconDangerButtonClass} ui-tooltip`} aria-label="删除" data-tooltip="删除" data-tooltip-side="top" data-tooltip-align="right" type="button">
                        <IconTrash className="h-4 w-4" />
                      </button>
                    </div>
                  </div>

                  <div className="mt-3 border-t border-[hsl(var(--border))]/70 pt-2.5">
                    <CompactNodeResults server={server} dim={isTesting} />
                  </div>
                </article>
              );
            })}

            {state.servers.length === 0 && (
              <div className="rounded-[14px] border border-dashed border-[hsl(var(--border))] p-6 text-center text-[13px] text-[hsl(var(--muted-foreground))]">暂无节点</div>
            )}

            {state.servers.length === 1 && (
              <p className="pt-1 text-center text-[11.5px] text-[hsl(var(--muted-foreground))]/85">
                添加备用节点后，主节点故障时将自动切换，列表项也会独立展示延迟与测速对比
              </p>
            )}
          </div>
        </section>

        {sideSlot}
      </div>

      <dialog
        ref={noteDialog}
        aria-labelledby="note-dialog-title"
        className="m-auto w-[min(420px,calc(100%-32px))] rounded-[20px] border border-[hsl(var(--border))] bg-[hsl(var(--card))] p-0 text-[hsl(var(--foreground))] shadow-[0_24px_64px_rgba(0,0,0,.26)] backdrop:bg-black/45 backdrop:backdrop-blur-[3px]"
        onCancel={(event) => { event.preventDefault(); setEditing(""); }}
        onClose={() => setEditing("")}
        onClick={(event) => { if (event.target === event.currentTarget) setEditing(""); }}
      >
        <form className="p-5" onSubmit={(event) => { event.preventDefault(); void saveNote(); }}>
          <h2 id="note-dialog-title" className="text-[14.5px] font-semibold">{hasExistingNote ? "修改备注" : "添加备注"}</h2>
          <div className="mt-1 break-all font-mono text-[12px] text-[hsl(var(--muted-foreground))]">{editing}</div>
          <input ref={noteInput} maxLength={60} value={note} onChange={(event) => setNote(event.target.value)} placeholder="输入节点备注" className={`${inputClass} mt-4 w-full font-sans`} />
          <div className="mt-5 flex justify-end gap-2">
            <button className={topButtonClass} onClick={() => setEditing("")} type="button">取消</button>
            <button className={`${primaryButtonClass} h-[34px] px-[14px]`} disabled={locked} type="submit">
              {busyAction === "note" ? "保存中…" : "保存备注"}
            </button>
          </div>
        </form>
      </dialog>
    </div>
  );
}
