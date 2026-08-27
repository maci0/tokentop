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

One request, no JavaScript, no webfonts, inline CSS only. Measured against the
current source: 6,124 bytes identity / 2,609 gzip / 2,116 brotli - all inside
the ~14 KB initial congestion window, so first paint needs a single round
trip. Anything pulling the page past one window (a script, a font, an image)
needs a better reason than decoration. The budget is pinned by a test, so
drift fails `bun test site/`; numbers above are re-measurable with it:

```sh
bun test site/    # pins negotiation, caching and the method/health contract
```

Routing is by custom domain (`toktop.ai`, `www.toktop.ai`) rather than a
route pattern, so Cloudflare manages the DNS record for both names. The zone's
MX and SPF records are Namecheap's email forwarding and are left alone.
