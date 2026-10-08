package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"agent-orchestrator/db"
	"agent-orchestrator/db/models"
	"agent-orchestrator/engine/aicli"
	"agent-orchestrator/engine/aicli/tools"
)

// maxCheckpointMisses is how often a model may answer a demanded checkpoint
// with something else before the demand is dropped until the next interval.
const maxCheckpointMisses = 2

// checkpointTracker decides when an executor must checkpoint. It counts tool
// calls in the conversation rather than keeping its own tally, so it is right
// for a session resumed from its log as well as for a fresh one.
type checkpointTracker struct {
	every int
	// baseline is the number of tool calls in the conversation when the
	// executor last checkpointed, or when the last demand was dropped.
	baseline int
	// rebase asks for the baseline to be moved to the present on the next
	// turn: set when a checkpoint is recorded or a demand is dropped.
	rebase bool
	// demanded is true for the turn in which a checkpoint is the only tool.
	demanded bool
	misses   int
}

// toolCalls counts the tool calls made in a conversation, and how many there
// were at the last call to checkpoint.
func toolCalls(history []aicli.Message) (total, atLastCheckpoint int) {
	for _, message := range history {
		if message.Role != "assistant" {
			continue
		}
		for _, call := range message.ToolCalls {
			total++
			if call.Function.Name == string(aicli.ToolCheckpoint) {
				atLastCheckpoint = total
			}
		}
	}
	return total, atLastCheckpoint
}

// start aligns the tracker with a conversation that already exists.
func (c *checkpointTracker) start(history []aicli.Message) {
	_, c.baseline = toolCalls(history)
}

// due reports whether a checkpoint must be demanded before the next turn,
// and marks it demanded when it is.
func (c *checkpointTracker) due(history []aicli.Message) bool {
	total, _ := toolCalls(history)
	if c.rebase {
		c.baseline, c.rebase = total, false
	}
	if c.demanded || c.every <= 0 || total-c.baseline < c.every {
		return false
	}
	c.demanded, c.misses = true, 0
	return true
}

// demand asks for a checkpoint now, ahead of the interval: something worth
// recording has just happened. It reports whether this newly demanded one.
func (c *checkpointTracker) demand() bool {
	if c.demanded {
		return false
	}
	c.demanded, c.misses = true, 0
	return true
}

// recorded notes that the executor checkpointed.
func (c *checkpointTracker) recorded() {
	c.demanded, c.misses, c.rebase = false, 0, true
}

// missed notes that a demanded checkpoint was not made. It returns false once
// the demand is dropped.
func (c *checkpointTracker) missed() bool {
	c.misses++
	if c.misses < maxCheckpointMisses {
		return true
	}
	c.demanded, c.misses, c.rebase = false, 0, true
	return false
}

func normalizeRecord(text string) string {
	return strings.ToLower(strings.Join(strings.Fields(text), " "))
}

// recordOutcome says what became of the records an executor submitted.
type recordOutcome struct {
	saved      []db.Decision
	duplicates []string
	rejected   []string
}

// message is what the executor is told about its records.
func (o recordOutcome) message() string {
	var parts []string
	if len(o.saved) > 0 {
		ids := make([]string, 0, len(o.saved))
		for _, decision := range o.saved {
			ids = append(ids, fmt.Sprintf("#%d (%s)", decision.ID, decision.Kind))
		}
		parts = append(parts, "Recorded "+strings.Join(ids, ", ")+".")
	}
	if len(o.duplicates) > 0 {
		parts = append(parts, "Already on record, not added again: "+strings.Join(o.duplicates, "; ")+".")
	}
	if len(o.rejected) > 0 {
		parts = append(parts, "Not recorded: "+strings.Join(o.rejected, "; ")+".")
	}
	return strings.Join(parts, " ")
}

// sameRecordsFunc finds entries that restate an existing record in other
// words: it maps an entry's key to the ID of the record it repeats.
type sameRecordsFunc func(ctx context.Context, fresh map[string]tools.RecordInput, existing []db.Decision) map[string]int64

// recordEntries stores the decisions, dead ends and assumptions an executor
// reported, skipping what is already on record for the task: an entry that
// matches a record word for word, and, when same is given, one that restates
// a record in other words. An entry that names an earlier record in `revises`
// supersedes it instead of being added beside it. A skipped duplicate is
// noted in the journal, so nothing an executor reported disappears without a
// trace.
func recordEntries(ctx context.Context, q *db.Queries, task db.Task, run db.Run, logSeq int64, entries map[string][]tools.RecordInput, same sameRecordsFunc) (recordOutcome, error) {
	var outcome recordOutcome
	existing, err := q.ListDecisionsByTask(ctx, task.ID)
	if err != nil {
		return outcome, err
	}
	known := make(map[int64]bool, len(existing))
	seen := make(map[string]int64, len(existing))
	for _, decision := range existing {
		known[decision.ID] = true
		seen[decision.Kind+"|"+normalizeRecord(decision.Title)+"|"+normalizeRecord(decision.Decision)] = decision.ID
	}
	var seq *int64
	if logSeq > 0 {
		seq = &logSeq
	}
	runID, agentID := run.ID, run.AgentID

	// Entries that are not word-for-word repeats may still be restatements.
	var restated map[string]int64
	if same != nil {
		fresh := map[string]tools.RecordInput{}
		for kind, list := range entries {
			for _, entry := range list {
				key := kind + "|" + normalizeRecord(entry.Title) + "|" + normalizeRecord(entry.Detail)
				if _, exact := seen[key]; !exact && entry.Revises == 0 {
					fresh[key] = entry
				}
			}
		}
		restated = same(ctx, fresh, existing)
	}

	for _, kind := range []string{models.DecisionKindDecision, models.DecisionKindDeadEnd, models.DecisionKindAssumption} {
		for _, entry := range entries[kind] {
			key := kind + "|" + normalizeRecord(entry.Title) + "|" + normalizeRecord(entry.Detail)
			id, duplicate := seen[key]
			if !duplicate {
				id, duplicate = restated[key]
			}
			if duplicate && entry.Revises == 0 {
				outcome.duplicates = append(outcome.duplicates, fmt.Sprintf("%q is #%d", entry.Title, id))
				_, _ = q.AppendTaskStep(ctx, db.TaskStep{
					TaskID: task.ID, RootTaskID: task.RootTaskID, Kind: models.StepNote, Phase: models.TaskPhaseExecute,
					AgentID: &agentID, RunID: &runID, Result: fmt.Sprintf("duplicate of #%d: %s", id, entry.Title),
				})
				continue
			}
			row := db.Decision{
				TaskID: task.ID, RootTaskID: task.RootTaskID, RunID: &runID, LogSeq: seq,
				Phase: models.TaskPhaseExecute, AgentID: &agentID, Kind: kind,
				Title: entry.Title, Decision: entry.Detail, Rationale: entry.Reason,
			}
			if len(entry.Alternatives) > 0 {
				encoded, _ := json.Marshal(entry.Alternatives)
				row.Alternatives = string(encoded)
			}
			if entry.Revises != 0 {
				if !known[entry.Revises] {
					outcome.rejected = append(outcome.rejected, fmt.Sprintf("%q revises #%d, which is not a record of this task", entry.Title, entry.Revises))
					continue
				}
				revised := entry.Revises
				row.SupersedesID = &revised
			}
			saved, err := q.CreateDecision(ctx, row)
			if err != nil {
				return outcome, err
			}
			outcome.saved = append(outcome.saved, saved)
			known[saved.ID] = true
			seen[key] = saved.ID
		}
	}
	return outcome, nil
}

// recordsOnFile lists a task's records for the executor, so it can see what
// not to repeat and which ID to revise.
func recordsOnFile(decisions []db.Decision) string {
	if len(decisions) == 0 {
		return ""
	}
	superseded := map[int64]bool{}
	for _, decision := range decisions {
		if decision.SupersedesID != nil {
			superseded[*decision.SupersedesID] = true
		}
	}
	var b strings.Builder
	for _, decision := range decisions {
		if superseded[decision.ID] {
			continue
		}
		fmt.Fprintf(&b, "#%d [%s] %s: %s\n", decision.ID, decision.Kind, oneLine(decision.Title, 100), oneLine(decision.Decision, 200))
	}
	return strings.TrimRight(b.String(), "\n")
}
