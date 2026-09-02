# toktop.ai

One Worker, one page, no build step.

```sh
bunx wrangler deploy          # from this directory
```

`worker.js` holds the HTML: it is a template literal, so there is nothing to
bundle. The Worker answers `/health` with `ok` for uptime checks, serves the
dashboard capture from `public/` at `/dashboard.png` and `/dashboard.webp`,
and answers every other path with the page (a one-page site should not 404
on a typo). Wrong methods are `405` with `Allow: GET, HEAD`. Image paths
without an asset binding, and 404/5xx from the asset store, are `no-store`
so a missing file is not cached as a day-long success. Error bodies are
`text/plain`, matching `/health`.

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
hero is the real dashboard capture: AVIF (~67 KB at 1920px, ~38 KB at 1280px)
then WebP (~143 KB / ~79 KB) then the PNG share-card original. `srcset` picks
1280w for phones and 1x desktops; 1920w is the 2x desktop slot. Served from
this Worker so a deploy updates share cards and the page together.
`wrangler.jsonc` sets `run_worker_first` so those image paths hit the Worker
(cache headers, HSTS, 405s) instead of Cloudflare's asset pipeline. Measured
against the current source: 6,588 bytes identity / 2,715 gzip / 2,197 brotli
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
