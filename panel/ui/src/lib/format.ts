import type { ServerInfo } from "../types/panel";

export const ms = (value?: number) => Number.isFinite(value) ? `${value!.toFixed(1)}ms` : "-";
export const mbps = (value?: number) => Number.isFinite(value) ? value!.toFixed(1) : "-";

export function formatTestTime(value?: string) {
  if (!value) return "";
  const d = new Date(value);
  if (Number.isNaN(d.getTime())) return "";
  const sameYear = d.getFullYear() === new Date().getFullYear();
  return d.toLocaleString("zh-CN", { ...(sameYear ? {} : { year: "2-digit" }), month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", hour12: false });
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

export function errorMessage(error: unknown) {
  if (error instanceof Error) {
    return error.message === "Failed to fetch" ? "无法连接面板" : error.message;
  }
  return "操作失败";
}
