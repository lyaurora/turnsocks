import type { FormEvent } from "react";
import { Chip } from "../../components/Chip";
import { Switch } from "../../components/Switch";
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
        <div className="flex items-center justify-between gap-3 border-b border-[hsl(var(--border))] px-4 py-3.5 md:px-[18px]">
          <h2 className="text-[14.5px] font-semibold text-[hsl(var(--foreground))]">概览</h2>
          <Chip mono>PID {state.service.pid || "-"}</Chip>
        </div>
        <div className="grid gap-1.5 p-4 md:p-[18px]">
          <div className="text-[12.5px] font-medium text-[hsl(var(--muted-foreground))]">DoH</div>
          <div className="break-all font-mono text-[13px] leading-[1.55] text-[hsl(var(--foreground))]">{state.doh || "-"}</div>
        </div>
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
            <input type="text" value={config.panelUsername} onChange={(event) => onFieldChange("panelUsername", event.target.value)} className={inputClass} />
          </label>
          <label className={labelClass}>
            <span className={labelTextClass}>密码</span>
            <input type="password" placeholder="留空不修改" value={config.panelPassword} onChange={(event) => onFieldChange("panelPassword", event.target.value)} className={inputClass} />
          </label>
          <button className={`${primaryButtonClass} mt-1 min-h-[38px] w-full`} disabled={busy} type="submit">保存配置</button>
        </form>
      </section>
    </aside>
  );
}
