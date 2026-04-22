# TUI streaming and prefix regressions

Two patterns were worth writing down from the April 22 TUI bug pass.

1. High-rate reasoning/thinking streams should not post one Bubble Tea message per delta. Buffer provider events under a mutex and drain them on a `tea.Tick`; otherwise Bubble Tea's unbuffered program channel can starve key handling and make the UI feel frozen. Pair that with block-level render caching so historical markdown is not re-rendered every tick.

2. Multi-chord bindings need separate internal and display paths. Bubble Tea keymaps still want the first chord registered as a flat binding, but help/UI surfaces must render the full sequence (`ctrl+x ctrl+b`, `ctrl+x ctrl+c`) or users cannot discover what actually works.

3. When an action changes from immediate command execution to a confirmation modal, tests need to assert the state transition first and the final command only after the confirm keypress.
