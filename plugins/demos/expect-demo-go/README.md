# expect-demo-go

Minimal example of the `stado_terminal_expect` host primitive — the
read-until-pattern PTY operation that replaces the model loop of
`shell.read with timeout → substring-check → loop` with a single tool
call. The bundled `shell.expect` tool exposes the same primitive
without writing a plugin; this demo shows the host-import glue plugin
authors need when scripting an interactive program from wasm.

## What it does

The plugin exposes one tool, `expect_demo`. It runs (no args needed):

1. Spawn `bash -c 'printf "DEMO> "; read -r line; echo got=$line; exit 0'`
2. **Expect** `"DEMO> "` (timeout 2 s) — captures the prompt.
3. **Write** `"hello\n"` to stdin.
4. **Expect** `"got=hello"` (timeout 2 s) — captures the echo.
5. **Expect** `"never"` (timeout 0.5 s) — drives to EOF; reports the exit code.

The result lists each step's match envelope so the operator can see
exactly what came back from `stado_terminal_expect` (matched bytes,
the discriminator, exit code on EOF).

## Build, sign, install

```sh
cd plugins/demos/expect-demo-go
stado plugin gen-key expect-demo-go.seed
./build.sh
stado plugin trust <pubkey-hex-from-gen-key>
stado plugin install .
```

## Use

```
/tool run expect_demo
```

Sample output (paraphrased):

```jsonc
{
  "summary": "spawn → expect prompt → write 'hello' → expect echo → wait for EOF",
  "steps": [
    {"step": "expect prompt", "matched": true, "match": "DEMO> "},
    {"step": "expect echo",   "matched": true, "match": "got=hello"},
    {"step": "expect after exit", "matched": false, "note": "eof exit_code=0"}
  ]
}
```

## What plugin authors learn from this

- `stado_terminal_expect` returns the response JSON directly: byte
  count on success, `-n` with the host's error string at `resPtr` on
  failure. Same negative-return convention as the rest of the PTY
  family.
- The id rides on the i32 pair `(idLo, idHi)` — same wire shape as
  `stado_terminal_read` / `_write` — so the result buffer pointer can
  stay i32. JSON args carry only `patterns` / `regex` / `timeout_ms`.
- `before` and `match` are base64 because PTY output routinely includes
  ANSI escapes and other non-UTF8 sequences. Decode them with
  `base64.StdEncoding.DecodeString` when the caller wants a string view.
- Across patterns, the EARLIEST byte position wins (ties go to the
  lower `patterns[i]` index). Use this to branch on "either a prompt
  or an error" without two separate calls.
- For full-screen TUIs (vim, mc, htop) match against
  `stado_terminal_snapshot` rendered text instead — `expect` operates
  on the raw byte stream, where ANSI escapes interleave with content
  and substring matches against rendered words won't find them.
