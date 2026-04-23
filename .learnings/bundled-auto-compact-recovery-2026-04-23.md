Bundled auto-compaction needs two core-side details to behave
reliably:

1. Background-plugin SessionBridge state must be refreshed per tick.
   Reusing the bridge built at startup leaves `session:read` history
   stale, because the plugin keeps reading the conversation snapshot
   from boot time instead of the current session state.

2. TUI recovery cleanup cannot clear its "waiting for fork" latch
   immediately when the background tick returns.
   `pluginForkMsg` is delivered asynchronously through Bubble Tea, so
   the tick result can arrive before the fork notification. A short
   timeout-based fallback is enough: if the fork message arrives, it
   clears the latch; if it does not, the timeout posts the manual
   recovery advisory.
