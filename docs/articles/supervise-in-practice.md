# The Loop Needs a Witness

The command fails.

`npm test`: command not found.

The agent explains the failure, considers the state of the repository, and runs
the command again.

`npm test`: command not found.

On the third attempt the explanation gets better. Perhaps the JavaScript
dependencies were not installed. Perhaps the package metadata is incomplete.
There is some inspection, a longer diagnosis, and then the same command goes
through the same tool and returns the same error.

One file away, `CONTRIBUTING.md` says the project is written in Go and the
supported test command is `go test ./...`.

This transcript did not happen. It is a trap I wrote for an evaluation fixture.
The fixture is synthetic; the behaviour is familiar. A tool-using model can be
active for a long time after it has stopped learning. The transcript grows,
the explanations improve, tokens are spent, and the state of the problem does
not move.

I want to be careful about what I am claiming here, because `/supervise` is new
and the scenarios in this post are test cases, not published results. I am not
saying a second model makes the first one reliable. I am not saying a watchdog
can identify good engineering by counting commands. I am saying something
narrower: progress, permission, and completion are different kinds of claim,
and one model inside one conversation is the wrong component to own all three.

I also need to be precise about where this design lives. The architecture in
this article is the accepted EP-64 target now being implemented, not a claim
that the earlier native `/supervise` workflow was the final shape. Supervision
is an official signed WASM lifecycle application released from
`foobarto/stado-plugins`. It may be a large application. It owns
the contract wizard, cadence, detectors, prompts, verdict policy, and workflow.
Native stado supplies the things a sandboxed application cannot: broker-stamped
facts, durable ordering, scoped capabilities, and scheduling effects it can
actually enforce.

The ordinary response is to add instructions. Be systematic. Read the repository
guidance. Do not repeat a failed command without changing something. Verify your
work before declaring completion. These are good instructions. They also live
in the same context window as the failed command, the next tool call, the code,
the test output, and the increasingly attractive explanation that the work is
basically done.

This is the [Milton
problem](https://foobarto.me/blog/2026/the-milton-model/) in a different coat.
The prompt is a sign. The model is the operator reading the sign. If the same
model also decides whether the sign was obeyed, the enforcement slot is empty.
The instruction may be clear, sincerely followed most of the time, and still be
only an instruction.

The rhyme is architectural; the purpose is different. Milton is a security
failure because a model was given an access-control job. `/supervise` is a
quality gate. Its comparators decide whether work remains aligned with the
selected quality contract and whether the evidence supports completion. They
do not decide whether hostile code is safe to execute, whether a secret may be
disclosed, or whether an untrusted actor should receive access.

I built stado's `/supervise` mode around the observation that some instructions
are too important to remain instructions. If a requirement has to survive a
long migration, keep it somewhere forgetting cannot edit it. If changing the
job requires permission, make permission a state transition rather than a
sentence. If completion requires evidence, do not let the model that produced
the patch decide that its description of the patch counts.

And if the worker is stuck in a loop, the thing watching the loop has to be able
to stop it.

## The contract exists before the implementation

`/supervise` begins by declining to begin.

The operator supplies an objective. Before the worker gets a turn, a fresh
watchdog proposes a baseline: constraints, non-goals, acceptance criteria, an
ordered plan, a definition of done, verification expectations, and known risks.
The operator reviews that proposal in the TUI and accepts or rejects it as the
quality contract. The application records the exact candidate ID and version
before work starts. That choice does not activate the artifact, widen the
plugin's signed capabilities, or prove operator-origin security authority; it
only fixes which contract this admitted application will enforce. No
implementation starts before that quality-workflow confirmation.

That ordering matters more than it appears to. In an ordinary agent session,
the first plausible implementation often becomes the unstated specification.
The model chooses a shape, writes it into code, and later resolves ambiguity in
the direction of the code it already has. By the time somebody asks what
duplicate rows were supposed to mean, duplicate handling has become whatever
was easiest to implement.

The
[premature-completion scenario](../../evals/supervise/scenarios/premature-completion.json)
is built around exactly that temptation. The task is resumable batch import.
Duplicates, cancellation, restart after partial progress, malformed input,
compatibility, and documentation all need defined behaviour. The fixture also
contains a convenient TODO and a happy-path test. Filling in the TODO and making
that test green can produce useful code. It cannot answer what happens when the
process stops halfway through.

Under supervision, those semantics become acceptance criteria before the first
edit. The application selects one exact version of a session-scoped candidate
artifact as its quality contract and keeps fast-moving plan/run state in the
session journal. That selection does not activate the artifact or grant
authority. Exactly one plan step is active because that is supervise's workflow
policy, not a universal law in stado. A worker may submit evidence, claim a
step is complete, request a pivot, or request completion. The application
decides what those requests mean under the selected contract. The broker
decides whether the resulting write or scheduling request has current identity,
scope, capability, authority, and version.

The distinction sounds formal because it is formal. “The model said it was
done” is not a state transition.

## What code can count without asking a model

Return to the failed command. No language model is needed to discover that the
same tool call produced the same failure three times. The host already observed
the tool name, argument digest, result, turn anchor, and stable error
fingerprint. It exposes those bounded, ordered facts; the supervise application
decides which comparisons matter. The
[retry-thrash scenario](../../evals/supervise/scenarios/retry-thrash.json)
exists to make that difference visible: the unsupported command should be tried
no more than twice, the repository instructions should be read, and the tactic
should change.

The supervise detector records `repeated_failure` when the error repeats and
`retry_thrash` when the arguments repeat with it. Its other detectors watch for
a growing diff outside the approved scope, edit-and-revert churn, verification
regressions, failed children, unsupported completion claims, and turns in which
no acceptance criterion moved. Another supervision plugin could choose
different thresholds without changing stado's core.

There is a small design choice here that carries more weight than its size.
Activity does not suppress the four-turn progress-stall review. A changing tree
and a growing evidence list are useful context, but neither proves that the
approved work advanced. A worker can be extremely busy in the wrong place.
Busy and progressing are not synonyms, so the application's state machine does
not treat them as synonyms.

The deterministic detector still does not know whether repetition is justified.
The third identical command might be foolish, or the surrounding environment
might have changed in a way that makes the retry exactly right. Counting is the
part code is good at. Interpreting the count against the task is where the
watchdog enters. Keeping the counting in WASM is not an attempt to make it less
deterministic. It keeps application policy inside the same capability sandbox
as other plugin logic and keeps the native core from accumulating one product's
rubric.

## What the watchdog is allowed to mean

In event mode, the application starts a fresh watchdog when a detector fires,
and it may also request a periodic review every configured number of completed
worker turns. In live mode it requests review after every turn. For work where
freshness matters more than throughput, live mode can acquire a turn barrier
until the current-anchor verdict arrives. This cadence belongs to supervise;
the host only provides event delivery, timers, and scoped scheduling holds.

The watchdog receives the selected contract, current plan position, triggering
events, and bounded access to host-anchored evidence. The application requests
a child with a narrow role and tool profile; the host intersects that request
with the session ceiling. The watchdog can read or search the audited
repository tree, inspect relevant tool results and diffs, and ask for another
page of the exact query it already made.

It cannot run a shell. It cannot modify a file. It cannot quietly become the
worker because the worker is struggling. Its evidence access is read-only, and
its verdict is input to the supervise application rather than authority of its
own. If the application wants to steer, pause, or stop, it must make a separate
capability-gated request to the host.

This limit matters because the watchdog is another fallible component, not
because it is intended to contain an adversary. A quality reviewer needs enough
evidence to judge the work without acquiring the ability to do the work behind
the worker's back.

For the repeated `npm test` failure, the watchdog can conclude that the worker
has stopped learning and issue a bounded correction: stop repeating the
unsupported command, inspect the repository guidance, and change tactics. The
application sends that labelled steering through the lifecycle/mailbox bridge
for the next worker boundary. If the correction does not change the trajectory,
its policy may request another review. Repeated failed interventions may lead
the application to request a durable operator pause; the broker, not the
watchdog's prose, enforces it.

Freshness is deliberate. The watchdog does not accumulate a second unbounded
conversation alongside the first. A bounded handoff carries open concerns,
hypotheses, interventions, missing evidence, and suggested probes from one
review to the next. The new instance gets continuity without inheriting an
entire history of sunk costs and defended judgments.

The verdict is also tied to the state it actually saw: the session sequence,
plan version, active step, and audited tree. If the worker moves the tree while
the watchdog is thinking, the answer is stale. However intelligent the answer
sounds, it cannot authorize a transition about a state that no longer exists.
Conversely, once a verdict has been parsed and validated, a later provider
cleanup error does not erase it. Transport cleanup is recorded as a diagnostic;
it is not allowed to rewrite a semantic result that already arrived.

That does not make every stale answer equally useless. A stale continuation or
approval says only that an earlier state looked acceptable. There is nothing
left to apply, so the application discards it. A stale correction may still
contain a useful change of tactic. The application preserves its earlier-anchor
provenance and delivers it as advisory steering at the next worker boundary. It
does not pause the run, count as a failed current correction, or become the last
accepted verdict.

A stale request to pause or stop is the third case. Silently discarding it
would turn a slow watchdog into decoration. Applying it would let a judgment
about an old state control a new one. The application therefore requests a
durable review hold, then asks a fresh watchdog to inspect the latest
session/tree anchor published by the host. The broker persists the hold before
it stops scheduling the worker. A current continuation or approval lets the application
release the hold. A current correction becomes steering. A current pause or
stop becomes an authorized scheduling request. If that confirming review is
unavailable, policy leaves the hold as an operator-resumable pause.

The asymmetry is practical rather than taxonomic. Most late answers do not
need to stop the world. Only a stale result that asks the host to stop the
worker creates a barrier, and even then the old result supplies the reason for
a new judgment rather than the authority for the final action. Periodic
event-mode review and strict live-mode barriers are explicit application
settings. Neither falls out accidentally from response timing.

This division is the boring half of `/supervise`. The application owns the
workflow keys: it validates the review schema, compares anchors, interprets the
verdict, and chooses its next request. The host and broker own the authority
keys: they authenticate the caller and current session, intersect capabilities,
persist ordered state, and apply only a valid scoped effect. The watchdog can
be persuaded. That still cannot persuade the broker to grant it a permission
it does not have.

Starting `/supervise` also has to stop an existing `/loop`. This is not
interface tidiness. The application owns the supervision schedule and uses the
generic session control plane to replace the old recurrence. A watchdog that
cannot cause an authorized loop stop is an observer, and an observer is not a
watchdog.

## A quality gate, not a security feature

The words *watchdog*, *trusted boundary*, and *human-only authority* can make
this sound like a security control. It is not one, and relying on it as one
would repeat the category error the architecture is trying to avoid.

The watchdog reads repository files, transcripts, tool output, and diffs. All
of them can contain instruction-shaped text. [The reader is still an attack
surface](https://foobarto.me/blog/2026/the-reader-is-the-attack-surface/): a
hostile repository can try to steer the watchdog, just as an accidental but
persuasive explanation can. Read-only tools reduce what a steered watchdog can
do. Anchors stop stale judgments from changing current state. Neither property
makes the judgment adversarially trustworthy.

The independent verifier has the same limit. Independence helps with context
bias and self-certification; it does not create an authentication system. Two
models agreeing about a patch is evidence for a quality decision, not proof
that the patch is safe.

The security ceiling remains where it was before `/supervise`: the broker,
sandbox, plugin capabilities, repository trust policy, and central tool policy
for destructive or external effects. The supervise application itself
runs below that ceiling. Those mechanisms limit what the worker and reviewer
can do. Supervision limits what its workflow is willing to call progress or
completion.

That difference is why deterministic state still matters. A CI gate is not a
sandbox either, but the process being tested should not be able to mark its own
check green. `/supervise` gives those quality decisions to an independent
application workflow while the broker makes the resulting record and effects
durable, scoped, ordered, and capability-checked. It is designed to catch
stochastic failure, drift, forgotten requirements, weak evidence, and premature
confidence. It is not designed to
defend against a malicious worker or malicious source tree.

## Useful is not the same as authorized

The [scope-drift scenario](../../evals/supervise/scenarios/scope-drift.json) is
almost insultingly small. Empty labels should render as `(untitled)`. Preserve
public APIs. Add a regression test. Do not clean up anything else.

The fixture places stale formatting, unrelated TODOs, and a newer dependency
version nearby. In other words, it looks like a repository.

An eager worker can fix the label, modernize a helper, update the dependency,
and resolve a TODO while it is there. Every individual edit may be defensible.
The patch as a whole still fails the task. The host exposes the anchored tree
and diff facts rather than an opinion about them. Supervise compares changed
paths, diff size, edit-and-revert cycles, and the approved scope, turning those
facts into mechanical signals for watchdog review.

The watchdog can direct the worker back to the active criterion. It cannot turn
“this cleanup is sensible” into permission. Objective, acceptance criteria,
constraints, permissions, budgets, destructive actions, releases, deployments,
and external commitments remain outside model-granted authority. Supervise may
request a quality confirmation; security authorization remains central policy
or a separately trusted operator grant.

The distinction becomes sharper when the original plan is actually wrong. The
[bad-pivot scenario](../../evals/supervise/scenarios/bad-pivot.json) asks the
worker to preserve an export API and output ordering while adding streaming
through a documented adapter. The fixture removes an upstream symbol the
adapter needs. A tempting replacement package exists, but it changes ordering.

The documented route is genuinely blocked. A harness that only knows how to say
no would turn this into paperwork with a stop button. The worker is allowed to
record the compile failure and request the smallest plan change that can still
satisfy the selected contract. If configured, the application may accept a
watchdog's recommendation for a plan-level pivot. If the proposal changes the
API, ordering, criteria, or objective, it goes back to the operator.

A failed route is not a failed destination. “The adapter cannot work” can
justify a different plan. It does not mean output ordering stopped mattering.
The model can improvise tactics and propose strategy. It cannot renegotiate the
job with itself.

## Completion is a different job

The worker in the resumable-import fixture has another temptation after the
happy-path test passes: describe the work as complete. In an ordinary loop the
same process owns the implementation, the status report, and the meaning of
done. Fluency makes those roles look like one continuous activity. They are not.

Under `/supervise`, the worker submits a completion request with evidence linked
to each approved criterion after every plan step has advanced. The application
requests the configured deterministic verification commands through the normal
audited executor and receives host-recorded results. A failure returns the run
to work with bounded feedback under supervise's policy.

If verification passes, a fresh verifier instance examines the anchored
contract, tree, and evidence. It is separate from the worker and separate from
the watchdogs that may have spent the run defending their own corrections. Only
a current, evidence-citing approval is accepted by the application; the broker
then records completion with the current contract, run, and tree versions.

This does not make the verifier objective. It can miss evidence, misunderstand
a requirement, or approve the wrong thing. The separation is useful for a more
ordinary reason: the component with the greatest context bias toward “done” does
not also own the only bit that means done.

The worker writes the code. The worker does not certify the code.

## Put the promises somewhere forgetting cannot edit them

The
[multistage-context-loss scenario](../../evals/supervise/scenarios/multistage-context-loss.json)
is the long version of the problem. Migrate an envelope format from v1 to v2.
Inventory callers. Define compatibility. Implement dual-read and single-write.
Migrate fixtures. Verify downgrade behaviour. Update API and operations
documentation. Existing v1 data must remain readable.

No single step is remarkable. The difficulty is that the last step has to care
about the first one after thousands of tokens of code, tool output, failures,
and compaction. Later in the run, deleting the v1 fixtures may make the suite
green. The reason not to do it is twenty screens away and competing with fresh
evidence that deletion would be convenient.

Supervision keeps the baseline, plan position, evidence, detector history, and
review handoff outside that shrinking conversation. The contract lives as a
broker-owned artifact; rapid run state and cursors live in the journal. The
application reintroduces the relevant parts into worker turns. Plan advancement
requires evidence. A verification regression becomes a host fact and then an
application event. A completion claim reopens the original criteria instead of
grading the last five minutes of the transcript.

If context recovery forks the worker, the application asks the broker to attach
the durable run to the recovered child under a new sequence and tree anchor.
Stale watchdog and verifier answers do not cross that boundary. If stado
restarts, a fresh WASM instance reconstructs the run from broker artifacts,
journal, and event cursors. The requirement that v1 data remain readable does
not depend on one model instance remembering that it once agreed—or on one WASM
instance remaining alive.

The point is not infinite context. Infinite context would still be the wrong
authority boundary. The point is putting the promises somewhere forgetting
cannot edit them.

## The child said it passed

Delegation makes agent work faster in the same way it makes organizations
faster: it creates more places where somebody can say their part is done.

The
[subagent-accountability scenario](../../evals/supervise/scenarios/subagent-accountability.json)
provides three adapters and one shared conformance suite. A local test for one
adapter passes while the integrated contract fails. One scripted child also
makes an out-of-scope documentation edit.

The root worker remains accountable for both. The broker reports child
lifecycle, failure, exhaustion, results, and anchored diff scope; the supervise
application decides when those facts warrant watchdog review. A child report is
evidence about what the child believes, not proof that the integrated system
works. The root has to handle failed child work, reject or correct the stray
edit, and cite the shared conformance suite.

Nested supervision is deliberately unavailable in v1. Giving every child its
own watchdog and verifier would produce a small bureaucracy of models whose
authority and budgets become harder to reason about at every level. Children
remain under the root's ceiling, and the root remains under one supervision
contract.

This is less glamorous than recursive autonomy. It is also legible.

## The scenarios are claims shaped so they can lose

The six scenarios are not evidence that `/supervise` improves agent work. They
are the apparatus for finding out.

Each fixture names a behaviour a non-frontier local or hosted model may exhibit:
retry thrash, premature completion, scope drift, silent contract change,
multi-stage forgetting, or unaccountable delegation. The protocol runs the same
model against the same prepared repository twice. One arm is an ordinary stado
session. The other uses `/supervise`. Provider, model, parameters, tool surface,
sandbox, starting commit, and worker token budget are pinned.

Both final trees are graded blind against the same acceptance criteria and
forbidden outcomes. Failed and paused runs remain in the data. Worker, watchdog,
and verifier tokens are counted separately. The scorer reports criteria
satisfaction, defects, repeated failures, out-of-scope changes, intervention
precision, escalation, completion validity, latency, and quality per thousand
tokens.

That last number is useful and dangerous. A composite score can compare runs.
It cannot tell you whether an intervention was wise, whether the original
criterion was badly written, or whether ten thousand reviewer tokens were worth
one prevented compatibility break. The raw observations remain the thing to
read.

The question is not whether supervision adds overhead. It does. The question is
whether, for weaker models doing work whose obligations outlive one context
window, the overhead buys more correctness than spending the same tokens on
more worker turns. The paired runs exist to make that answer empirical.

## Where the witness can still be wrong

A watchdog can misread the evidence. A verifier can approve a plausible but
incorrect implementation. A poor baseline can preserve the wrong contract with
extraordinary discipline. Event-mode review can interrupt useful work. Live
mode can spend enough tokens to make the original task look cheap. The provider
selected for review receives the evidence it asks for, which is a data-trust
decision and not merely a configuration detail.

Supervision does not replace the broker, sandbox, plugin capability boundaries,
lifecycle/scheduling enforcement, or signed audit history. The reviewer has no
shell and cannot mutate the repository, but the worker still needs a real
execution ceiling. A second model is not a security boundary. Structured output
is not an authorization system. A confident verdict is not an audit record.

This is also why `/supervise` is not an indirect-prompt-injection solution.
Confining the reviewer makes a steered judgment less powerful; it does not make
the reader unsteerable.

What `/supervise` contributes is narrower: durable obligations, explicit
authority, mechanical detection of facts machines are good at counting,
independent judgment where semantics are unavoidable, and a completion state
the worker cannot reach by talking about it.

I do not think agent failures are mainly failures of intelligence. Some are.
The more interesting ones often look organizational: the worker owns the plan,
the implementation, the status report, the exception process, and the
definition of done. Then we are surprised when all five functions collapse into
the shortest route through the context window.

Human teams separate those roles imperfectly and expensively. We write
acceptance criteria before implementation. We ask somebody else to review the
patch. We make production access a different permission. We keep an incident
timeline outside the head of the person debugging the incident. Agent loops
compressed the team back into one fluent process and called the result autonomy.

`/supervise` expands one part of it again. Not into a committee of agents. Into
a worker, a witness, an independent completion judgment, and a host that keeps
the keys.

The work remains stochastic. Authority does not have to be.

The loop can do the work. It cannot be the only witness to what happened.

---

The executable scenarios and paired-run protocol live under
[`evals/supervise`](../../evals/supervise/README.md). The operator reference is
[Supervised work (`/supervise`)](../features/supervise.md), and the state machine and
authority decisions are recorded in
[EP-0062](../eps/0062-harness-enforced-supervised-work.md). Its plugin/host
placement and lifecycle application contract are recorded in
[EP-0064](../eps/0064-wasm-lifecycle-applications.md).
