import { ReactNode, ButtonHTMLAttributes, InputHTMLAttributes } from "react";

export const topButtonClass = "h-[32px] inline-flex items-center justify-center rounded-[var(--radius)] border border-[hsl(var(--border))] bg-[hsl(var(--background))] px-3 text-[13px] font-medium text-[hsl(var(--foreground))] shadow-sm transition-all hover:bg-[hsl(var(--accent))] hover:text-[hsl(var(--accent-foreground))] active:scale-[0.98] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[hsl(var(--ring))/0.3] disabled:pointer-events-none disabled:opacity-50";
export const smallButtonClass = "h-[28px] inline-flex items-center justify-center rounded-[var(--radius)] border border-[hsl(var(--border))] bg-[hsl(var(--background))] px-2.5 text-[13px] font-medium text-[hsl(var(--foreground))] shadow-sm transition-all hover:bg-[hsl(var(--accent))] hover:text-[hsl(var(--accent-foreground))] active:scale-[0.98] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[hsl(var(--ring))/0.3] disabled:pointer-events-none disabled:opacity-50";
export const primaryButtonClass = "h-[32px] inline-flex items-center justify-center rounded-[var(--radius)] bg-[hsl(var(--primary))] px-3 text-[13px] font-medium text-[hsl(var(--primary-foreground))] shadow-sm transition-all hover:bg-[hsl(var(--primary))/0.9] active:scale-[0.98] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[hsl(var(--ring))/0.4] focus-visible:ring-offset-1 focus-visible:ring-offset-[hsl(var(--background))] disabled:pointer-events-none disabled:opacity-50";
export const dangerButtonClass = "h-[28px] inline-flex items-center justify-center rounded-[var(--radius)] border border-[hsl(var(--destructive))/0.2] bg-[hsl(var(--background))] px-2.5 text-[13px] font-medium text-[hsl(var(--destructive))] shadow-sm transition-all hover:bg-[hsl(var(--destructive))/10] hover:border-[hsl(var(--destructive))/0.3] active:scale-[0.98] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[hsl(var(--destructive))/0.3] disabled:pointer-events-none disabled:opacity-50";

export const inputClass = "flex h-[36px] w-full rounded-[var(--radius)] border border-[hsl(var(--input))] bg-[hsl(var(--card))] px-3 py-1 text-sm shadow-sm transition-all placeholder:text-[hsl(var(--muted-foreground))]/70 focus-visible:outline-none focus-visible:border-[hsl(var(--ring))] focus-visible:ring-2 focus-visible:ring-[hsl(var(--ring))/0.2] disabled:cursor-not-allowed disabled:opacity-50 font-mono";

export const labelClass = "flex flex-col gap-1.5";
export const labelTextClass = "text-[12px] font-medium leading-none text-[hsl(var(--foreground))] opacity-90";

export function WindowSection({ children, header, right, className = "" }: { children: ReactNode; header?: ReactNode; right?: ReactNode; className?: string }) {
  return (
    <section className={`flex flex-col border border-[hsl(var(--border))] rounded-[10px] overflow-hidden bg-[hsl(var(--card))] shadow-[0_2px_8px_-2px_rgba(0,0,0,0.04)] ${className}`}>
      {(header || right) && (
        <div className="flex h-[54px] items-center justify-between border-b border-[hsl(var(--border))] px-5 bg-[hsl(var(--muted))/0.5]">
          <strong className="text-[13px] font-semibold text-[hsl(var(--foreground))] tracking-wide">{header}</strong>
          {right && <div className="flex items-center gap-2">{right}</div>}
        </div>
      )}
      {children}
    </section>
  );
}

export function Button({ className, variant = "small", ...props }: ButtonHTMLAttributes<HTMLButtonElement> & { variant?: "top" | "small" | "primary" | "danger" }) {
  let baseClass = "";
  if (variant === "top") baseClass = topButtonClass;
  else if (variant === "small") baseClass = smallButtonClass;
  else if (variant === "primary") baseClass = primaryButtonClass;
  else if (variant === "danger") baseClass = dangerButtonClass;

  return <button className={`${baseClass} ${className || ""}`} type={props.type || "button"} {...props} />;
}

export function Input({ className, ...props }: InputHTMLAttributes<HTMLInputElement>) {
  return <input className={`${inputClass} ${className || ""}`} {...props} />;
}
