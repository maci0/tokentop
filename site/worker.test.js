// Copyright (C) 2026 Marcel W. Wysocki
// SPDX-License-Identifier: MIT

// Run with `bun test site/` from the repository root (no other deps needed).

import { expect, test } from "bun:test";

import worker from "./worker.js";

const ORIGIN = "https://toktop.ai";
const call = (headers = {}, init = {}) =>
  worker.fetch(
    new Request(ORIGIN + (init.path ?? "/"), {
      method: init.method ?? "GET",
      headers,
    }),
  );

const identityBody = await call().then((r) => r.text());

async function decompress(bytes, format) {
  const out = await new Response(
    new Response(bytes).body.pipeThrough(new DecompressionStream(format)),
  ).arrayBuffer();
  return new TextDecoder().decode(out);
}

test("no accept-encoding: identity body, no content-encoding", async () => {
  const res = await call();
  expect(res.status).toBe(200);
  expect(res.headers.get("content-encoding")).toBeNull();
  expect(await res.text()).toBe(identityBody);
});

test("single-coding clients get a body that decompresses to the page", async () => {
  for (const [ae, format] of [
    ["gzip", "gzip"],
    ["zstd", "zstd"],
    ["br", "brotli"],
  ]) {
    const res = await call({ "accept-encoding": ae });
    expect(res.status).toBe(200);
    expect(res.headers.get("content-encoding")).toBe(ae);
    const bytes = new Uint8Array(await res.arrayBuffer());
    expect(await decompress(bytes, format)).toBe(identityBody);
  }
});

test("brotli-capable clients are served br, not gzip", async () => {
  for (const ae of ["br", "gzip, deflate, br", "gzip, deflate, br, zstd"]) {
    const res = await call({ "accept-encoding": ae });
    expect(res.status).toBe(200);
    expect(res.headers.get("content-encoding")).toBe("br");
    const bytes = new Uint8Array(await res.arrayBuffer());
    expect(await decompress(bytes, "brotli")).toBe(identityBody);
  }
});

test("accept-encoding variants negotiate correctly", async () => {
  for (const [ae, want] of [
    ["gzip", "gzip"],
    ["GZIP", "gzip"],
    ["*", "br"],
    ["gzip;q=0.5, br", "br"],
    ["br;q=0.1, gzip", "gzip"],
    ["br", "br"],
    ["zstd", "zstd"],
    ["gzip;q=0", null],
    ["deflate", null],
  ]) {
    const res = await call({ "accept-encoding": ae });
    expect(res.headers.get("content-encoding")).toBe(want);
    if (want == null) expect(await res.text()).toBe(identityBody);
  }
});

test("every variant carries Vary: Accept-Encoding", async () => {
  const fresh = await call({ "accept-encoding": "gzip" });
  expect(fresh.headers.get("vary")).toBe("Accept-Encoding");
  const plain = await call();
  expect(plain.headers.get("vary")).toBe("Accept-Encoding");
  const etag = plain.headers.get("etag");
  const revalidated = await call({ "if-none-match": etag });
  expect(revalidated.status).toBe(304);
  expect(revalidated.headers.get("vary")).toBe("Accept-Encoding");
});

test("weak ETag round trip, including validators from older strong-form deploys", async () => {
  const etag = (await call()).headers.get("etag");
  expect(etag.startsWith('W/"')).toBe(true);
  for (const echoed of [etag, etag.slice(2), `"x", ${etag}`, "*"]) {
    const res = await call({ "if-none-match": echoed });
    expect(res.status).toBe(304);
    expect(res.headers.get("etag")).toBe(etag);
    expect(await res.text()).toBe("");
  }
});

test("changed validator re-sends the body", async () => {
  const res = await call({ "if-none-match": '"deadbeef"' });
  expect(res.status).toBe(200);
  expect(await res.text()).toBe(identityBody);
});

test("HEAD answers with page headers and no body", async () => {
  const res = await call({}, { method: "HEAD" });
  expect(res.status).toBe(200);
  expect(res.headers.get("content-type")).toContain("text/html");
  expect(res.headers.get("content-encoding")).toBeNull();
  expect((await res.arrayBuffer()).byteLength).toBe(0);
});

test("HEAD with Accept-Encoding matches GET's coding and length, with no body", async () => {
  const get = await call({ "accept-encoding": "gzip, deflate, br" });
  const head = await call({ "accept-encoding": "gzip, deflate, br" }, { method: "HEAD" });
  expect(head.status).toBe(200);
  expect(head.headers.get("content-encoding")).toBe(get.headers.get("content-encoding"));
  expect(head.headers.get("content-length")).toBe(get.headers.get("content-length"));
  expect((await head.arrayBuffer()).byteLength).toBe(0);
});

const SECURITY_HEADER_NAMES = [
  "content-security-policy",
  "x-content-type-options",
  "strict-transport-security",
  "referrer-policy",
];

test("non-GET methods and /health keep their contract", async () => {
  const denied = await call({}, { method: "POST" });
  expect(denied.status).toBe(405);
  expect(denied.headers.get("allow")).toBe("GET, HEAD");
  const health = await call({}, { path: "/health" });
  expect(health.status).toBe(200);
  expect(health.headers.get("cache-control")).toBe("no-store");
  const healthHead = await call({}, { method: "HEAD", path: "/health" });
  expect(healthHead.status).toBe(200);
  expect(healthHead.headers.get("content-type")).toBe(health.headers.get("content-type"));
  expect(healthHead.headers.get("content-length")).toBe(health.headers.get("content-length"));
  expect((await healthHead.arrayBuffer()).byteLength).toBe(0);
  for (const res of [denied, health, healthHead]) {
    for (const name of SECURITY_HEADER_NAMES) {
      expect(res.headers.get(name)).not.toBeNull();
    }
  }
});

test("every page answer carries the security headers, not only revalidations", async () => {
  const fresh = await call();
  const gzipped = await call({ "accept-encoding": "gzip" });
  const brotli = await call({ "accept-encoding": "br" });
  const revalidated = await call({ "if-none-match": fresh.headers.get("etag") });
  expect(revalidated.status).toBe(304);
  for (const res of [fresh, gzipped, brotli, revalidated]) {
    for (const name of SECURITY_HEADER_NAMES) {
      expect(res.headers.get(name)).not.toBeNull();
    }
  }
});

const IMAGE_CACHE = "public, max-age=86400, stale-while-revalidate=604800";

function assetsEnv(body = new Uint8Array([1, 2, 3, 4])) {
  return {
    ASSETS: {
      fetch: () =>
        new Response(body, {
          status: 200,
          headers: { "content-type": "image/png" },
        }),
    },
  };
}

test("dashboard images take cache and security headers from the worker", async () => {
  const body = new Uint8Array([1, 2, 3, 4]);
  const env = assetsEnv(body);
  for (const path of ["/dashboard.png", "/dashboard.webp"]) {
    const res = await worker.fetch(new Request(ORIGIN + path), env);
    expect(res.status).toBe(200);
    expect(res.headers.get("cache-control")).toBe(IMAGE_CACHE);
    expect(res.headers.get("content-type")).toBe("image/png");
    for (const name of SECURITY_HEADER_NAMES) {
      expect(res.headers.get(name)).not.toBeNull();
    }
    expect(new Uint8Array(await res.arrayBuffer())).toEqual(body);
  }
});

test("dashboard image HEAD is bodyless with the same headers as GET", async () => {
  const env = assetsEnv();
  const get = await worker.fetch(new Request(ORIGIN + "/dashboard.png"), env);
  const head = await worker.fetch(
    new Request(ORIGIN + "/dashboard.png", { method: "HEAD" }),
    env,
  );
  expect(head.status).toBe(200);
  expect(head.headers.get("cache-control")).toBe(get.headers.get("cache-control"));
  expect((await head.arrayBuffer()).byteLength).toBe(0);
  for (const name of SECURITY_HEADER_NAMES) {
    expect(head.headers.get(name)).not.toBeNull();
  }
});

test("dashboard images reject non-GET/HEAD without fetching assets", async () => {
  let fetched = false;
  const env = {
    ASSETS: {
      fetch: () => {
        fetched = true;
        return new Response("no");
      },
    },
  };
  const res = await worker.fetch(
    new Request(ORIGIN + "/dashboard.png", { method: "POST" }),
    env,
  );
  expect(res.status).toBe(405);
  expect(res.headers.get("allow")).toBe("GET, HEAD");
  expect(fetched).toBe(false);
  for (const name of SECURITY_HEADER_NAMES) {
    expect(res.headers.get(name)).not.toBeNull();
  }
});

test("served HTML does not carry source comments", () => {
  expect(identityBody.includes("<!--")).toBe(false);
  expect(identityBody.includes("/*")).toBe(false);
});

test("hero is the captured dashboard, not an ASCII stand-in", () => {
  expect(identityBody.includes("<picture>")).toBe(true);
  expect(identityBody.includes("/dashboard.webp")).toBe(true);
  expect(identityBody.includes('src="/dashboard.png"')).toBe(true);
  expect(identityBody.includes("https://toktop.ai/dashboard.png")).toBe(true);
});

test("section titles are sentence case on the body scale, not marketing labels", () => {
  expect(identityBody.includes("text-transform")).toBe(false);
  expect(identityBody.includes("letter-spacing")).toBe(false);
  expect(identityBody.includes("max-width: 62ch")).toBe(true);
});

// RFC 6928 initcwnd: ten ~1460-byte segments (~14 KB). Identity bytes plus
// inline CSS are everything there is, so staying under this keeps first paint
// at one round trip. Exact sizes are the record: a copy or compression
// change that grows the payload fails here instead of hiding under the
// window ceiling.
test("recorded transfer sizes stay inside the initial congestion window", async () => {
  const budget = 10 * 1460;
  const identity = new Uint8Array(await (await call()).arrayBuffer()).byteLength;
  const gzipped = new Uint8Array(
    await (await call({ "accept-encoding": "gzip" })).arrayBuffer(),
  ).byteLength;
  const brotli = new Uint8Array(
    await (await call({ "accept-encoding": "br" })).arrayBuffer(),
  ).byteLength;
  expect(identity).toBe(6399);
  expect(gzipped).toBe(2674);
  expect(brotli).toBe(2162);
  expect(identity).toBeLessThan(budget);
  expect(gzipped).toBeLessThan(budget);
  expect(brotli).toBeLessThan(budget);
});
