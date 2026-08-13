# Resumable Sessions and Fast Binary Updates

## Goal

Make an agent session resumable by starting a fresh in-memory agent runtime with
the session's persisted conversation and execution checkpoint. Use that
capability to make binary updates wait only until active sessions reach a safe,
durable pause point, rather than waiting for long runs to finish.

The resume primitive must not be limited to planned update pauses. Its contract
must also be capable of recovering failed and stale sessions in the future,
although the first release will automatically resume only sessions that were
intentionally paused for a restart.

No frontend resume action or public resume API is part of this work. The resume
operation remains an engine-level Go function used by startup and update
orchestration.

## Current State and Gaps

The repository already contains an initial implementation:

- `aicli.Agent.RunWithHistory` can continue from saved messages.
- `NativeEngine.BeginDrain` prevents new runs and requests a pause at an LLM
  turn boundary.
- `Run.PausedHistory` stores a checkpoint under the `interrupted` status.
- startup calls `ResumeInterruptedRuns`.
- unit, engine, isolated-restart, and real deploy E2E tests cover one root run.

The current path is not yet the general recovery mechanism this design needs:

- only root runs can pause; delegated and blocking sessions can keep an update
  waiting;
- `interrupted` conflates an intentional pause with abnormal interruption;
- the checkpoint is cleared before resumed execution safely starts;
- listing and claiming resumable runs is not atomic across processes;
- startup resumption is asynchronous and can race new work;
- only one safe boundary is supported: after an LLM response and before its
  tools;
- update shutdown proceeds after its timeout even if a session was not safely
  checkpointed;
- failed and stale runs are finalized and unlocked instead of retaining a
  reusable recovery checkpoint.

## Lifecycle and Terminology

Keep a stable logical `Run.ID` across recovery. Resuming creates a new
in-memory agent runtime/execution attempt, not a second logical run. This
preserves task locking, run names, hierarchy, logs, token totals, and links
from existing UI and API consumers.

Use explicit runtime states:

```text
running -> paused -------------> resuming -> running -> completed/failed/canceled
       \-> recoverable_failed -/
       \-> stale --------------/
```

`recoverable_failed` and `stale` describe recovery eligibility, not an
automatic retry policy. The first release automatically selects only `paused`
runs whose pause reason permits automatic recovery.

A run is resumable only when it has a valid durable checkpoint. A failed or
stale run without a checkpoint is not resumable and continues through the
existing failure/unlock behavior.

## 1. Add a Durable, Versioned Checkpoint

Replace the update-specific `PausedHistory` concept with a general session
checkpoint. It may initially remain fields on `Run`, but its data contract must
be explicit and versioned.

Persist at least:

- complete, unpruned chat history;
- checkpoint schema version;
- execution phase (`before_tools` or `after_tools`);
- turn count and any other limits that must survive a restart;
- checkpoint creation time;
- recovery class: `paused`, `failed`, or `stale`;
- pause/failure reason and original error;
- initiator when known (`deploy_webhook`, user ID, system, watchdog);
- target build or commit when the reason is a binary update;
- resume attempt count and last resume error;
- resume lease owner and expiry.

Keep checkpoint contents internal to backend responses. Existing run logs may
show lifecycle events, but raw history must not be exposed as a new API field.

For failures, checkpoint before converting the run to a terminal or recoverable
failure state whenever the agent loop still has valid history. For staleness,
use the latest durable checkpoint rather than trying to reconstruct state from
log text.

Primary files:

- `db/models.go`
- `db/queries_runs.go`
- `db/querier.go`
- database migration and backend compatibility tests

## 2. Introduce One General Code-Only Resume Function

Add an engine operation conceptually shaped as:

```go
type ResumeCause string

const (
    ResumeAfterUpdate ResumeCause = "binary_update"
    ResumeAfterFailure ResumeCause = "failed_recovery"
    ResumeAfterStale   ResumeCause = "stale_recovery"
)

type ResumeOptions struct {
    Cause       ResumeCause
    InitiatorID *int32
    Reason      string
    TargetBuild string
}

func (e *NativeEngine) ResumeSession(
    ctx context.Context,
    runID int32,
    opts ResumeOptions,
) error
```

The exact names can follow local conventions, but there must be only one
reconstruction and continuation path for paused, failed, and stale sessions.

`ResumeSession` must:

1. atomically verify that the requested run is eligible for the supplied
   recovery cause;
2. claim it with a lease and transition it to `resuming`;
3. load and validate its checkpoint before mutating task ownership;
4. reconstruct the task, agent, provider, tools, workspace, credentials,
   logger, and hierarchy;
5. instantiate a fresh `aicli.Agent`;
6. restore the full conversation and pending execution phase;
7. add a short synthetic system message explaining that the session was
   resumed, by whom when known, and why;
8. retain the same logical run and task lock;
9. transition to `running` only after the runtime is registered and ready;
10. retain the prior checkpoint until a new durable checkpoint or terminal
    result has been written.

If reconstruction fails, release the lease and return the run to its previous
recoverable state with `last_resume_error`. Do not silently unlock its task or
erase its history.

The resume message must not invalidate tool-call ordering. If the checkpoint
ends with an assistant tool call, execute or reattach that pending tool first,
append its durable result, and add the resume message before the next LLM
request.

## 3. Separate Eligibility from Automatic Recovery Policy

Implement a selector/policy layer above `ResumeSession`:

- `paused` plus reason `binary_update` or another explicitly automatic reason:
  eligible for automatic startup resume now;
- `recoverable_failed`: eligible for an explicit future recovery caller, but
  not automatically resumed now;
- `stale` with a valid checkpoint: eligible for an explicit future recovery
  caller, but not automatically resumed now;
- terminal runs without a valid checkpoint: rejected;
- completed or canceled runs: never resumed unless a future, separately
  designed replay/fork feature is introduced.

Replace `ResumeInterruptedRuns` with a coordinator such as
`ResumeEligibleSessions`. In the first release its startup policy queries only
intentionally paused sessions, then invokes the same general `ResumeSession`
function that future failed/stale recovery will use.

This avoids coupling the core capability to today's update workflow without
creating an unsafe automatic retry loop for arbitrary failures.

## 4. Make Resume Claiming Crash-Safe

Use a conditional database update for the claim:

```text
eligible recovery state -> resuming, lease_owner=X, lease_expires_at=T
```

Required behavior:

- only one process can claim a run;
- the durable checkpoint remains present throughout `resuming`;
- a crash after claim but before execution does not lose resumability;
- expired `resuming` leases return to their prior recovery state or can be
  reclaimed directly;
- a successful resume clears the old checkpoint only after a newer checkpoint
  or terminal state is durable;
- SQLite and PostgreSQL use equivalent conditional semantics;
- startup resumption and incoming task updates cannot create duplicate runs.

Startup should finish claiming eligible paused sessions before accepting new
agent work. The HTTP server may become available, but task execution must stay
behind a startup recovery gate until claims are established.

## 5. Expand Safe Checkpoint Boundaries

The agent loop must support checkpoints at both safe boundaries:

1. after a complete LLM response, before any returned tool calls start;
2. after a complete tool batch and persisted tool results, before the next LLM
   request.

Never interrupt an executing tool. If pause is requested during a tool, let the
tool finish and checkpoint immediately afterward. A restored pending tool call
must execute once rather than being represented to the model as completed.

Persist total turn usage across resumes so repeatedly resuming a run does not
reset safety limits such as `maxTurns`.

Primary files:

- `engine/aicli/agent.go`
- `engine/aicli/agent_test.go`
- `engine/native_engine.go`

## 6. Make Tool Execution Recoverable and Idempotent

Persist tool execution identity keyed by at least `(run_id, tool_call_id)` with:

- tool name and arguments hash;
- `pending`, `completed`, or `failed` state;
- durable result or error;
- start and completion timestamps.

Before executing a restored tool call:

- replay an already completed durable result without repeating the side
  effect;
- reattach to a known durable pending operation when supported;
- run a never-started call normally;
- stop and mark the session recoverable when a pending call cannot safely be
  classified.

This ledger supports planned pauses today and safe recovery of failed or stale
sessions later.

## 7. Make Delegated and Blocking Sessions Resumable

The current in-memory parent/child channels cannot survive a process restart.
Persist enough coordination state to reconstruct the session tree:

- track every root and delegated run in the active-session registry;
- use existing `ParentRunID` and `RootRunID` as durable hierarchy identity;
- persist parent waits and the child/tool call they are waiting for;
- resume children before parents;
- make replayed `create_subtask` attach to the existing child rather than
  create a duplicate;
- persist question/answer state for `ask_task_owner` and
  `answer_subtask_question`;
- represent `ask_human` as a durable waiting state so an update does not wait
  for the human response;
- recreate ephemeral MCP or browser connections after restart and inform the
  agent when non-conversational tool state was reset.

Failed or stale child sessions must retain their original error and checkpoint
and remain linked to the parent. A future explicit recovery of that child must
allow the parent to reattach and receive its eventual result.

## 8. Refactor Binary Update into Prepare, Pause, Commit, Restart

Change the deployment sequence to:

1. download the candidate binary to a staged path;
2. verify source, checksum, permissions, and target identity;
3. enter quiescing mode and reject new run starts with an explicit retriable
   result;
4. request `binary_update` pauses for every active session;
5. wait until every session is terminal or has a durable checkpoint;
6. abort the restart if the pause deadline expires, leaving the current binary
   running and the candidate staged or safely discarded;
7. atomically replace the executable only after checkpointing succeeds;
8. seal the keyring and close the HTTP listener;
9. `exec` the new binary;
10. claim and automatically resume sessions paused for this update.

The update coordinator must not treat failed or stale sessions as automatic
startup work. Those states are supported by `ResumeSession` for future use but
require an explicit policy or caller.

Primary files:

- `pkg/updater/updater.go`
- `main.go`
- `server/controllers/deploy.go`

## 9. Logging and Observability

Write durable lifecycle events for:

- pause requested;
- checkpoint written, including safe-boundary phase;
- session claimed for resume;
- session resumed, with cause, initiator, and target build where applicable;
- resume claim rejected or lease reclaimed;
- resume reconstruction failed;
- session classified as recoverable failed or stale;
- update aborted because one or more sessions could not checkpoint safely.

Continue the existing JSONL stream with monotonic sequence numbers. Emit
`run_paused` and `run_resumed` events rather than representing a pause as an
ordinary `run_ended` followed by a new run.

## 10. Test Plan

### Agent-loop tests

- exact full-history continuation;
- synthetic resume message and metadata;
- pause before tools;
- pause after tool results;
- restored pending tool executes exactly once;
- completed tool result is replayed without repeating its side effect;
- terminal response completes instead of unnecessarily pausing;
- turn limit remains cumulative across resumes;
- corrupt or unsupported checkpoint version is rejected without data loss.

### Database and engine tests

- `ResumeSession` accepts an intentionally paused run;
- the same function accepts a recoverable failed run when explicitly called;
- the same function accepts a stale run with a valid checkpoint when explicitly
  called;
- failed/stale runs without checkpoints are rejected;
- automatic startup policy selects paused update runs only;
- two engine instances race to claim one run and only one wins;
- process death after claim is recovered through lease expiry;
- checkpoint is retained until safe replacement;
- task lock survives pause, failure recovery, stale recovery, and resume;
- invalid reconstruction records `last_resume_error` and remains recoverable;
- child-first tree recovery does not duplicate subtasks;
- `ask_human` and owner-question waits survive restart;
- SQLite and PostgreSQL behavior is equivalent.

### Restart E2E tests

Extend `e2e/tests/auto_update_resume.spec.ts` to cover:

- several concurrent sessions pausing and resuming;
- shutdown during an executing tool, proving restart waits for tool completion;
- pause at both supported checkpoint phases;
- delegated parent and child sessions surviving restart;
- a waiting-for-human session surviving restart;
- successor killed after claiming a run, followed by successful recovery on the
  next boot;
- no duplicate tool side effects, subtask rows, or run rows;
- full history and the resume reason reaching the first post-resume LLM call.

### Real deploy E2E tests

Update `e2e/tests/deploy_webhook.spec.ts` to prove:

- the binary remains staged until every checkpoint is durable;
- all sessions paused for the deployment resume in the target build;
- pause timeout aborts the deployment without losing a run;
- target commit, logical run identity, hierarchy, task lock, history, logs, and
  final status are preserved;
- failed or stale checkpoints are not automatically resumed by the update
  startup policy.

## Delivery Sequence

Implement in reviewable slices:

1. versioned checkpoint model, explicit states, and database tests;
2. general `ResumeSession` with atomic lease claiming and tests for paused,
   failed, and stale eligibility;
3. expanded agent-loop safe boundaries and durable tool execution ledger;
4. active-session registry plus durable delegated/blocking waits;
5. prepare/pause/commit updater refactor and startup recovery gate;
6. expanded isolated restart and real deploy E2E coverage;
7. remove the legacy `interrupted`/`PausedHistory` path after migration and
   compatibility tests pass.

## Acceptance Criteria

- A planned binary update does not wait for an entire long-running agent run;
  it waits only for all active sessions to reach durable safe points.
- No session is stopped in the middle of an LLM response or ordinary tool
  execution.
- Every session paused for the update resumes in the new binary with complete
  history, stable logical identity, and an explanatory resume message.
- Pending or previously completed tools are not duplicated during resume.
- A crash during the resume handoff leaves the session recoverable.
- Delegated and human-waiting sessions do not indefinitely block a planned
  update.
- The internal resume primitive can explicitly resume paused, recoverable
  failed, and stale sessions with valid checkpoints.
- Automatic recovery in this release remains limited to intentional paused
  sessions, preventing uncontrolled retries of arbitrary failures.
- No frontend resume control or public resume endpoint is introduced.
