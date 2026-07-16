type IconProps = { className?: string };

const base = {
  viewBox: "0 0 24 24",
  fill: "none",
  stroke: "currentColor",
  strokeLinecap: "round" as const,
  strokeLinejoin: "round" as const
};

export function IconRelay({ className = "h-4 w-4" }: IconProps) {
  return (
    <svg className={className} {...base} strokeWidth={2.2}>
      <path d="m16 3 4 4-4 4" />
      <path d="M20 7H4" />
      <path d="m8 21-4-4 4-4" />
      <path d="M4 17h16" />
    </svg>
  );
}

export function IconRefresh({ className = "h-4 w-4" }: IconProps) {
  return (
    <svg className={className} {...base} strokeWidth={1.8}>
      <path d="M3 12a9 9 0 0 1 15-6.7L21 8" />
      <path d="M21 3v5h-5" />
      <path d="M21 12a9 9 0 0 1-15 6.7L3 16" />
      <path d="M3 21v-5h5" />
    </svg>
  );
}

export function IconLogout({ className = "h-4 w-4" }: IconProps) {
  return (
    <svg className={className} {...base} strokeWidth={1.8}>
      <path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4" />
      <path d="m16 17 5-5-5-5" />
      <path d="M21 12H9" />
    </svg>
  );
}

export function IconZap({ className = "h-4 w-4" }: IconProps) {
  return (
    <svg className={className} {...base} strokeWidth={1.8}>
      <path d="m13 2-2 9h6l-8 11 2-9H5z" />
    </svg>
  );
}

export function IconPlus({ className = "h-4 w-4" }: IconProps) {
  return (
    <svg className={className} {...base} strokeWidth={2.1}>
      <path d="M12 5v14M5 12h14" />
    </svg>
  );
}

export function IconTrash({ className = "h-4 w-4" }: IconProps) {
  return (
    <svg className={className} {...base} strokeWidth={1.8}>
      <path d="M3 6h18" />
      <path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6" />
      <path d="M8 6V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2" />
      <path d="M10 11v6M14 11v6" />
    </svg>
  );
}

export function IconAlert({ className = "h-4 w-4" }: IconProps) {
  return (
    <svg className={className} {...base} strokeWidth={1.9}>
      <path d="m21.7 18-8-13.9a2 2 0 0 0-3.4 0L2.3 18a2 2 0 0 0 1.7 3h16a2 2 0 0 0 1.7-3z" />
      <path d="M12 9v4M12 17h.01" />
    </svg>
  );
}

export function IconSun({ className = "h-4 w-4" }: IconProps) {
  return (
    <svg className={className} {...base} strokeWidth={2}>
      <circle cx="12" cy="12" r="4" />
      <path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4" />
    </svg>
  );
}

export function IconMoon({ className = "h-4 w-4" }: IconProps) {
  return (
    <svg className={className} {...base} strokeWidth={2}>
      <path d="M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8z" />
    </svg>
  );
}

export function IconMonitor({ className = "h-4 w-4" }: IconProps) {
  return (
    <svg className={className} {...base} strokeWidth={2}>
      <rect x="2" y="3" width="20" height="14" rx="2" />
      <path d="M8 21h8M12 17v4" />
    </svg>
  );
}
