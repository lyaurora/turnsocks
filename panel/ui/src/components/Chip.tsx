import type { ReactNode } from "react";

export function Chip({ children, active, warn, danger }: { children: ReactNode; active?: boolean; warn?: boolean; danger?: boolean }) {
  let colorClass = "border-[hsl(var(--border))] bg-[hsl(var(--card))] text-[hsl(var(--muted-foreground))]";
  if (active) colorClass = "border-[hsl(var(--ok))]/30 bg-[hsl(var(--ok))]/10 text-[hsl(var(--ok))]";
  if (warn) colorClass = "border-[hsl(var(--warn))]/30 bg-[hsl(var(--warn))]/10 text-[hsl(var(--warn))]";
  if (danger) colorClass = "border-[hsl(var(--danger))]/30 bg-[hsl(var(--danger))]/10 text-[hsl(var(--danger))]";
  return (
    <span className={`inline-flex min-h-[30px] items-center justify-center rounded-full border px-[11px] pt-[2px] font-mono text-[11px] uppercase leading-none tracking-[0.12em] whitespace-nowrap ${colorClass}`}>
      {children}
    </span>
  );
}

export function IconDot() {
  return <span className="h-2 w-2 rounded-full bg-current" />;
}
