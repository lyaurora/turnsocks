import type { PanelState } from "../types/panel";

async function readJSON<T>(res: Response): Promise<T> {
  if (res.status === 401) {
    window.location.href = "/login";
    throw new Error("请先登录面板");
  }
  const data = await res.json().catch(() => ({ ok: false, message: "请求失败" }));
  if (!res.ok || data.ok === false) {
    throw new Error(data.message || "请求失败");
  }
  return data as T;
}

export async function getState() {
  const res = await fetch("/api/state");
  return readJSON<PanelState>(res);
}

export async function postJSON<T>(path: string, body?: unknown) {
  const res = await fetch(path, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify(body || {})
  });
  return readJSON<T>(res);
}
