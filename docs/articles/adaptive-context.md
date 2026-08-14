# What Survives the Window

The tool call fails.

The agent supplied one path where the tool expected a list of paths. The error
is clear, the agent changes the argument, and the next call succeeds. It finishes
the task and the session ends.

A week later another session makes the same mistake.

Nothing mysterious happened. The correction is still present in the old
transcript. The model that needs it simply cannot see that transcript, and
putting every old transcript into every new prompt would be a spectacularly
expensive way to avoid admitting that a transcript is not memory.

The tempting answer is to make the system remember more. Save every correction.
Summarise every session. Search everything automatically. Move the most useful
fragments into the prompt. Let the agent update its own operating instructions
as it learns.

Each step sounds sensible. Taken together they create a machine that quietly
turns yesterday's model output into tomorrow's model authority.

Stado's adaptive context work starts from a less ambitious question: what is
worth carrying across a context boundary, and who gets to decide?

I want to be precise about the word *adaptive*. Stado does not continuously
fine-tune a model, and it does not treat a successful tool call as proof that a
new rule is true. It records a small set of observable events, lets an isolated
reviewer propose lessons, requires an explicit broker activation grant before
those lessons become active, and retrieves active material under explicit scope
and size limits. That grant comes from predeclared policy or a separately
trusted presenter; an in-process confirmation alone is only workflow input.

The system can become better informed by its history. It cannot promote its own
interpretation of that history into policy.

That distinction is the feature.

## A transcript is a history, not a memory

Conversation is good at preserving sequence. The operator asked for something,
the agent tried an approach, a command failed, a correction followed, and the
task eventually passed verification. If the question is “what happened?”, the
transcript and signed trace are the right evidence.

They are a poor answer to “what should be available next time?”

Most of a working session is disposable. Search results, compiler output,
intermediate hypotheses, abandoned edits, and the third explanation of the
same failure were useful at the time. Carrying all of them forward would spend
the next session's context budget reliving the previous one.

Compaction helps, but a summary is still trying to serve two incompatible jobs.
It has to preserve enough narrative to continue the current task, while also
guessing which small fact will matter in an unrelated task next month. A larger
context window postpones that choice. It does not remove it.

So stado keeps several things separate.

The transcript records what was said. The trace records what the harness
observed. Session state records what is currently in progress. A journal gives
a readable chronology. Decisions preserve choices and their consequences.
Signals record small mechanical patterns that may deserve review. Memories and
lessons are versioned artifacts with an explicit lifecycle.

This can sound like taxonomy for its own sake. It is really a refusal to let one
convenient blob become history, working state, evidence, instruction, and
authority at the same time.

## The correction is not yet the lesson

Return to the malformed tool argument.

Stado can observe the failure without interpreting it. It knows the tool name,
that the call failed, that a later call changed the arguments, and that the
changed call succeeded. It can also observe repeated identical failures, a
verification failure followed by a pass, recurring policy denials, and an
explicit operator correction.

These are signals. They are deliberately modest claims.

“The arguments changed and the call succeeded” is something the host can
record. “Always use a list of paths with this tool” is an interpretation. The
first can be produced deterministically. The second needs a review of the
surrounding evidence and may still be wrong. Perhaps two versions of the tool
were involved. Perhaps the successful call worked for an unrelated reason.
Perhaps the correction applies only to one repository.

`/learn` sends the completed trajectory and its host-recorded signals to an
isolated, tool-less reviewer. The reviewer can propose a bounded lesson and
cite the evidence that motivated it. It cannot activate the lesson. A one-off
failure does not produce a learning signal at all, because an adaptive system
that memorialises every typo is mostly an automated collection of noise.

This is also why learning happens after a trajectory rather than as a running
rewrite of the system prompt. During the task, the agent is busy defending its
current hypothesis. A separate review gets a cleaner job: identify a repeatable
operational correction, state the conditions under which it applies, and leave
the result pending.

Pending is not a euphemism for active.

## Activation is an authority event

An agent can run shell commands. Therefore a CLI command executed through a
shell is not evidence that the human operator approved anything.

Stado exposes inspection through `stado learn` and the TUI:

```text
/learn candidates
/learn show art_...
```

Activation is deliberately withheld until a broker-owned predeclared policy or
a separately trusted presenter can prove operator intent for the exact artifact
text, version, and scope. An in-process TUI callback, command-origin label,
plugin UI response, or session-controller token is not that proof. If the
candidate changes, any eventual exact grant cannot float to the new version;
it must be consumed when activation is committed to the broker's durable log.

This is not ceremony around a confidence score. A lesson may be well supported
and remain inactive. Another may be activated for a narrow experiment despite
limited evidence. Confidence describes what we think about the evidence.
Activation decides whether future sessions are allowed to receive the text.
They are different facts.

Even an active lesson remains untrusted guidance. It sits below operator and
repository instructions. It cannot grant a tool, expand a sandbox, approve a
plugin, or turn a historical capability into a current one. The operator is
approving the lesson for retrieval, not signing every sentence as eternal
truth.

Scope makes that approval smaller. A session lesson is visible to the session
that created it and its descendants, where compaction and deliberate forks can
carry the work forward. It does not leak sideways to a sibling or backwards to
an ancestor. Repository artifacts stay with the repository. Global means the
current local broker principal, not every user or machine that might eventually
see a copied file.

The useful default is not “remember everywhere.” It is “remember no farther
than the evidence justifies.”

## Retrieval has more than one speed

Suppose the approved lesson is relevant a week later. There are three sensible
ways to recover history, depending on how much history the question needs.

The fast path is ordinary prompt retrieval. Stado selects a small number of
active, authorized, non-expired artifacts under hard item and token limits. The
section is labeled as reviewable, untrusted context. This is the right path for
a short operational reminder: the tool expects a list of paths, this repository
uses a particular verification command, or a known compatibility constraint
must be preserved.

Fast retrieval is intentionally incomplete. It should be cheap enough to use on
ordinary turns, which means it cannot open a large corpus and reason across it.
When the question is “what did we learn about flaky release jobs?”, stado can
launch an isolated memory researcher. That child receives catalog, search, and
open tools over the artifacts the caller is authorized to inspect. It returns a
bounded synthesis with exact artifact versions and excerpts. The raw search
process stays out of the main agent's context.

Historical session research is slower again. It can inspect authorized session
paths and bounded message windows to answer questions such as “why did the
earlier migration preserve the old decoder?” The parent receives a cited
synthesis rather than a replay of every explored transcript.

The citation contract is deliberately narrow. Stado can prove that an excerpt
came from a particular stored version. It cannot prove that the excerpt is true,
that the researcher understood it, or that it entails the conclusion next to
it. Byte provenance is valuable. It is not semantic certainty.

The three paths make context a cost decision instead of a binary one. Small,
likely-relevant material can arrive quickly. Broader questions can spend a
separate context budget. Deep history remains addressable without making the
main prompt carry the archive.

## Compaction should lose conversation, not commitments

Context pressure is most visible during long work. The model approaches its
window limit, stado compacts or forks the session, and the new conversation has
to continue without most of the old turns.

Some loss is the purpose of compaction. The new worker does not need every
failed search or every patch it considered. It does need to know the active
task, blockers, next action, current children, verification state, and any
decision whose consequences still constrain the work.

Stado stores those as bounded projections outside the conversation. Model-written
state is clearly marked as an assertion and is limited to fields such as the
current task, blockers, and next step. Host-owned fields—identity, capabilities,
usage, child status, and verification results—cannot be rewritten by model
prose. Decisions and signals retain their own provenance instead of being
flattened into a summary that makes every sentence look equally authoritative.

When a session forks, it takes an immutable view of the history available at
that point and then appends its own events. The child can inherit relevant data.
It does not inherit a magic right to reinterpret the past or widen its present
capabilities.

This is the same underlying idea used by `/supervise`: put the parts that must
survive forgetting somewhere forgetting cannot edit them. Adaptive context is
broader, though. Its job is not to decide whether work is complete. Its job is
to keep history available in forms whose cost, provenance, and authority remain
legible after the conversation that produced them is gone.

## Retained agents are context with a return address

Sometimes the useful thing from an earlier turn is not a fact. It is a piece of
unfinished work.

A parent might ask a child to investigate a dependency, continue with its own
task, and return to the investigation later. A synchronous tool result is a bad
fit: either the parent waits, or the relationship disappears when the turn
ends. A shared scratch file is not much better. It has no clear sender,
recipient, ordering, or authority boundary.

Retained agents give the work a durable handle. A child can be admitted with a
bounded purpose, model, tool profile, write scope, time, turns, and tokens. The
parent can list it, send a follow-up, read its messages, or cancel it after a
restart. Mailboxes preserve parent-to-child and child-to-parent messages with
per-sender ordering, backpressure, expiry, and durable delivery state.

The messages carry data, not permissions. A child cannot write “I now have
deployment authority” into a reply and make it so. Lifecycle and cancellation
events travel on a separate broker-owned control plane so a full data mailbox
cannot hide the fact that the child stopped.

Resuming historical work also creates a new child identity. Stado can restore a
bounded transcript and the exact selected tree from an earlier session, but it
does not resurrect the old process in place. The source contributes context;
the new admission determines current authority.

That distinction also constrains restarts. Transient runtime failures may use a
bounded restart policy. Logical errors, policy denials, exhausted budgets,
cancellation, and failed verification do not become more correct by being run
again automatically.

Durability is useful. Indiscriminate persistence is just a loop with better
storage.

## Guidance belongs with the workflow

Making artifact research, session research, `/learn`, retained agents, and
mailboxes available does not mean a model will know when to use them. Tool
descriptions explain what a mechanism does. They rarely notice that the current
turn is the moment to use it.

The first implementation solved this with native harness guidance. Host code
looked at signals, chose one of several fixed recommendations, and inserted the
result before the next model turn. It was bounded and useful. It was also a
small application policy hiding in the native runtime: stado itself decided
when a failure deserved learning, when a historical question deserved a
researcher, and when an unread child deserved attention.

Those decisions now belong to a signed lifecycle application. The host exposes
the smaller facts it actually knows: a stable failure fingerprint repeated, a
retrieval returned no match, a child remains active, a mailbox has unread data,
and a particular capability is available. The application chooses whether any
of that deserves guidance, what threshold to use, and how to word the nudge.
Another application can make different choices without teaching native stado a
second product's workflow.

The useful constraints survive the move. Guidance is intentionally boring. At
most a few fixed templates fit under a separate byte cap. Raw tool arguments,
signal attributes, artifact bodies, and mailbox payloads are not interpolated
into the text. A suggestion appears only when the corresponding capability is
actually available.

Guidance does not approve a lesson, widen a tool set, choose a recipient the
caller could not already address, or override operator and repository
instructions. The application can teach the model about a workflow opportunity.
The broker still decides what that application and model are allowed to do.

There is an important humility in fixed wording. No second model call is needed
to tell the first model that two identical failures were observed. A small
sandboxed application can state the observation, offer the appropriate
mechanism, and leave both semantic judgment and authority where they belong.

## Adaptation without the success mythology

A retrieval system eventually wants to know whether its results were useful.
That sentence hides a trap.

An artifact appearing before a successful result does not mean the artifact
caused the success. The model may not have read it. It may have ignored it. The
task may have been easy anyway. A highly cited artifact may be popular because
it is vague enough to fit everything.

Stado therefore records mechanical observations separately: an artifact was
considered, surfaced, opened, or cited. Claims such as *helped*, *failed*, or
*contradicted* require an external evaluation or an explicitly labeled
judgment. Temporal association is not presented as causal evidence.

Adaptive ranking is shadow-only in the current design. Stado can compute and
report how an alternative ranking would have ordered the eligible artifacts,
but that score does not yet change what reaches the prompt. This makes it
possible to inspect the proposed adaptation before adaptation starts shaping
the evidence used to justify itself.

Some material is not eligible for automatic demotion at all. Mandatory or
pinned security guidance should not disappear because recent tasks did not cite
it. Absence of visible use is not evidence that a boundary has stopped being
necessary.

The goal is measured retrieval, not an engagement algorithm for instructions.

## The database is not the memory

Search wants a database. Authority wants an inspectable history.

Stado keeps canonical artifact and session events in broker-owned append-only
logs with ordering, integrity checks, optimistic versioning, and idempotent
jobs. SQLite FTS5 provides the fast structured index over ordinary searchable
material. If the index is missing, corrupt, or behind its recorded checkpoint,
stado rebuilds or rejects it rather than treating stale rows as truth.

This matters most when an artifact is retired, superseded, deleted, or changes
scope. A search cache must not keep an old permission alive. The answer to
“which version is active?” comes from the canonical event history, not from
whichever search row happens to exist.

Private and secret bodies stay out of the ordinary full-text index. Quotas bound
items, events, observations, and research reads. Legacy memory is imported
idempotently, with the original bytes archived and unresolvable session bindings
quarantined instead of broadened.

Most users should not have to think about any of this while recovering a useful
lesson. That is exactly why the machinery exists. A feature that works only
while its cache, process, and last model response all agree is not memory. It is
a coincidence.

## Remembering is not the same as trusting

Persistent context expands the reader's attack surface. A repository file can
contain persuasive instructions. So can a tool result, a child message, an old
transcript, and a lesson proposed from any of them. Moving text into a durable
store does not wash its provenance away.

Review and scope reduce the chance of accidental propagation. Read-only
research tools reduce the consequences of a steered researcher. Sensitivity
rules keep some bodies out of normal search. Capability ceilings stop retrieved
text from granting itself a tool. None of these turn model-authored text into a
security principal or make an approved memory universally true.

An operator can approve a bad lesson. A researcher can produce a misleading
synthesis with valid citations. A stale artifact can outlive the conditions
that made it useful. The lifecycle includes new versions, rejection,
supersession, retirement, expiry, and deletion because learning without
unlearning is accumulation, not adaptation.

The boundary stado can enforce is narrower and more useful: historical data does
not become current authority merely by being remembered. That leaves the model
free to use the past without asking the past for permission.

## What survives is a decision

The context window will fill. Compaction will discard detail. Sessions will end.
Processes will restart. Children will finish after their parents have moved on.
Useful corrections will compete with thousands of lines that were useful only
once.

The answer is not to make forgetting impossible. Forgetting is what keeps a
working context workable.

The answer is to separate what happened from what was inferred, what was
proposed from what was approved, what is searchable from what is authoritative,
and what a session may read from what it is allowed to do.

Then the small correction from the first task can reach the second task without
dragging the first task behind it. The child can return after the parent has
compacted. The old decision can be found without restoring the old identity.
The search index can be thrown away without throwing away the truth about which
artifact is active.

A context window is where the model works now.

Memory is the set of decisions about what may meet it there.

---

The concise operational references are [`stado learn`](../commands/learning.md),
[`stado memory`](../commands/memory.md), and [context
management](../features/context.md). The detailed contracts are recorded in
[EP-0052](../eps/0052-learn-trajectory-refinement.md) through
[EP-0060](../eps/0060-native-harness-guidance.md).
