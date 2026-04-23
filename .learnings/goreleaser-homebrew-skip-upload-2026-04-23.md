When publishing a Homebrew tap with GoReleaser, `skip_upload: auto`
does not mean "skip when the tap token is missing". The official
Homebrew docs only define `auto` for prerelease tags.

For this repo, the safe pattern is:

- keep `repository.token: "{{ .Env.HOMEBREW_TAP_TOKEN }}"`
- set `skip_upload` with a template:
  `{{ if .Env.HOMEBREW_TAP_TOKEN }}auto{{ else }}true{{ end }}`

That preserves prerelease auto-skip when the token exists, and forces a
non-failing "generate formula only" path when the token is absent.
