# browser — tiny headless "browser" in a wasm plugin

A best-effort headless browser inside the wasip1 sandbox: fetches
pages, parses the DOM, navigates links, submits forms, persists
cookies, and spoofs UA / Accept-* headers from a chrome / firefox /
safari profile. Wraps `stado_http_request` + goquery + `golang.org/x/net/html`.

**This is not a real browser.** Read [What it can't do](#what-it-cant-do)
before relying on it for anything that crosses Cloudflare-grade
anti-bot.

## What it can do

- HTTP GET/POST with cookie jar (per-host, persisted to disk).
- Follow redirects (up to 10 hops per call).
- Parse HTML via goquery — same selectors you'd use in jQuery.
- Extract a flat list of links and forms with default values.
- Submit `application/x-www-form-urlencoded` forms with merged
  defaults + caller overrides + named submit-button "click".
- Spoof `User-Agent`, `Accept`, `Accept-Language`, `Sec-Ch-Ua-*`,
  `Sec-Fetch-*` per profile. Default profile is `chrome`.

## What it can't do

- **No JavaScript.** A JS engine inside wasip1 (Goja or QuickJS) adds
  1-3 MB and gives no canvas, no WebGL, no AudioContext — the
  exact signals modern fingerprinters key on. JS would help only
  against sites whose data is gated behind client-side rendering,
  which is a different problem.
- **No real fingerprint resistance.** `stado_http_request` runs on
  Go's `net/http` transport. JA3/JA4 TLS fingerprint, HTTP/2 frame
  ordering, cipher-suite order — all come from the host's HTTP
  client and are visible to anti-bot regardless of what headers we
  set. Any modern Cloudflare setup catches this on the handshake.
- **No rendering.** "screenshot" returns serialized HTML + a flat
  text extract. Useful for an LLM walking the page; useless for
  anyone wanting pixels.
- **No `multipart/form-data`.** v0.1 only handles
  `application/x-www-form-urlencoded`. File uploads aren't here.
- **No WebSocket / SSE / long-poll.** Single-request semantics.

## Tools

```
browser_open {url, profile?, headers?}
  → {session_id, url, status, headers, title, links, forms, cookies, text, html}

browser_click {session_id, link_index? | selector? | text_match?}
  → same shape

browser_submit {session_id, form_index?, selector?, fields, click?}
  → same shape

browser_screenshot {session_id}
  → {url, title, text, html, dom_summary, links, forms}

browser_eval {session_id, selector, attr?}
  → {matches: [{html, text, attrs, value}]}
```

## Build + install

```sh
stado plugin gen-key browser-demo.seed
./build.sh                                # 6.9 MB plugin.wasm
stado plugin trust "$(cat author.pubkey)" browser-demo
stado plugin install .
mkdir -p $PWD/.cache/stado-browser
```

## Run

```sh
# fetch a page
stado plugin run --with-tool-host --workdir $PWD \
  browser-0.1.0 browser_open '{"url":"https://news.ycombinator.com"}'
# → {session_id: "8c13...", title: "Hacker News", links: [...], ...}

# follow the third link from the result
stado plugin run --with-tool-host --workdir $PWD \
  browser-0.1.0 browser_click '{"session_id":"8c13...", "link_index":2}'

# fish a CSS selection out of the current page
stado plugin run --with-tool-host --workdir $PWD \
  browser-0.1.0 browser_eval '{"session_id":"8c13...", "selector":".titleline a", "attr":"href"}'

# submit a form (httpbin example)
stado plugin run --with-tool-host --workdir $PWD \
  browser-0.1.0 browser_open '{"url":"https://httpbin.org/forms/post"}'
# (note the session_id, then…)
stado plugin run --with-tool-host --workdir $PWD \
  browser-0.1.0 browser_submit '{
    "session_id":"<sid>",
    "fields":{"custname":"Ada","custemail":"ada@example.com","size":"large"}
  }'
```

## Capabilities

```toml
capabilities = [
  "net:http_request",
  "net:http_request_private",         # drop if you only hit public sites
  "fs:read:.cache/stado-browser",
  "fs:write:.cache/stado-browser",
]
```

## Profiles

`chrome` (default), `firefox`, `safari`. Each is a header bundle
matching a recent stable channel. We deliberately omit
`Accept-Encoding` from every profile — Go's `net/http` transport
auto-injects `gzip` and transparently decompresses responses, but
only when the caller hasn't set the header. Setting
`Accept-Encoding: br` ourselves would force the transport to leave
the body brotli-compressed and we'd need to ship a wasm brotli
decoder. Cost in fingerprint fidelity is minor; cost in code is large.

## Where to grow this

If you have a concrete target that needs JS-rendered content **and**
the host has been upgraded to spoof JA3/JA4 (e.g. via
[`utls`](https://github.com/refraction-networking/utls) under
`stado_http_request`), v0.2 would add a Goja-backed JS executor.
Without the host-side TLS spoofing, JS execution doesn't move the
needle against anti-bot — and the cost is real: +3 MB binary,
+complexity. Don't add JS prematurely.
