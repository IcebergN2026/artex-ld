// Minimal CDP screenshot: navigate, wait for SPA render, capture.
// Usage: node cdp-shot.js <url> <out.png> [waitMs]
const { spawn } = require("child_process");
const fs = require("fs");

const url = process.argv[2];
const out = process.argv[3];
const waitMs = parseInt(process.argv[4] || "8000", 10);
const CHROME = "C:/Program Files/Google/Chrome/Application/chrome.exe";
const PORT = 9223;

const chrome = spawn(CHROME, [
  "--headless", "--disable-gpu", "--no-first-run",
  `--remote-debugging-port=${PORT}`,
  "--user-data-dir=/tmp/chrome-cdp-profile",
  "--window-size=1500,950",
  "about:blank",
], { stdio: "ignore" });

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function getWsUrl() {
  for (let i = 0; i < 40; i++) {
    try {
      const res = await fetch(`http://127.0.0.1:${PORT}/json`);
      const targets = await res.json();
      const page = targets.find((t) => t.type === "page");
      if (page) return page.webSocketDebuggerUrl;
    } catch {}
    await sleep(500);
  }
  throw new Error("chrome CDP not ready");
}

(async () => {
  const ws = new WebSocket(await getWsUrl());
  let id = 0;
  const pending = new Map();
  const send = (method, params = {}) =>
    new Promise((resolve, reject) => {
      const mid = ++id;
      pending.set(mid, { resolve, reject });
      ws.send(JSON.stringify({ id: mid, method, params }));
    });
  const loaded = new Promise((resolve) => {
    ws.addEventListener("message", (ev) => {
      const msg = JSON.parse(ev.data);
      if (msg.id && pending.has(msg.id)) {
        const p = pending.get(msg.id);
        pending.delete(msg.id);
        msg.error ? p.reject(new Error(msg.error.message)) : p.resolve(msg.result);
      } else if (msg.method === "Page.loadEventFired") {
        resolve();
      }
    });
  });
  await new Promise((r) => (ws.onopen = r));
  await send("Page.enable");
  await send("Runtime.enable");
  await send("Emulation.setDeviceMetricsOverride", { width: 1500, height: 950, deviceScaleFactor: 1, mobile: false });
  const token = process.argv[5];
  if (token) {
    // 先到源站写 localStorage，再跳目标页（SPA 从 localStorage 读 artex_token）。
    await send("Page.navigate", { url: new URL(url).origin + "/" });
    await sleep(2500);
    await send("Runtime.evaluate", { expression: `localStorage.setItem("artex_token", ${JSON.stringify(token)})` });
  }
  await send("Page.navigate", { url });
  await Promise.race([loaded, sleep(15000)]);
  await sleep(waitMs); // SPA 渲染 + 数据拉取
  const preShot = process.argv[6]; // 截图前执行的 JS（如点击 tab），再额外等 5s
  if (preShot) {
    const r = await send("Runtime.evaluate", { expression: preShot, returnByValue: true });
    console.log("preShot result:", JSON.stringify(r.result));
    await sleep(5000);
  }
  const shot = await send("Page.captureScreenshot", { format: "png" });
  fs.writeFileSync(out, Buffer.from(shot.data, "base64"));
  console.log("saved", out, fs.statSync(out).size, "bytes");
  ws.close();
  chrome.kill("SIGKILL");
  process.exit(0);
})().catch((e) => {
  console.error("FAIL:", e.message);
  chrome.kill("SIGKILL");
  process.exit(1);
});
