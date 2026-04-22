# Documentation surface audit

When updating docs in `stado`, validate two things together:

- `./stado --help` for the current shipped command surface and one-line summaries
- Local markdown links under `README.md`, `docs/`, `CONTRIBUTING.md`, and `SECURITY.md`

The docs index can drift by linking to guides that do not exist yet,
and README status text can lag behind shipped behavior. Treat CLI help
as authoritative for command coverage until a standalone guide exists.
