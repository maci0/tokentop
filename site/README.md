# toktop.ai

One Worker, one page, no build step.

```sh
bunx wrangler deploy          # from this directory
```

`worker.js` holds the whole site: the HTML is a template literal, so there is
nothing to bundle and nothing to keep in sync with a static host. The Worker
answers every path with the page (a one-page site should not 404 on a typo)
except `/health`, which returns `ok` for uptime checks.

The page carries an ETag derived from its own bytes: reloads and visits
past the five-minute freshness window answer with an empty 304 instead of
resending the body, and `stale-while-revalidate` lets returning browsers paint
from their copy while that check runs. The validator is weak (`W/`) because
the page ships in several encodings under one URL, which one strong tag may
not span.

## Encoding

Clients that advertise brotli, zstd, or gzip get a body that was compressed
once when the isolate started, not once per request; clients that advertise
none of those get the identity bytes. Among the encodings a client accepts,
the smallest body at the highest q-value wins, so a typical `gzip, deflate,
br, zstd` request is answered with brotli rather than gzip. Source comments
in the HTML and CSS stay in `worker.js` and are stripped before the page is
hashed, compressed, or sent. Every response carries `Vary: Accept-Encoding`,
so caches never hand a compressed body to a client that cannot decode it.

## Performance budget

One request for the page, no JavaScript, no webfonts, inline CSS only. The
hero is the real dashboard capture (WebP ~140 KB, PNG fallback), served from
this Worker so a deploy updates share cards and the page together. Measured
against the current source: 6,399 bytes identity / 2,674 gzip / 2,162 brotli
for the HTML, still inside the ~14 KB initial congestion window. The budget
is pinned by a test, so drift fails `bun test site/`; numbers above are
re-measurable with it:

```sh
bun test site/    # or `make site-check` from the repo root
```

The bun version is pinned in `.bun-version`; CI reads the same file.

Routing is by custom domain (`toktop.ai`, `www.toktop.ai`) rather than a
route pattern, so Cloudflare manages the DNS record for both names. The zone's
MX and SPF records are Namecheap's email forwarding and are left alone.
