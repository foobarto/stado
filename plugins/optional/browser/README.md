# browser — stado bundled browser plugin

A two-tier browser for LLM models. Available automatically to any stado session.

## Tier 1 — HTTP browser (always available, no dependencies)

Fetches pages via stado's HTTP client, parses HTML with goquery, manages a
cookie jar, and spoofs Chrome/Firefox/Safari headers. Works on any site that
doesn't require JavaScript or advanced bot fingerprinting.

**When the model gets `needs_js: true` in the result, switch to tier 2.**

Tools:
- `browser_open` — fetch a URL, parse HTML, return links/forms/text
- `browser_click` — follow a link by index or text match
- `browser_query` — CSS selector query on the current page

## Tier 2 — Chrome CDP (requires Chrome/Chromium installed)

Spawns a real headless Chrome process via `stado_proc_spawn`, connects to it
via the Chrome DevTools Protocol over WebSocket, and drives it with full JS
execution. Anti-detection flags applied at launch:
`--disable-blink-features=AutomationControlled` + spoofed UA. Handles most
DataDome / Akamai scenarios that don't require behavioral mouse simulation.

Tools:
- `browser_cdp_open` — navigate to URL, wait for load, return rendered content
- `browser_cdp_eval` — execute JavaScript in the page
- `browser_cdp_navigate` — navigate to new URL in existing session (efficient)
- `browser_cdp_screenshot` — capture PNG/JPEG screenshot
- `browser_cdp_close` — terminate Chrome and free resources

## Typical model workflow

```
# Start with tier 1 (fast, no deps)
result = browser_open(url="https://example.com")

if result.needs_js:
    # Upgrade to tier 2
    result = browser_cdp_open(url="https://example.com")
    value = browser_cdp_eval(session_id=result.session_id, js="document.title")
    browser_cdp_close(session_id=result.session_id)
```

## Capabilities required

```
net:http_request           # tier 1
net:http_request_private   # tier 1 (optional, for private IPs)
exec:proc                  # tier 2 (spawning Chrome)
net:dial:tcp:127.0.0.1:*  # tier 2 (CDP WebSocket)
```

## Installation (for operator review / manual install)

```bash
# Build
./build.sh

# Sign with your key
stado plugin gen-key browser.seed
stado plugin sign plugin.manifest.template.json --key browser.seed

# Trust and install
stado plugin trust $(cat author.pubkey)
stado plugin install .
```

Or use `stado plugin dev .` for the full authoring loop.

## Anti-bot notes

**Tier 1:** No real anti-bot resistance. Header spoofing only. Fails against
any fingerprinting that checks TLS (JA3/JA4), canvas, or WebGL.

**Tier 2:** Handles most DataDome / Akamai level 1-2 by removing automation
signals from Chrome's JS environment. Does NOT simulate mouse movement,
touch events, or browser behavioral patterns (level 3+). For those, use
a dedicated anti-bot service or playwright with behavioral plugins.
