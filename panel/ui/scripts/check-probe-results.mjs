import assert from "node:assert/strict";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { createServer } from "vite";

const vite = await createServer({ server: { middlewareMode: true, hmr: false }, logLevel: "silent" });
const originalFetch = globalThis.fetch;
const originalWindow = globalThis.window;

try {
  const { testServer, deleteServer } = await vite.ssrLoadModule("/src/api/client.ts");
  const partial = {
    ok: false,
    message: "测试未完成，下载数据不完整",
    tcpConnect: { ok: true, avgMs: 10 },
    socksUdp: { ok: true },
    singleThread: { ok: false, mbps: 200.16, bytes: 33504512, message: "下载异常：unexpected EOF" },
    multiThread: { ok: false, mbps: 355.34, bytes: 83865984, message: "下载异常：connection reset" }
  };
  globalThis.fetch = async () => Response.json(partial);
  const result = await testServer("turn.example:3478");
  assert.deepEqual(result, partial, "failed measurements must retain their data");

  const controller = new AbortController();
  globalThis.fetch = async (path, request) => {
    assert.equal(path, "/api/servers/test");
    assert.deepEqual(JSON.parse(request.body), { server: "turn.example:3478", mode: "check" });
    assert.equal(request.signal, controller.signal);
    return new Promise((_, reject) => request.signal.addEventListener("abort", () => reject(request.signal.reason), { once: true }));
  };
  const pending = testServer("turn.example:3478", "check", controller.signal);
  controller.abort();
  await assert.rejects(pending, { name: "AbortError" });

  globalThis.fetch = async () => Response.json({ ok: false, message: "节点不存在" }, { status: 400 });
  await assert.rejects(() => deleteServer("turn.example:3478"), /节点不存在/);
  globalThis.window = { location: { href: "" } };
  globalThis.fetch = async () => new Response("", { status: 401 });
  await assert.rejects(() => testServer("turn.example:3478"), /请先登录/);
  assert.equal(window.location.href, "/login");
  for (const body of ["invalid JSON", "null"]) {
    globalThis.fetch = async () => new Response(body);
    await assert.rejects(() => testServer("turn.example:3478"), /请求失败|响应格式错误/);
  }

  const { NodePanel } = await vite.ssrLoadModule("/src/features/nodes/NodePanel.tsx");
  const render = (test, check, testing = null) => renderToStaticMarkup(createElement(NodePanel, {
    state: {
      service: { active: true },
      servers: [
        { raw: "turn.example:3478", addr: "turn.example:3478", hasAuth: false, current: true, test, check },
        { raw: "other.example:3478", addr: "other.example:3478", hasAuth: false }
      ]
    },
    serverInput: "",
    testing,
    locked: !!testing
  }));
  const html = render(result);
  for (const label of ["单线程（未完成）", "多线程（未完成）"]) {
    assert.equal(html.split(label).length - 1, 2, "both the hero and node card must flag incomplete downloads");
  }
  assert.match(html, /200\.2/);
  assert.match(html, /355\.3/);
  assert.match(html, /unexpected EOF/);

  const mixed = render({ ...partial, ok: true, singleThread: { ...partial.singleThread, ok: true } });
  assert.doesNotMatch(mixed, /单线程（未完成）/);
  assert.match(mixed, /多线程（未完成）/);
  const complete = render({ ...partial, ok: true,
    singleThread: { ...partial.singleThread, ok: true }, multiThread: { ...partial.multiThread, ok: true }
  });
  assert.doesNotMatch(complete, /（未完成）/);
  const failed = render({ ok: false, message: "连接失败" });
  assert.match(failed, /连接失败/);
  assert.doesNotMatch(failed, /Mbps/);

  const check = { mode: "check", ok: false, socksTcp: { ok: true }, socksUdp: { ok: false, message: "UDP timed out" } };
  const light = render(undefined, check);
  assert.match(light, /连通性检查/);
  assert.match(light, /UDP timed out/);
  assert.doesNotMatch(light, /Mbps|单线程|多线程/);
  const combined = render(partial, check);
  assert.match(combined, /UDP timed out/);
  assert.match(combined, /200\.2/);

  const running = render(partial, check, { server: "turn.example:3478", mode: "check" });
  const buttons = [...running.matchAll(/<button\b([^>]*)>(.*?)<\/button>/gs)];
  const starts = buttons.filter(([, , content]) => /(?:检查|测速)(?:全部)?$/.test(content));
  assert.equal(starts.length, 6, "both node cards and batch actions need controls");
  for (const [, attributes] of starts) assert.match(attributes, /\sdisabled=/, "other nodes must not start competing probes");
  const stop = buttons.find(([, , content]) => content === "停止检测");
  assert.ok(stop, "a running batch must expose its stop control");
  assert.doesNotMatch(stop[1], /\sdisabled=/);

  const { SettingsPanel } = await vite.ssrLoadModule("/src/features/settings/SettingsPanel.tsx");
  const events = renderToStaticMarkup(createElement(SettingsPanel, {
    state: { servers: [], service: {}, runtime: {
      last_failure: { at: "2026-09-05T12:00:00Z", addr: "turn.example:3478", stage: "TURN TCP", message: "<private error>" },
      last_switch: { at: "2026-09-05T12:00:01Z", from: "turn.example:3478", to: "other.example:3478", reason: "自动切换" }
    } },
    config: { listen: "", doh: "", panelUsername: "", panelPassword: "", panelAuthEnabled: false },
    busy: false
  }));
  assert.match(events, /最近记录的失败/);
  assert.match(events, /&lt;private error&gt;/);
  assert.match(events, /自动切换/);
  assert.match(events, /历史记录/);

  console.log("Probe API, cancellation, result display, and runtime event checks passed");
} finally {
  globalThis.fetch = originalFetch;
  if (originalWindow === undefined) delete globalThis.window;
  else globalThis.window = originalWindow;
  await vite.close();
}
