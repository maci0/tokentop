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

test("no accept-encoding: identity body, no content-encoding", async () => {
  const res = await call();
  expect(res.status).toBe(200);
  expect(res.headers.get("content-encoding")).toBeNull();
  expect(await res.text()).toBe(identityBody);
});

test("gzip client gets a body that decompresses to the page", async () => {
  const res = await call({ "accept-encoding": "gzip, deflate, br" });
  expect(res.status).toBe(200);
  expect(res.headers.get("content-encoding")).toBe("gzip");
  const bytes = new Uint8Array(await res.arrayBuffer());
  expect(new TextDecoder().decode(Bun.gunzipSync(bytes))).toBe(identityBody);
});

test("accept-encoding variants negotiate correctly", async () => {
  for (const [ae, wantEncoding] of [
    ["gzip", true],
    ["*", true],
    ["gzip;q=0.5, br", true],
    ["br", false],
    ["gzip;q=0", false],
    ["deflate", false],
    ["GZIP", true],
  ]) {
    const res = await call({ "accept-encoding": ae });
    const encoding = res.headers.get("content-encoding");
    if (wantEncoding) expect(encoding).toBe("gzip");
    else {
      expect(encoding).toBeNull();
      expect(await res.text()).toBe(identityBody);
    }
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
});

test("non-GET methods and /health keep their contract", async () => {
  expect((await call({}, { method: "POST" })).status).toBe(405);
  const health = await call({}, { path: "/health" });
  expect(health.status).toBe(200);
  expect(health.headers.get("cache-control")).toBe("no-store");
});

const SECURITY_HEADER_NAMES = [
  "content-security-policy",
  "x-content-type-options",
  "strict-transport-security",
  "referrer-policy",
];

test("every page answer carries the security headers, not only revalidations", async () => {
  const fresh = await call();
  const compressed = await call({ "accept-encoding": "gzip" });
  const revalidated = await call({ "if-none-match": fresh.headers.get("etag") });
  expect(revalidated.status).toBe(304);
  for (const res of [fresh, compressed, revalidated]) {
    for (const name of SECURITY_HEADER_NAMES) {
      expect(res.headers.get(name)).not.toBeNull();
    }
  }
});

// RFC 6928 initcwnd: ten ~1460-byte segments (~14 KB). Identity bytes plus
// inline CSS are everything there is, so staying under this keeps first paint
// at one round trip; measured against exactly what each encoding puts on the
// wire, so compression drift counts too.
test("both encodings stay inside the initial congestion window", async () => {
  const budget = 10 * 1460;
  const identity = new Uint8Array(await (await call()).arrayBuffer()).byteLength;
  const gzipped = new Uint8Array(
    await (await call({ "accept-encoding": "gzip" })).arrayBuffer(),
  ).byteLength;
  expect(identity).toBeLessThan(budget);
  expect(gzipped).toBeLessThan(budget);
});
