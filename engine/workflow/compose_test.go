package workflow

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-orchestrator/db/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var updateGolden = flag.Bool("update", false, "rewrite the composer's golden files")

// golden compares a composed prompt with its checked-in file. Run
// `go test ./engine/workflow -run Compose -update` after an intended change
// and review the diff: these files are the prompts smart models actually see.
func golden(t *testing.T, name string, prompt Prompt) {
	t.Helper()
	got := "=== SYSTEM ===\n" + prompt.System + "\n\n=== USER ===\n" + prompt.User + "\n"
	path := filepath.Join("testdata", name+".golden")
	if *updateGolden {
		require.NoError(t, os.WriteFile(path, []byte(got), 0o644))
		return
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "missing golden file; run with -update")
	assert.Equal(t, string(want), got)
}

func firstRefineInput() PromptInput {
	return PromptInput{
		RolePrompt:  "You are the CEO agent.\n\nAlways prefer boring technology.",
		Phase:       models.TaskPhaseRefine,
		TaskRef:     "HC1-12",
		TaskTitle:   "Add single sign-on",
		TaskType:    models.TaskTypeCoding,
		Description: "Customers want to log in with their company identity provider.",
		StepsLeft:   40,
		AdjustsLeft: 3,
	}
}

func fullAdjustInput() PromptInput {
	return PromptInput{
		RolePrompt:    "You are the CTO agent.",
		Phase:         models.TaskPhaseAdjust,
		TaskRef:       "HC1-12",
		TaskTitle:     "Add single sign-on",
		TaskType:      models.TaskTypeCoding,
		Description:   "Customers want to log in with their company identity provider.",
		ParentContext: "HC1-3 — Enterprise readiness: make the product sellable to large companies.",
		Spec:          "Support SAML login against Okta and Azure AD.\nExisting password login must keep working.",
		DefinitionOfDone: []SpecItem{
			{ID: 1, Text: "A user can log in through Okta", Status: "failed", Note: "500 on the callback"},
			{ID: 2, Text: "Password login still works", Status: "passed", Note: "seen in the test run"},
		},
		Design:        "Add a SAML handler behind /auth/saml; store IdP metadata per company.",
		TestScenarios: []SpecItem{{ID: 1, Text: "A valid assertion logs the user in", Status: "pending"}},
		Humans:        []HumanExchange{{Question: "Which identity providers matter first?", Answer: "Okta, then Azure AD."}},
		Answers: []Answer{
			{TaskID: 21, Question: "How is authentication implemented today?", Kind: models.TaskTypeResearch, Status: models.TaskStatusDone,
				Summary: "Passkeys only, in server/controllers/auth_webauthn.go."},
			{TaskID: 22, Question: "Is there a SAML library already vendored?", Kind: models.TaskTypeResearch, Status: models.TaskStatusFailed,
				Reason: models.TaskResultCannotComplete, Summary: "go.mod has none; could not determine whether one is acceptable to add."},
			{TaskID: 23, Question: "Do the auth tests pass on main?", Kind: models.TaskTypeReview, Status: models.TaskStatusDone,
				Verdict: models.TaskVerdictApproved, Summary: "All 41 pass."},
		},
		Subtasks: []SubtaskReport{
			{TaskID: 31, RefKey: "HC1-12-4", Title: "SAML handler", Type: models.TaskTypeCoding, Role: "Coder", Status: models.TaskStatusDone,
				Summary: "Handler added with signature validation."},
			{TaskID: 32, RefKey: "HC1-12-5", Title: "Review: SAML handler", Type: models.TaskTypeReview, Role: "QA", Status: models.TaskStatusDone,
				Verdict: models.TaskVerdictChangesRequested, Summary: "Callback returns 500 when the assertion has no NameID.\nNo test covers it.", DependsOn: []int32{31}},
			{TaskID: 33, RefKey: "HC1-12-6", Title: "Metadata storage", Type: models.TaskTypeCoding, Role: "Coder", Status: models.TaskStatusFailed,
				Reason: models.TaskResultReportedFailure, Summary: "Migration conflicts with an existing column."},
			{TaskID: 34, RefKey: "HC1-12-7", Title: "Admin UI", Type: models.TaskTypeCoding, Status: models.TaskStatusCanceled,
				Reason: models.TaskResultPrerequisiteFailed, DependsOn: []int32{33}},
			{TaskID: 20, RefKey: "HC1-12-2", Title: "First attempt at the handler", Type: models.TaskTypeCoding, Status: models.TaskStatusFailed,
				Reason: models.TaskResultRunError, Handled: true},
		},
		Decisions:   "HC1-12 Add single sign-on\n  #4 [decision] Use SAML, not OIDC — both target IdPs support it\n  #7 [dead_end] crewjam/saml v0.3 — panics on unsigned assertions",
		Situation:   "Verification failed: fix the callback.\nAlso failing: task 33 failed (reported_failure); task 34 canceled (prerequisite_failed).",
		Inspections: []Inspection{{Tool: ToolGetExecutionState, Output: "Task 33 details:\nColumn companies.metadata already exists with type text."}},
		StepsLeft:   3,
		AdjustsLeft: 0,
	}
}

func TestComposeFirstRefinementStep(t *testing.T) {
	prompt := Compose(firstRefineInput())
	golden(t, "compose_refine_first", prompt)
	assert.Empty(t, prompt.Truncated)
	assert.True(t, strings.HasPrefix(prompt.System, "You are the CEO agent."), "the role speaks first")
	assert.Contains(t, prompt.System, "Always prefer boring technology.", "the user's additions are part of the role")
	assert.Contains(t, prompt.System, "Phase: refinement.")
	for _, absent := range []string{"## Specification", "## Delegated work", "## Decisions so far", "## Why you are here"} {
		assert.NotContains(t, prompt.User, absent, "empty sections are left out")
	}
}

func TestComposeAdjustmentWithEverything(t *testing.T) {
	prompt := Compose(fullAdjustInput())
	golden(t, "compose_adjust_full", prompt)
	assert.Empty(t, prompt.Truncated)
	assert.Contains(t, prompt.System, "Phase: adjustment.")
	// Sections appear in a fixed order, most stable first.
	order := []string{"## Task", "## Why you are here", "## Specification", "## Definition of done", "## Technical design",
		"## Test scenarios", "## Answers from the human", "## Answers to your questions", "## Delegated work",
		"## Decisions so far", "## What you asked to read", "## Your move"}
	last := -1
	for _, heading := range order {
		at := strings.Index(prompt.User, heading)
		require.Greater(t, at, last, "%s is missing or out of order", heading)
		last = at
	}
	assert.Contains(t, prompt.User, "NOT ANSWERED (could not be completed)", "an unanswerable question says so")
	assert.Contains(t, prompt.User, "[NOT met]")
	assert.Contains(t, prompt.User, "already dealt with by an earlier re-plan")
	assert.Contains(t, prompt.User, "You have 3 steps left")
	assert.Contains(t, prompt.User, "last re-plan allowed")
}

// Composition is deterministic: the same input gives the same prompt, so a
// step can be reproduced exactly from what the journal recorded.
func TestComposeIsDeterministic(t *testing.T) {
	first, second := Compose(fullAdjustInput()), Compose(fullAdjustInput())
	assert.Equal(t, first, second)
}

func TestComposeEveryPhaseHasAGoal(t *testing.T) {
	for _, phase := range []string{models.TaskPhaseRefine, models.TaskPhaseDesign, models.TaskPhaseTestPlan,
		models.TaskPhasePlan, models.TaskPhaseAdjust, models.TaskPhaseVerify} {
		in := firstRefineInput()
		in.Phase = phase
		assert.Contains(t, Compose(in).System, "Phase: ", phase)
		assert.Contains(t, Compose(in).System, "Goal:", phase)
	}
}

func TestComposeCutsOnlyWhatDoesNotFit(t *testing.T) {
	in := fullAdjustInput()
	in.Subtasks[0].Summary = strings.Repeat("A long report line about the handler.\n", 400)
	whole := Compose(in)
	require.Empty(t, whole.Truncated, "well under the default budget")

	in.Budget = 4000
	cut := Compose(in)
	assert.Equal(t, []string{"subtasks"}, cut.Truncated, "only the oversized section is cut")
	assert.Less(t, len(cut.User), len(whole.User))
	assert.Contains(t, cut.User, "[Cut to fit.")
	// Everything that fits is intact, and the sections that are never cut —
	// the task, why the model is here, the definition of done — always are.
	for _, kept := range []string{"Customers want to log in", "Verification failed: fix the callback.", "A user can log in through Okta",
		"Add a SAML handler behind /auth/saml", "Okta, then Azure AD.", "Passkeys only", "#7 [dead_end]", "Column companies.metadata already exists"} {
		assert.Contains(t, cut.User, kept)
	}
	assert.LessOrEqual(t, len(cut.User), 4000+600, "the context respects its budget, give or take headings")
}

func TestFitSharesSpaceByWeight(t *testing.T) {
	sections := []section{
		{name: "fixed", body: strings.Repeat("f", 100)},
		{name: "small", body: strings.Repeat("s", 50), weight: 1},
		{name: "big", body: strings.Repeat("b", 5000), weight: 1},
		{name: "bigger", body: strings.Repeat("c", 5000), weight: 3},
		{name: "empty", weight: 5},
	}
	cut := fit(sections, 1150)
	require.Len(t, cut, 2)
	// Each cut section is reported in full, with the room it has.
	assert.Equal(t, []string{"big", "bigger"}, []string{cut[0].Name, cut[1].Name})
	assert.Len(t, cut[0].Full, 5000)
	assert.Equal(t, []int{250, 750}, []int{cut[0].Limit, cut[1].Limit})
	assert.Len(t, sections[0].body, 100, "a section with no weight is never cut")
	assert.Len(t, sections[1].body, 50, "a section within its share is left whole")
	// 1150 - 100 fixed - 50 small = 1000 left, shared 1:3.
	assert.LessOrEqual(t, len(sections[2].body), 250)
	assert.LessOrEqual(t, len(sections[3].body), 750)
	assert.Greater(t, len(sections[3].body), len(sections[2].body))

	untouched := []section{{name: "a", body: "short", weight: 1}}
	assert.Nil(t, fit(untouched, 100))
	assert.Equal(t, "short", untouched[0].body)
}

func TestTruncateEndsOnALineWhenItCan(t *testing.T) {
	// The cut snaps back to a line end when that keeps most of the text.
	body := "line one is here\nline two is also here\nline three is the long one that gets cut in the middle"
	cut := truncate(body, 45+len(truncationNote))
	assert.True(t, strings.HasPrefix(cut, "line one is here\nline two is also here"))
	assert.NotContains(t, cut, "line th")
	assert.True(t, strings.HasSuffix(cut, truncationNote))
	assert.Equal(t, "fits", truncate("fits", 100+len(truncationNote)))
}

// The smart model cannot open files, so it is told what the human attached
// and how to find out what is in them.
func TestComposeNamesAttachedFiles(t *testing.T) {
	prompt := Compose(PromptInput{Phase: models.TaskPhaseRefine, TaskTitle: "Summarise the contract", TaskType: models.TaskTypeResearch,
		Attachments: []string{"contract.pdf", "annex.png"}})
	require.Contains(t, prompt.User, "Files attached by the human: contract.pdf, annex.png")
	require.Contains(t, prompt.User, "You cannot open them")

	without := Compose(PromptInput{Phase: models.TaskPhaseRefine, TaskTitle: "Summarise the contract", TaskType: models.TaskTypeResearch})
	require.NotContains(t, without.User, "attached")
}

// A section that did not fit can be handed back condensed: it then replaces
// the cut text, and says that it is a condensed rendering.
func TestComposeUsesACondensedSectionInPlaceOfACutOne(t *testing.T) {
	long := strings.Repeat("Task 12 failed because the export endpoint returned 401. ", 400)
	in := PromptInput{
		Phase: models.TaskPhaseAdjust, TaskTitle: "Publish prices", TaskType: models.TaskTypeGeneral,
		Subtasks: []SubtaskReport{{TaskID: 12, Title: "Export", Type: "general", Status: models.TaskStatusFailed, Summary: long}},
		Budget:   3000,
	}
	cut := Compose(in)
	require.Equal(t, []string{"subtasks"}, cut.Truncated)
	require.Len(t, cut.Overflow, 1)
	assert.Equal(t, "Delegated work", cut.Overflow[0].Title)
	assert.Contains(t, cut.Overflow[0].Full, long[:200])
	assert.Contains(t, cut.User, "[Cut to fit.")

	in.Condensed = map[string]string{"subtasks": "Task 12 (Export) failed: the export endpoint returns 401 on every attempt."}
	condensed := Compose(in)
	assert.Empty(t, condensed.Truncated)
	assert.Contains(t, condensed.User, "Task 12 (Export) failed: the export endpoint returns 401 on every attempt.")
	assert.Contains(t, condensed.User, "[Condensed to fit.")
	assert.NotContains(t, condensed.User, "[Cut to fit.")
}
