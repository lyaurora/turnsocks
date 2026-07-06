import { useCallback, useEffect, useState } from "react";
import { getState, postJSON } from "../api/client";
import { errorMessage } from "../lib/format";
import type { ApiResponse, ConfigForm, PanelState, ServerTest } from "../types/panel";

const emptyState: PanelState = {
  listen: "",
  doh: "",
  panelUsername: "",
  panelAuthEnabled: false,
  servers: [],
  service: { active: false }
};

let toastTimer: number | undefined;

export function usePanel() {
  const [state, setState] = useState<PanelState>(emptyState);
  const [config, setConfig] = useState<ConfigForm>({ listen: "", doh: "", panelAuthEnabled: false, panelUsername: "", panelPassword: "" });
  const [settingsDirty, setSettingsDirty] = useState(false);
  const [testing, setTesting] = useState<Set<string>>(new Set());
  const [busy, setBusy] = useState(false);
  const [toast, setToast] = useState("");

  const showToast = useCallback((message: string) => {
    setToast(message);
    window.clearTimeout(toastTimer);
    toastTimer = window.setTimeout(() => setToast(""), 2200);
  }, []);

  const syncConfig = useCallback((next: PanelState) => {
    if (settingsDirty) return;
    setConfig({
      listen: next.listen || "",
      doh: next.doh || "",
      panelAuthEnabled: !!next.panelAuthEnabled,
      panelUsername: next.panelUsername || "",
      panelPassword: ""
    });
  }, [settingsDirty]);

  const refresh = useCallback(async () => {
    const next = await getState();
    setState(next);
    syncConfig(next);
    return next;
  }, [syncConfig]);

  useEffect(() => {
    refresh().catch((err) => showToast(errorMessage(err)));
  }, [refresh, showToast]);

  useEffect(() => {
    const timer = window.setInterval(() => {
      if (!busy && testing.size === 0) {
        refresh().catch(() => {});
      }
    }, 5000);
    return () => window.clearInterval(timer);
  }, [busy, refresh, testing.size]);

  async function run(action: () => Promise<ApiResponse>) {
    if (busy) return;
    setBusy(true);
    try {
      const res = await action();
      showToast(res.message || "完成");
      await refresh();
    } catch (err) {
      showToast(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  async function testServer(server: string) {
    if (!server || testing.has(server)) return;
    setTesting((prev) => new Set(prev).add(server));
    try {
      const result = await postJSON<ServerTest>("/api/servers/test", { server });
      setState((prev) => ({
        ...prev,
        servers: prev.servers.map((item) => item.raw === server ? { ...item, test: result } : item)
      }));
      showToast(result.message || "测试完成");
    } catch (err) {
      setState((prev) => ({
        ...prev,
        servers: prev.servers.map((item) => item.raw === server ? { ...item, test: { ok: false, message: errorMessage(err), testedAt: new Date().toISOString() } } : item)
      }));
      showToast(errorMessage(err));
    } finally {
      setTesting((prev) => {
        const next = new Set(prev);
        next.delete(server);
        return next;
      });
    }
  }

  return {
    state,
    config,
    setConfig,
    settingsDirty,
    setSettingsDirty,
    testing,
    busy,
    toast,
    run,
    refresh,
    showToast,
    testServer
  };
}
