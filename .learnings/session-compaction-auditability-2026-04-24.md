Session compaction auditability hinges on three separate records:

- `.stado/conversation.jsonl` is the raw append-only conversation log.
  Compaction appends a `type:"compaction"` event; it must not rewrite
  or truncate prior message lines.
- `refs/sessions/<id>/tree` and `trace` both get compaction marker
  commits. The marker should include a digest of the raw JSONL bytes
  before the compaction event is appended.
- Every completed LLM turn needs a `refs/sessions/<id>/turns/N` marker,
  including pure chat/no-file-change turns and `stado run --session`
  turns without tools. Open existing sessions through runtime
  scaffolding so these marker commits keep normal signing/logging.
- Headless/ACP git-backed sessions must persist the folded transcript
  view to `.stado/conversation.jsonl`; otherwise turn refs exist but
  compaction binds an empty or stale raw log.
