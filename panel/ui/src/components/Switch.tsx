export function Switch({ checked, disabled, onChange }: { checked: boolean; disabled?: boolean; onChange: (value: boolean) => void }) {
  return (
    <span className={`relative inline-block h-[21px] w-[37px] flex-none ${disabled ? "opacity-55" : ""}`}>
      <input
        type="checkbox"
        className="peer sr-only"
        checked={checked}
        disabled={disabled}
        onChange={(event) => onChange(event.target.checked)}
      />
      <span className="absolute inset-0 rounded-full bg-[hsl(var(--border))] transition-colors peer-checked:bg-[hsl(var(--primary))] peer-focus-visible:ring-2 peer-focus-visible:ring-[hsl(var(--ring))] peer-focus-visible:ring-offset-2 peer-focus-visible:ring-offset-[hsl(var(--card))]" />
      <span className="absolute left-[2px] top-[2px] h-[17px] w-[17px] rounded-full bg-white shadow-[0_1px_2px_rgba(0,0,0,0.3)] transition-transform peer-checked:translate-x-[16px]" />
    </span>
  );
}
