import { ReactNode } from "react";

export function Chip({ children, active, warn }: { children: ReactNode; active?: boolean; warn?: boolean }) {
  let style = "border-[hsl(var(--border))] text-[hsl(var(--muted-foreground))]";
  if (active) style = "border-[hsl(var(--ok))]/30 bg-[hsl(var(--ok))]/10 text-[hsl(var(--ok))]";
  else if (warn) style = "border-[hsl(var(--warn))]/30 bg-[hsl(var(--warn))]/10 text-[hsl(var(--warn))]";

  return (
    <span className={`inline-flex items-center rounded-md border px-2 py-0.5 text-[10px] font-medium transition-colors focus:outline-none focus:ring-2 focus:ring-[hsl(var(--ring))] focus:ring-offset-2 uppercase tracking-wider ${style}`}>
      {children}
    </span>
  );
}

export function IconDot() {
  return <span className="h-1.5 w-1.5 rounded-full bg-current" />;
}
