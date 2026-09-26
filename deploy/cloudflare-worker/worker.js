/**
 * Personal Game — control-plane edge relay (Cloudflare Worker).
 *
 * PURPOSE (control path only):
 *
 *   Windows client / Kaggle node
 *     -> https://<worker>/personal-game/v1/...
 *     -> this Worker rewrites the path onto ONE configured upstream
 *     -> Go control plane (internal/control/api)
 *
 * The Go control plane API remains authoritative; this Worker invents no
 * API semantics. It is a fixed-origin reverse proxy for /personal-game/*.
 *
 * HARD BOUNDARY: this Worker is NEVER in the Moonlight media path. No
 * video, audio, input, gamepad, or raw game TCP/UDP flows through here —
 * Moonlight connects to the node's advertised media endpoint directly
 * (Tailscale / Cloudflare private network / direct). See
 * docs/operations/media-networks.md and ADR-0017.
 *
 * SECURITY POSTURE (no-password development mode):
 *   - exactly one configured upstream (CONTROL_PLANE_ORIGIN env); client
 *    -supplied destinations are impossible
 *   - only GET/HEAD/POST/PUT/DELETE/OPTIONS forwarded
 *   - Authorization forwarded opaquely to the Go control plane (which
 *     enforces PG_API_TOKEN); never logged, never echoed
 *   - request bodies capped (default 10 MB), no streaming upstream bodies
 *     retained; upstream errors are summarized, not leaked
 *   - root and health endpoints identify the relay without exposing env
 *
 * Configuration (wrangler.toml [vars] or `wrangler secret put`):
 *   CONTROL_PLANE_ORIGIN  e.g. "https://control.example.internal"
 *   RELAY_MAX_BODY_BYTES  optional, default 10485760
 */

const RELAY_PREFIX = "/personal-game";
const MAX_BODY_DEFAULT = 10 * 1024 * 1024;

// Methods this relay understands. WebDAV/TRACE/CONNECT and other exotic
// verbs are rejected: the Go control API uses none of them.
const FORWARDED_METHODS = new Set([
  "GET", "HEAD", "POST", "PUT", "DELETE", "OPTIONS",
]);

function json(body, status, extraHeaders = {}) {
  return new Response(JSON.stringify(body), {
    status,
    headers: {
      "Content-Type": "application/json",
      "X-Personal-Game-Relay": "cloudflare-worker",
      ...extraHeaders,
    },
  });
}

function healthBody(upstream) {
  return {
    relay: "ok",
    upstream, // "ok" | "unreachable" | "misconfigured"
    project: "personal-game",
  };
}

/** True when the origin env is a plausible https control-plane origin. */
function originConfigured(env) {
  const origin = (env.CONTROL_PLANE_ORIGIN || "").trim();
  if (!origin) return "";
  try {
    const u = new URL(origin);
    if (u.protocol !== "https:" && u.protocol !== "http:") return "";
    return origin.replace(/\/+$/, "");
  } catch {
    return "";
  }
}

export default {
  async fetch(request, env) {
    const url = new URL(request.url);
    const path = url.pathname;

    // --- Worker/root health -------------------------------------------
    if (path === "/" && (request.method === "GET" || request.method === "HEAD")) {
      return new Response("PERSONAL GAME RELAY OK\n", {
        headers: { "Content-Type": "text/plain; charset=utf-8" },
      });
    }

    // Everything else must live under the project namespace.
    if (path !== RELAY_PREFIX && !path.startsWith(RELAY_PREFIX + "/")) {
      return json({ error: "not found" }, 404);
    }

    // --- Relay health (does an upstream probe, honestly) ---------------
    if (path === RELAY_PREFIX + "/health" && request.method === "GET") {
      const origin = originConfigured(env);
      if (!origin) return json(healthBody("misconfigured"), 200);
      try {
        const probe = AbortSignal.timeout(5000); // static factory — never `new`
        const resp = await fetch(origin + "/healthz", {
          method: "GET",
          signal: probe,
          headers: { "User-Agent": "personal-game-relay/1.0" },
        });
        return json(healthBody(resp.ok ? "ok" : "error " + resp.status), 200);
      } catch {
        return json(healthBody("unreachable"), 200);
      }
    }

    // --- Reverse proxy /personal-game/v1/* -> <origin>/v1/* ------------
    if (!path.startsWith(RELAY_PREFIX + "/")) {
      return json({ error: "not found" }, 404);
    }
    if (!FORWARDED_METHODS.has(request.method)) {
      return json({ error: "method not allowed" }, 405);
    }

    const origin = originConfigured(env);
    if (!origin) {
      return json({ error: "relay misconfigured: CONTROL_PLANE_ORIGIN not set" }, 503);
    }

    let upstreamPath = path.slice(RELAY_PREFIX.length); // "/v1/..." (never empty here)
    // Normalize a client base like "<worker>/personal-game/" + "/v1/games":
    // collapse duplicate slashes so the upstream path is exactly /v1/games.
    upstreamPath = upstreamPath.replace(/\/{2,}/g, "/");
    const upstreamURL = origin + upstreamPath + url.search;

    // Body handling: OPTIONS/GET/HEAD have no body; others are capped.
    let body;
    if (request.method === "OPTIONS" || request.method === "GET" || request.method === "HEAD") {
      body = undefined;
    } else {
      const maxBody = Number(env.RELAY_MAX_BODY_BYTES || MAX_BODY_DEFAULT);
      const declared = Number(request.headers.get("Content-Length") || "0");
      if (declared > maxBody) {
        return json({ error: "payload too large" }, 413);
      }
      body = await request.arrayBuffer();
      if (body.byteLength > maxBody) {
        return json({ error: "payload too large" }, 413);
      }
    }

    // Header allowlist: forward what the Go API needs; drop edge/browser
    // noise; Authorization is forwarded verbatim for PG_API_TOKEN.
    const headers = new Headers();
    for (const name of ["Content-Type", "Accept", "Authorization", "User-Agent"]) {
      const v = request.headers.get(name);
      if (v) headers.set(name, v);
    }
    if (body !== undefined && body.byteLength > 0 && !headers.has("Content-Type")) {
      headers.set("Content-Type", "application/json");
    }

    let upstreamResp;
    try {
      const timeout = AbortSignal.timeout(30000);
      upstreamResp = await fetch(upstreamURL, {
        method: request.method,
        headers,
        body,
        signal: timeout,
        // Workers runtimes reject redirect:"error"; "manual" stops the
        // relay from silently following upstream redirects elsewhere.
        redirect: "manual",
      });
      if (upstreamResp.status >= 300 && upstreamResp.status < 400) {
        return json({ error: "control plane returned a redirect (refused)" }, 502);
      }
    } catch {
      return json({ error: "control plane unavailable" }, 502);
    }

    // Response pass-through with its body; strip hop-by-hop noise by
    // rebuilding a minimal header set (Go API sets Content-Type).
    const out = new Headers({
      "Content-Type": upstreamResp.headers.get("Content-Type") || "application/json",
      "X-Personal-Game-Relay": "cloudflare-worker",
      "Cache-Control": "no-store",
    });
    return new Response(upstreamResp.body, { status: upstreamResp.status, headers: out });
  },
};
