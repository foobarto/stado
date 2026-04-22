Session lifecycle and local-provider fallback learnings

- ACP and headless cannot safely treat `session.prompt` as re-entrant. The simple, correct fix is to mark a session busy under lock and reject overlapping prompt/compact requests. Trying to merge concurrent message histories is the wrong level of complexity for this codebase.
- ACP needs to persist both `workdir` and the lazy-opened git session on the session object. Reopening a fresh git session on every prompt breaks continuity for tool/audit state.
- ACP should persist the full `msgs` returned by `runtime.AgentLoop`, not synthesize a plain assistant text message afterward. Otherwise tool/result structure is lost between turns.
- The JSON-RPC transport shutdown path only exits cleanly if `Conn.Close()` can unblock the read loop. Closing the read side is enough when stdin/stdout are separate handles; the handler should still drain earlier requests before returning the shutdown ACK.
- Local fallback selection should prefer reachable runners with loaded models over merely reachable endpoints. On real machines it is common to have `ollama` up with no models while `lmstudio` has loaded models, and first-reachable selection picks the wrong backend.
