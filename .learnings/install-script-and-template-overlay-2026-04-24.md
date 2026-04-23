## Installer + template overlay notes

- `install.sh` does not need the GitHub Releases API to find the right
  archive. Downloading `checksums.txt` from `releases/latest/download/`
  is enough: the manifest already names the exact archive for each
  `os/arch`, so the script can pick the asset from the signed manifest
  instead of parsing GitHub JSON.

- The simplest smoke test for the installer is a local `file://` fixture
  directory plus a fake `cosign` binary in `PATH`. That keeps the test on
  the real shell script, real `curl`, real archive extraction, and real
  sha256 verification without depending on GitHub or a live trust root.

- TUI template overrides belong next to `config.toml`, not in a separate
  path knob. The entry point should look under
  `$XDG_CONFIG_HOME/stado/templates/` (same parent as `theme.toml`) and
  call `render.NewWithOverlay` only when that directory exists.
