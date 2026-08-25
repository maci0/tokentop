// Copyright (C) 2026 Marcel W. Wysocki
// SPDX-License-Identifier: MIT

// toktop.ai: one page, served from the edge. The whole site is this file, so
// there is no build step, no bucket, and nothing to keep in sync.

const HTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>toktop - btop for AI</title>
<meta name="description" content="A terminal dashboard for LLM inference engines and the coding agents hammering them.">
<meta property="og:title" content="toktop">
<meta property="og:description" content="btop for AI: a terminal dashboard for LLM inference engines and the coding agents hammering them.">
<meta property="og:type" content="website">
<meta property="og:url" content="https://toktop.ai">
<!-- the icon is the h1 cursor block in --accent/--panel, not a placeholder emoji -->
<link rel="icon" href="data:image/svg+xml,<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 100 100'><rect width='100' height='100' rx='20' fill='%2311161d'/><rect x='37' y='25' width='26' height='50' rx='5' fill='%234cc38a'/></svg>">
<style>
  :root {
    --bg: #0d1117; --panel: #11161d; --line: #222b36;
    --fg: #d7dde5; --dim: #7d8895; --accent: #4cc38a; --warm: #e3b341;
    --mono: ui-monospace, "SF Mono", "JetBrains Mono", Menlo, Consolas, monospace;
  }
  @media (prefers-color-scheme: light) {
    :root {
      --bg: #fbfbf9; --panel: #ffffff; --line: #e3e3de;
      --fg: #1b1f24; --dim: #5c6570; --accent: #1a7f4b; --warm: #9a6b00;
    }
  }
  * { box-sizing: border-box; }
  body {
    margin: 0; padding: 4rem 1.25rem 5rem;
    background: var(--bg); color: var(--fg);
    font-family: var(--mono); font-size: 15px; line-height: 1.6;
  }
  main { max-width: 62rem; margin: 0 auto; }
  h1 { font-size: 2.6rem; margin: 0; letter-spacing: -0.03em; }
  h1 .cursor { color: var(--accent); animation: blink 1.2s step-end infinite; }
  @keyframes blink { 50% { opacity: 0; } }
  .tag { color: var(--dim); margin: .6rem 0 2.4rem; font-size: 1.05rem; }
  h2 { font-size: .82rem; letter-spacing: .16em; text-transform: uppercase;
       color: var(--dim); font-weight: 600; margin: 3rem 0 .9rem; }
  pre {
    background: var(--panel); border: 1px solid var(--line); border-radius: 8px;
    padding: 1rem 1.15rem; overflow-x: auto; margin: 0 0 1rem; font-size: 13.5px;
  }
  code { color: inherit; }
  .up { color: var(--accent); }
  .warm { color: var(--warm); }
  .dim { color: var(--dim); }
  ul { padding-left: 1.1rem; margin: 0; }
  li { margin-bottom: .5rem; }
  li b { font-weight: 600; }
  a { color: var(--accent); text-decoration: none; border-bottom: 1px solid transparent; }
  a:hover { border-bottom-color: currentColor; }
  footer { margin-top: 4rem; padding-top: 1.25rem; border-top: 1px solid var(--line);
           color: var(--dim); font-size: 13px; display: flex; gap: 1.5rem; flex-wrap: wrap; }
</style>
</head>
<body>
<main>
  <h1>toktop<span class="cursor">_</span></h1>
  <p class="tag"><code>btop</code> for AI: a terminal dashboard for LLM inference
  engines and the coding agents hammering them.</p>

<pre><code>ENGINES  <span class="dim">local + ssh</span>
  llama.cpp   <span class="up">▲ 42.1 tok/s</span>   kv <span class="warm">61%</span>   q 2   qwen3-30b
  vLLM        <span class="up">▲ 18.7 tok/s</span>   kv 12%   q 0   llama-3.3-70b

AGENTS  <span class="dim">read from their own session logs</span>
  claude      <span class="up">▲ 1.1k tok/s</span>   2.4k tok   <span class="up">● live</span>
  codex       <span class="up">▲ 340 tok/s</span>    18k tok    <span class="dim">idle 12s</span>
  opencode    <span class="up">▲ 612 tok/s</span>    41k tok    <span class="up">● live</span></code></pre>

  <h2>Install</h2>
<pre><code>go install github.com/maci0/toktop/cmd/toktop@latest
<span class="dim"># or grab a binary: linux / macos / windows, amd64 + arm64</span></code></pre>

  <h2>Run</h2>
<pre><code>toktop --demo             <span class="dim"># simulated fleet, works instantly</span>
toktop                    <span class="dim"># auto-discovers local engines</span>
toktop --agents           <span class="dim"># also watch coding agents on this machine</span>
toktop ssh://you@box      <span class="dim"># watch another host over ssh</span></code></pre>

  <h2>What it shows</h2>
  <ul>
    <li><b>Engines</b> found by port and process: Ollama, llama.cpp, vLLM,
      SGLang, TRT-LLM, LM Studio, MLX, KoboldCpp, LocalAI, TGI, LiteLLM,
      GPUStack, Lemonade, OmniRoute, plus a generic OpenAI-compatible
      fallback so nothing is missed.</li>
    <li><b>Agents</b> without any cooperation from them: claude, codex, qwen,
      copilot, opencode, pi, prime-agent, feynman, clanker and dsh all keep
      session logs carrying the provider's own token counts.</li>
    <li><b>Probes</b> that measure real time-to-first-token and decode speed,
      instead of guessing from averages.</li>
    <li><b>System</b> context: GPU/NPU enumeration, VRAM, temps, power, and
      KV-cache pressure next to the throughput that caused it.</li>
  </ul>

  <h2>Measured, or nothing</h2>
  <p class="dim">Every number here is one an engine or an agent actually
  reported. Nothing is estimated, interpolated, or inferred from character
  counts. An agent that reports nothing shows no rate, rather than a zero that
  reads like a measurement. An agent generating through an engine that is
  already being watched is counted once, not twice.</p>

  <footer>
    <a href="https://github.com/maci0/toktop">github.com/maci0/toktop</a>
    <span>MIT licensed</span>
    <span>no telemetry, no account, no daemon</span>
  </footer>
</main>
</body>
</html>
`;

export default {
  async fetch(request) {
    const url = new URL(request.url);
    if (request.method !== "GET" && request.method !== "HEAD") {
      return new Response("method not allowed", { status: 405, headers: { allow: "GET, HEAD" } });
    }
    if (url.pathname === "/health") {
      return new Response("ok\n", { headers: { "content-type": "text/plain; charset=utf-8" } });
    }
    // One page: anything else is that page too, rather than a 404 nobody
    // learns anything from.
    return new Response(HTML, {
      headers: {
        "content-type": "text/html; charset=utf-8",
        "cache-control": "public, max-age=300",
        "x-content-type-options": "nosniff",
        "referrer-policy": "strict-origin-when-cross-origin",
        "content-security-policy":
          "default-src 'none'; style-src 'unsafe-inline'; img-src data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'",
      },
    });
  },
};
