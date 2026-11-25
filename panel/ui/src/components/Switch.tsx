export function Switch({ checked, disabled, onChange }: { checked: boolean; disabled?: boolean; onChange: (value: boolean) => void }) {
  return (
    <span className={`relative inline-block h-[22px] w-[38px] flex-none ${disabled ? "opacity-55" : ""}`}>
      <input
        type="checkbox"
        className="peer sr-only"
        checked={checked}
        disabled={disabled}
        onChange={(event) => onChange(event.target.checked)}
      />
      <span className={`absolute inset-0 rounded-full transition-colors peer-focus-visible:ring-2 peer-focus-visible:ring-[hsl(var(--ring))] peer-focus-visible:ring-offset-2 peer-focus-visible:ring-offset-[hsl(var(--card))] ${checked ? "bg-[hsl(var(--primary))]" : "bg-[hsl(var(--input))]"}`} />
      <span className={`absolute left-[2px] top-[2px] h-[18px] w-[18px] rounded-full bg-white shadow-[0_1px_2px_rgba(0,0,0,0.28)] transition-transform ${checked ? "translate-x-[16px]" : "translate-x-0"}`} />
    </span>
  );
}
