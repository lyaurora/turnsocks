import type { ReactNode } from "react";

export function Chip({ children, active, accent, warn, danger, mono }: { children: ReactNode; active?: boolean; accent?: boolean; warn?: boolean; danger?: boolean; mono?: boolean }) {
  let colorClass = "border-[hsl(var(--border))] bg-[hsl(var(--muted))] text-[hsl(var(--muted-foreground))]";
  if (active) colorClass = "border-transparent bg-[hsl(var(--ok))]/10 text-[hsl(var(--ok))]";
  if (accent) colorClass = "border-transparent bg-[hsl(var(--primary))]/10 text-[hsl(var(--primary))]";
  if (warn) colorClass = "border-transparent bg-[hsl(var(--warn))]/12 text-[hsl(var(--warn))]";
  if (danger) colorClass = "border-transparent bg-[hsl(var(--danger))]/10 text-[hsl(var(--danger))]";
  return (
    <span className={`inline-flex min-h-[24px] items-center gap-1.5 whitespace-nowrap rounded-[7px] border px-2 leading-none ${mono ? "font-mono text-[11.5px]" : "text-[12px] font-medium"} ${colorClass}`}>
      {active && <span className="h-1.5 w-1.5 rounded-full bg-current" />}
      {children}
    </span>
  );
}
