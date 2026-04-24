For stado's terminal UI, the useful parts to borrow from opencode are hierarchy and surface contrast, not its exact color tokens.

What translated well:
- subtle layered surfaces instead of loud borders everywhere
- a compact colored mode pill inside the input frame
- right-aligned user bubbles so prompts stand apart from assistant output
- quieter section chrome (`// section`) so content wins over labels

What did not need direct imitation:
- the full web-token system
- separate brand/orange/purple accents
- heavy hover/animation language that depends on a GUI stack

Mapping the foobarto.me palette worked best when treated as:
- background/panel/border = `#0c0f0d / #111613 / #1f2925`
- main accent = `#7fdc8c`
- warning/tool = `#e0c87b`
- error/system = `#e07b7b`
- cool secondary for thinking/BTW = `#7faedc`
