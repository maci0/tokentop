# toktop.ai

One Worker, one page, no build step.

```sh
npx wrangler deploy          # from this directory
```

`worker.js` holds the whole site: the HTML is a template literal, so there is
nothing to bundle and nothing to keep in sync with a static host. The Worker
answers every path with the page (a one-page site should not 404 on a typo)
except `/health`, which returns `ok` for uptime checks.

Routing is by custom domain (`toktop.ai`, `www.toktop.ai`) rather than a
route pattern, so Cloudflare manages the DNS record for both names. The zone's
MX and SPF records are Namecheap's email forwarding and are left alone.
