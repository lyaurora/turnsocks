import type { ServerInfo } from "../types/panel";

export const ms = (value?: number) => Number.isFinite(value) ? `${value!.toFixed(1)}ms` : "-";
export const msNum = (value?: number) => Number.isFinite(value) ? value!.toFixed(1) : "-";
export const mbps = (value?: number) => Number.isFinite(value) ? value!.toFixed(1) : "-";

export function formatTestTime(value?: string) {
  if (!value) return "";
  const d = new Date(value);
  if (Number.isNaN(d.getTime())) return "";
  const sameYear = d.getFullYear() === new Date().getFullYear();
  return d.toLocaleString("zh-CN", { ...(sameYear ? {} : { year: "2-digit" }), month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", hour12: false });
}

export function formatRelativeTime(value?: string, now = Date.now()) {
  if (!value) return "";
  const ts = Date.parse(value);
  if (Number.isNaN(ts)) return "";
  const diffSec = Math.max(0, Math.floor((now - ts) / 1000));
  if (diffSec < 45) return "刚刚";
  const mins = Math.floor(diffSec / 60);
  if (mins < 60) return `${mins} 分钟前`;
  const hours = Math.floor(mins / 60);
  if (hours < 24) return `${hours} 小时前`;
  const days = Math.floor(hours / 24);
  if (days < 30) return `${days} 天前`;
  const months = Math.floor(days / 30);
  if (months < 12) return `${months} 个月前`;
  return `${Math.floor(days / 365)} 年前`;
}

export async function copyText(text: string): Promise<boolean> {
  if (!text) return false;
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text);
      return true;
    }
  } catch {
    // Fall back to execCommand for non-HTTPS origins (e.g. Tailscale HTTP).
  }
  try {
    const el = document.createElement("textarea");
    el.value = text;
    el.setAttribute("readonly", "");
    el.style.position = "fixed";
    el.style.top = "0";
    el.style.left = "0";
    el.style.opacity = "0";
    el.style.pointerEvents = "none";
    document.body.appendChild(el);
    el.focus();
    el.select();
    el.setSelectionRange(0, el.value.length);
    const ok = document.execCommand("copy");
    document.body.removeChild(el);
    return ok;
  } catch {
    return false;
  }
}

export function displayHost(node?: ServerInfo) {
  if (!node) return "-";
  const addr = node.addr || node.raw || "";
  if (addr.startsWith("[")) {
    const end = addr.indexOf("]");
    if (end > 0) return addr.slice(1, end);
  }
  const idx = addr.lastIndexOf(":");
  return idx > 0 ? addr.slice(0, idx) : addr;
}

export function displayPort(node?: ServerInfo) {
  if (!node) return "";
  const addr = node.addr || node.raw || "";
  const end = addr.startsWith("[") ? addr.indexOf("]") : -1;
  const idx = addr.lastIndexOf(":");
  if (idx > end && idx > 0 && idx < addr.length - 1) return addr.slice(idx + 1);
  return "";
}

export function displayDohHost(doh?: string) {
  if (!doh) return "-";
  try {
    return new URL(doh).host || doh;
  } catch {
    return doh;
  }
}

export function formatBytesMiB(bytes?: number) {
  if (!bytes || bytes <= 0) return "";
  return `${Math.round(bytes / (1024 * 1024))} MiB`;
}

export function errorMessage(error: unknown) {
  if (error instanceof Error) {
    return error.message === "Failed to fetch" ? "无法连接面板" : error.message;
  }
  return "操作失败";
}
