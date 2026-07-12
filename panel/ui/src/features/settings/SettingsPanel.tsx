import type { FormEvent } from "react";
import { Chip } from "../../components/Chip";
import { inputClass, labelClass, labelTextClass, primaryButtonClass } from "../../controlClasses";
import type { ConfigForm, PanelState } from "../../types/panel";

type Props = {
  state: PanelState;
  config: ConfigForm;
  busy: boolean;
  onSubmit: (event: FormEvent) => void;
  onFieldChange: <K extends keyof ConfigForm>(key: K, value: ConfigForm[K]) => void;
};

export function SettingsPanel({ state, config, busy, onSubmit, onFieldChange }: Props) {
  return (
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
        <form className="grid gap-[10px] p-4 md:p-[18px]" onSubmit={onSubmit}>
          <label className={labelClass}>
            <span className={labelTextClass}>SOCKS5</span>
            <input type="text" value={config.listen} onChange={(event) => onFieldChange("listen", event.target.value)} className={inputClass} />
          </label>
          <label className={labelClass}>
            <span className={labelTextClass}>DOH</span>
            <input type="text" value={config.doh} onChange={(event) => onFieldChange("doh", event.target.value)} className={inputClass} />
          </label>
          <label className="inline-flex min-h-[34px] cursor-pointer items-center gap-[10px]">
            <input type="checkbox" checked={config.panelAuthEnabled} onChange={(event) => onFieldChange("panelAuthEnabled", event.target.checked)} className="h-[18px] w-[18px] cursor-pointer accent-[hsl(var(--primary))]" />
            <span className="text-[13px] text-[hsl(var(--muted-foreground))]">面板登录</span>
          </label>
          <label className={labelClass}>
            <span className={labelTextClass}>用户</span>
            <input type="text" value={config.panelUsername} onChange={(event) => onFieldChange("panelUsername", event.target.value)} className={inputClass} />
          </label>
          <label className={labelClass}>
            <span className={labelTextClass}>密码</span>
            <input type="password" placeholder="留空不修改" value={config.panelPassword} onChange={(event) => onFieldChange("panelPassword", event.target.value)} className={inputClass} />
          </label>
          <button className={`${primaryButtonClass} mt-[10px] min-h-[42px] w-full`} disabled={busy} type="submit">保存配置</button>
        </form>
      </section>
    </aside>
  );
}
