import type { SubmitEvent } from "react";
import { Chip } from "../../components/Chip";
import { Switch } from "../../components/Switch";
import { inputClass, labelClass, labelTextClass, primaryButtonClass } from "../../controlClasses";
import { formatTestTime } from "../../lib/format";
import type { ConfigForm, PanelState } from "../../types/panel";

type Props = {
  state: PanelState;
  config: ConfigForm;
  busy: boolean;
  onSubmit: (event: SubmitEvent<HTMLFormElement>) => void;
  onFieldChange: <K extends keyof ConfigForm>(key: K, value: ConfigForm[K]) => void;
};

export function SettingsPanel({ state, config, busy, onSubmit, onFieldChange }: Props) {
  const failure = state.runtime?.last_failure;
  const lastSwitch = state.runtime?.last_switch;
  return (
    <aside className="flex flex-col gap-5">
      <section className="shell-window overflow-hidden">
        <div className="flex items-center justify-between gap-3 border-b border-[hsl(var(--border))] px-4 py-3.5 md:px-[18px]">
          <h2 className="text-[14.5px] font-semibold text-[hsl(var(--foreground))]">概览</h2>
          <Chip mono>PID {state.service.pid || "-"}</Chip>
        </div>
        <div className="grid gap-3.5 p-4 md:p-[18px]">
          <div className="grid grid-cols-2 gap-3.5">
            <div className="grid gap-1.5">
              <div className="text-[12px] font-medium text-[hsl(var(--muted-foreground))]">SOCKS5 监听</div>
              <div className="break-all font-mono text-[13px] leading-[1.55] text-[hsl(var(--foreground))]">{state.listen || "-"}</div>
            </div>
            <div className="grid gap-1.5">
              <div className="text-[12px] font-medium text-[hsl(var(--muted-foreground))]">节点数</div>
              <div className="font-mono text-[13px] leading-[1.55] text-[hsl(var(--foreground))]">{state.servers.length}</div>
            </div>
          </div>
          <div className="grid gap-1.5">
            <div className="text-[12px] font-medium text-[hsl(var(--muted-foreground))]">DoH</div>
            <div className="break-all font-mono text-[13px] leading-[1.55] text-[hsl(var(--foreground))]">{state.doh || "-"}</div>
          </div>
        </div>
      </section>

      <section className="shell-window overflow-hidden">
        <div className="border-b border-[hsl(var(--border))] px-4 py-3.5 md:px-[18px]">
          <h2 className="text-[14.5px] font-semibold text-[hsl(var(--foreground))]">最近事件</h2>
          <p className="mt-1 text-[11px] text-[hsl(var(--muted-foreground))]">历史记录，当前连通性可通过节点检查确认</p>
        </div>
        <dl className="grid gap-4 p-4 text-[12px] md:p-[18px]">
          <div>
            <dt className="mb-1 font-medium text-[hsl(var(--muted-foreground))]">最近记录的失败</dt>
            <dd className="space-y-1 break-all">
              {failure ? <>
                <div className="text-[hsl(var(--danger))]">{failure.stage}{failure.addr && ` · ${failure.addr}`}</div>
                <p>{failure.message}</p>
                <time className="block text-[11px] text-[hsl(var(--muted-foreground))]" dateTime={failure.at}>{formatTestTime(failure.at)}</time>
              </> : "暂无记录"}
            </dd>
          </div>
          <div className="border-t border-[hsl(var(--border))] pt-3">
            <dt className="mb-1 font-medium text-[hsl(var(--muted-foreground))]">最近节点切换</dt>
            <dd className="space-y-1 break-all">
              {lastSwitch ? <>
                <div className="font-mono">{lastSwitch.from && `${lastSwitch.from} → `}{lastSwitch.to || "无节点"}</div>
                <p>{lastSwitch.reason}</p>
                <time className="block text-[11px] text-[hsl(var(--muted-foreground))]" dateTime={lastSwitch.at}>{formatTestTime(lastSwitch.at)}</time>
              </> : "暂无记录"}
            </dd>
          </div>
        </dl>
      </section>

      <section className="shell-window overflow-hidden">
        <div className="flex items-center justify-between gap-3 border-b border-[hsl(var(--border))] px-4 py-3.5 md:px-[18px]">
          <h2 className="text-[14.5px] font-semibold text-[hsl(var(--foreground))]">配置</h2>
          <Chip mono>config.env</Chip>
        </div>
        <form className="grid gap-3.5 p-4 md:p-[18px]" onSubmit={onSubmit}>
          <label className={labelClass}>
            <span className={labelTextClass}>SOCKS5 监听</span>
            <input type="text" value={config.listen} onChange={(event) => onFieldChange("listen", event.target.value)} className={inputClass} />
          </label>
          <label className={labelClass}>
            <span className={labelTextClass}>DoH</span>
            <input type="text" value={config.doh} onChange={(event) => onFieldChange("doh", event.target.value)} className={inputClass} />
          </label>
          <label className={`flex items-center justify-between gap-4 border-y border-[hsl(var(--border))] py-3 ${busy ? "cursor-wait" : "cursor-pointer"}`}>
            <div>
              <div className="text-[13px] font-medium text-[hsl(var(--foreground))]">启用面板登录</div>
              <div className="mt-0.5 text-[12px] text-[hsl(var(--muted-foreground))]">关闭后访问面板无需登录</div>
            </div>
            <Switch checked={config.panelAuthEnabled} disabled={busy} onChange={(value) => onFieldChange("panelAuthEnabled", value)} />
          </label>
          <label className={labelClass}>
            <span className={labelTextClass}>用户名</span>
            <input type="text" autoComplete="off" value={config.panelUsername} onChange={(event) => onFieldChange("panelUsername", event.target.value)} className={inputClass} />
          </label>
          <label className={labelClass}>
            <span className={labelTextClass}>密码</span>
            <input type="password" autoComplete="new-password" placeholder="留空不修改" value={config.panelPassword} onChange={(event) => onFieldChange("panelPassword", event.target.value)} className={inputClass} />
          </label>
          <button className={`${primaryButtonClass} mt-1 min-h-[38px] w-full`} disabled={busy} type="submit">保存配置</button>
        </form>
      </section>
    </aside>
  );
}
