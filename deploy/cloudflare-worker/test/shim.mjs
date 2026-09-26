// Minimal Worker shim: loads worker.js, feeds it WHATWG-ish Request/Response
// objects, and captures upstream fetch() calls as a fake origin instead of
// hitting the network. stdin: {url, method, body, headers, env} -> stdout:
// {status, body, headers}.
//
// Mock behavior via request headers:
//   __mock_upstream__: <origin>  -> respond OK for that origin
//   __mock_fail__: "1"           -> make fetch() throw (upstream failure)
//
// health endpoints perform REAL fetches per the worker; for hermetic tests
// the shim intercepts fetch: a fetch to the configured origin/healthz
// succeeds unless __mock_fail__ or the origin is unroutable-by-mock
// (127.0.0.1:9 => treated as unreachable by the shim's connect logic).

import fs from "node:fs";
import path from "node:path";

const input = JSON.parse(fs.readFileSync(0, "utf8"));
const workerPath = path.resolve(process.cwd(), process.argv[2]);
const worker = (await import(`file://${workerPath}`)).default;

const failAll = input.headers["__mock_fail__"] === "1";
const captured = []; // every upstream fetch the worker made

function makeHeaders(obj) {
  return new Headers(obj);
}

async function handleFetch(url, init) {
  const u = new URL(url);
  const mockOrigin = input.headers["__mock_upstream__"];
  if (mockOrigin && u.origin === mockOrigin && !failAll) {
    captured.push({
      origin: u.origin,
      path: u.pathname,
      search: u.search,
      method: (init.method || "GET").toUpperCase(),
      body: init.body ? Buffer.from(init.body).toString("utf8") : null,
      headers: Object.fromEntries(new Headers(init.headers || {}).entries()),
    });
    if (u.pathname === "/healthz") {
      return new Response(JSON.stringify({ status: "ok" }), { status: 200 });
    }
    return new Response(JSON.stringify({ proxied: true }), {
      status: 200,
      headers: { "content-type": "application/json" },
    });
  }
  // Real network for unreachable-origin honesty checks (e.g. 127.0.0.1:9).
  return realFetch(url, init);
}

const realFetch = globalThis.fetch;
globalThis.fetch = (url, init) => handleFetch(String(url), init || {});

const UNDICI_SAFE = new Set(["GET","HEAD","POST","PUT","DELETE","OPTIONS","PATCH"]);
let request;
if (UNDICI_SAFE.has(input.method)) {
  const reqInit = { method: input.method, headers: input.headers };
  if (input.body && !["GET", "HEAD"].includes(input.method)) {
    reqInit.body = input.body;
  }
  request = new Request(input.url, reqInit);
} else {
  // undici refuses TRACE/CONNECT; synthesize a minimal Request-like object —
  // the worker must still reject these methods with 405 (contract test).
  request = {
    method: input.method,
    url: input.url,
    headers: new Headers(input.headers || {}),
    arrayBuffer: async () => new ArrayBuffer(0),
  };
}

const env = input.env || {};
try {
  const resp = await worker.fetch(request, env);
  const text = await resp.text();
  const outHeaders = {};
  resp.headers.forEach((v, k) => {
    outHeaders[k.toLowerCase()] = v;
  });
  if (captured.length > 0) {
    outHeaders["x-mock-upstream-url"] = JSON.stringify(captured[0]);
  }
  process.stdout.write(
    JSON.stringify({ status: resp.status, body: text, headers: outHeaders })
  );
} catch (err) {
  process.stdout.write(
    JSON.stringify({ status: 500, body: String(err), headers: {} })
  );
}
