import { useState, type Ref, type SubmitEvent } from "react";
import { Chip } from "../../components/Chip";
import { IconAlert, IconArrowRight } from "../../components/icons";
import { Switch } from "../../components/Switch";
import { inputClass, labelClass, labelTextClass, mutedSubmitButtonClass, primaryButtonClass, topButtonClass } from "../../controlClasses";
import { formatRelativeTime, formatTestTime } from "../../lib/format";
import type { ConfigForm, PanelState } from "../../types/panel";

export type SideTab = "events" | "config";

type Props = {
  state: PanelState;
  config: ConfigForm;
  busy: boolean;
  saving?: boolean;
  dirty?: boolean;
  restartsProxy?: boolean;
  activeTab?: SideTab;
  panelRef?: Ref<HTMLElement>;
  onTabChange?: (tab: SideTab) => void;
  onSubmit: (event: SubmitEvent<HTMLFormElement>) => void;
  onReset?: () => void;
  onFieldChange: <K extends keyof ConfigForm>(key: K, value: ConfigForm[K]) => void;
};

export function SettingsPanel({
  state,
  config,
  busy,
  saving = false,
  dirty = false,
  restartsProxy = false,
  activeTab,
  panelRef,
  onTabChange,
  onSubmit,
  onReset,
  onFieldChange
}: Props) {
  const [innerTab, setInnerTab] = useState<SideTab>("events");
  const tab = activeTab ?? innerTab;
  const setTab = (next: SideTab) => {
    setInnerTab(next);
    onTabChange?.(next);
  };

  const failure = state.runtime?.last_failure;
  const lastSwitch = state.runtime?.last_switch;

  return (
    <aside ref={panelRef} className="bento-tile p-4 sm:p-5">
      <div role="tablist" aria-label="运行事件与服务配置" className="grid grid-cols-2 gap-1 rounded-[11px] bg-[hsl(var(--muted))] p-1">
        <button
          role="tab"
          aria-selected={tab === "events"}
          type="button"
          onClick={() => setTab("events")}
          className={`flex h-[32px] cursor-pointer items-center justify-center gap-1.5 whitespace-nowrap rounded-[8px] text-[12.5px] transition-colors ${
            tab === "events"
              ? "bg-[hsl(var(--card))] font-semibold text-[hsl(var(--foreground))] shadow-xs"
              : "font-medium text-[hsl(var(--muted-foreground))] hover:text-[hsl(var(--foreground))]"
          }`}
        >
          <span>运行事件</span>
          {failure && <span className="h-1.5 w-1.5 rounded-full bg-[hsl(var(--danger))]" aria-hidden="true" />}
        </button>
        <button
          role="tab"
          aria-selected={tab === "config"}
          type="button"
          onClick={() => setTab("config")}
          className={`flex h-[32px] cursor-pointer items-center justify-center gap-1.5 whitespace-nowrap rounded-[8px] text-[12.5px] transition-colors ${
            tab === "config"
              ? "bg-[hsl(var(--card))] font-semibold text-[hsl(var(--foreground))] shadow-xs"
              : "font-medium text-[hsl(var(--muted-foreground))] hover:text-[hsl(var(--foreground))]"
          }`}
        >
          <span>服务配置</span>
          {dirty && <span className="h-1.5 w-1.5 rounded-full bg-[hsl(var(--warn))]" aria-hidden="true" />}
        </button>
      </div>

      {tab === "events" ? (
        <div className="mt-4 space-y-3">
          <div className="flex items-center justify-between text-[11.5px] text-[hsl(var(--muted-foreground))]">
            <span>历史记录，实时状态以检查为准</span>
          </div>

          <dl className="space-y-3 text-[12px]">
            <div className={`rounded-[13px] border p-3.5 ${failure ? "border-[hsl(var(--danger))]/18 bg-[hsl(var(--danger))]/[0.06]" : "border-[hsl(var(--border))] bg-[hsl(var(--muted))]"}`}>
              <div className="mb-1.5 flex items-center justify-between gap-2">
                <dt className={`inline-flex items-center gap-1.5 font-semibold ${failure ? "text-[hsl(var(--danger))]" : "text-[hsl(var(--muted-foreground))]"}`}>
                  {failure && <IconAlert className="h-3.5 w-3.5 flex-none" />}
                  <span>最近记录的失败</span>
                </dt>
                {failure?.at && (
                  <time
                    className="ui-tooltip shrink-0 whitespace-nowrap rounded-full bg-[hsl(var(--card))]/85 px-2 py-0.5 text-[11px] text-[hsl(var(--muted-foreground))]"
                    dateTime={failure.at}
                    data-tooltip={formatTestTime(failure.at)}
                    data-tooltip-align="right"
                    tabIndex={0}
                  >
                    {formatRelativeTime(failure.at)}
                  </time>
                )}
              </div>
              <dd className="space-y-1 wrap-break-word">
                {failure ? (
                  <>
                    <div className="text-[11.5px] font-medium text-[hsl(var(--danger))]/90">
                      {failure.stage}{failure.addr && <span className="font-mono"> · {failure.addr}</span>}
                    </div>
                    <p className="font-mono text-[11.5px] leading-5 text-[hsl(var(--foreground))] wrap-break-word">
                      {failure.message}
                    </p>
                  </>
                ) : (
                  <span className="text-[hsl(var(--muted-foreground))]">暂无失败记录</span>
                )}
              </dd>
            </div>

            <div className="rounded-[13px] border border-[hsl(var(--border))]/80 bg-[hsl(var(--muted))] p-3.5">
              <div className="mb-1.5 flex items-center justify-between gap-2">
                <dt className="font-medium text-[hsl(var(--muted-foreground))]">最近节点切换</dt>
                {lastSwitch?.at && (
                  <time
                    className="ui-tooltip shrink-0 whitespace-nowrap rounded-full bg-[hsl(var(--card))]/85 px-2 py-0.5 text-[11px] text-[hsl(var(--muted-foreground))]"
                    dateTime={lastSwitch.at}
                    data-tooltip={formatTestTime(lastSwitch.at)}
                    data-tooltip-align="right"
                    tabIndex={0}
                  >
                    {formatRelativeTime(lastSwitch.at)}
                  </time>
                )}
              </div>
              <dd className="space-y-1 break-all">
                {lastSwitch ? (
                  <>
                    <div className="flex flex-wrap items-center gap-1.5 font-mono text-[12px] text-[hsl(var(--foreground))]">
                      {lastSwitch.from && (
                        <>
                          <span>{lastSwitch.from}</span>
                          <IconArrowRight className="h-3 w-3 flex-none text-[hsl(var(--muted-foreground))]" />
                        </>
                      )}
                      <span>{lastSwitch.to || "无节点"}</span>
                    </div>
                    <p className="text-[11.5px] text-[hsl(var(--muted-foreground))]">{lastSwitch.reason}</p>
                  </>
                ) : (
                  <span className="text-[hsl(var(--muted-foreground))]">暂无切换记录</span>
                )}
              </dd>
            </div>
          </dl>

          <div className="flex items-center justify-between gap-2 border-t border-[hsl(var(--border))]/70 pt-3 text-[12px]">
            <span className="text-[hsl(var(--muted-foreground))]">需修改监听 / DoH / 登录密码？</span>
            <button
              type="button"
              onClick={() => setTab("config")}
              className="inline-flex shrink-0 cursor-pointer items-center gap-1 font-medium text-[hsl(var(--primary-text))] transition-opacity hover:opacity-80"
            >
              <span>服务配置</span>
              <IconArrowRight className="h-3 w-3" />
            </button>
          </div>
        </div>
      ) : (
        <form className="mt-4 grid gap-3.5" onSubmit={onSubmit}>
          <div className="flex items-center justify-between gap-2">
            <span className="text-[11.5px] text-[hsl(var(--muted-foreground))]">持久化至配置文件</span>
            <div className="flex items-center gap-1.5">
              {dirty && <Chip warn>未保存</Chip>}
              <Chip mono>config.env</Chip>
            </div>
          </div>

          <label className={labelClass}>
            <span className="flex items-center justify-between gap-2">
              <span className={labelTextClass}>SOCKS5 监听</span>
              <span className="text-[11px] text-[hsl(var(--muted-foreground))]">修改后重启代理</span>
            </span>
            <input type="text" value={config.listen} onChange={(event) => onFieldChange("listen", event.target.value)} className={inputClass} />
          </label>
          <label className={labelClass}>
            <span className="flex items-center justify-between gap-2">
              <span className={labelTextClass}>DoH</span>
              <span className="text-[11px] text-[hsl(var(--muted-foreground))]">修改后重启代理</span>
            </span>
            <input type="text" value={config.doh} onChange={(event) => onFieldChange("doh", event.target.value)} className={inputClass} />
          </label>
          <label className={`flex items-center justify-between gap-4 border-y border-[hsl(var(--border))] py-3 ${busy ? "cursor-not-allowed" : "cursor-pointer"}`}>
            <div>
              <div className="text-[13px] font-medium text-[hsl(var(--foreground))]">启用面板登录</div>
              <div className="mt-0.5 text-[12px] text-[hsl(var(--muted-foreground))]">关闭后访问面板无需登录</div>
            </div>
            <Switch checked={config.panelAuthEnabled} disabled={busy} onChange={(value) => onFieldChange("panelAuthEnabled", value)} />
          </label>
          <label className={labelClass}>
            <span className={labelTextClass}>用户名</span>
            <input type="text" autoComplete="off" disabled={busy || !config.panelAuthEnabled} value={config.panelUsername} onChange={(event) => onFieldChange("panelUsername", event.target.value)} className={`${inputClass} disabled:cursor-not-allowed disabled:opacity-55`} />
          </label>
          <label className={labelClass}>
            <span className={labelTextClass}>密码</span>
            <input type="password" autoComplete="new-password" disabled={busy || !config.panelAuthEnabled} placeholder="留空不修改" value={config.panelPassword} onChange={(event) => onFieldChange("panelPassword", event.target.value)} className={`${inputClass} disabled:cursor-not-allowed disabled:opacity-55`} />
          </label>

          <div className="mt-1 flex gap-2">
            {dirty && onReset && (
              <button className={`${topButtonClass} min-h-[38px] px-4`} disabled={busy} onClick={onReset} type="button">
                还原
              </button>
            )}
            <button className={`${dirty ? primaryButtonClass : mutedSubmitButtonClass} min-h-[38px] flex-1`} disabled={busy || !dirty} type="submit">
              {saving ? "保存中…" : dirty ? (restartsProxy ? "保存并重启代理" : "保存配置") : "保存配置（无修改）"}
            </button>
          </div>
        </form>
      )}
    </aside>
  );
}
