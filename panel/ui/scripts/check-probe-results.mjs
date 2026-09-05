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
  const render = (test) => renderToStaticMarkup(createElement(NodePanel, {
    state: {
      service: { active: true },
      servers: [{ raw: "turn.example:3478", addr: "turn.example:3478", hasAuth: false, current: true, test }]
    },
    serverInput: "",
    testing: new Set(),
    busy: false,
    locked: false
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

  console.log("Probe API and result display checks passed");
} finally {
  globalThis.fetch = originalFetch;
  if (originalWindow === undefined) delete globalThis.window;
  else globalThis.window = originalWindow;
  await vite.close();
}
