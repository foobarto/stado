# web-search — HTTP search with no API key

Wraps `stado_http_request` to fetch search results from DuckDuckGo's
HTML endpoint (default) or a SearXNG instance (opt-in). Returns
parsed `{title, url, snippet}` triples — no scraper boilerplate in
the calling LLM's prompt.

## Tool

```
web_search {query, max_results?, backend?, instance_url?}
  → {backend, query, results: [{title, url, snippet}]}
```

- `backend = "duckduckgo"` (default): scrapes
  `html.duckduckgo.com/html/?q=...` and unwraps the redirector
  hrefs (`/l/?uddg=...`) back to real URLs. No API key.
- `backend = "searxng"`: calls `<instance_url>/search?q=...&format=json`.
  Caller supplies `instance_url`. Use this if you run / trust a
  specific instance and want a stable JSON contract.

## Build + install

```sh
stado plugin gen-key web-search-demo.seed
./build.sh
stado plugin trust "$(cat author.pubkey)" web-search-demo
stado plugin install .
```

## Run

```sh
stado plugin run --with-tool-host \
  web-search-0.1.0 web_search '{"query":"hackthebox writeups","max_results":5}'
```

The `--with-tool-host` flag is required because the plugin imports
the bundled `stado_http_request` (see `EP-0028`).

## Capabilities

```toml
capabilities = ["net:http_request"]
```

If you want to allow arbitrary public hosts, the broad cap above is
fine. Tighten to `net:http_request:html.duckduckgo.com` (or your
SearXNG host) if your threat model wants per-host pinning.

## Why DuckDuckGo HTML and not the JSON API

DuckDuckGo's "Instant Answer" JSON API only returns a tiny subset of
results (the answer card, not the organic list). The HTML endpoint
returns the full result list and works with no key — at the cost of
scraper fragility. If DDG changes their layout, update
`resultBlockRE` in `main.go`.

## Limitations

- DuckDuckGo can rate-limit or serve a CAPTCHA page to abusive UAs.
  The default UA matches a recent Firefox; if you start seeing zero
  results, run a manual `curl -A "<UA>" https://html.duckduckgo.com/html/?q=test`
  to confirm whether the issue is the plugin or the upstream.
- HTML entity decoding is minimal — covers `&amp;`, `&lt;`, `&gt;`,
  `&quot;`, `&#x27;`, `&#39;`, `&nbsp;`. If a snippet shows a literal
  `&hellip;`, extend `htmlUnescape` in `main.go`.
- No pagination (yet). Returns whatever fits on the first results
  page (~10 results from DDG).
