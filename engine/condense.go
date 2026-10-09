package engine

import (
	"context"
	"fmt"
	"strings"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/db/models"
	"agent-orchestrator/engine/aicli"
	"agent-orchestrator/engine/workflow"
	"agent-orchestrator/pkg/runtokens"
	"agent-orchestrator/pkg/secrets"
)

const leftOutOfPrompt = "(Left out as not needed for this step. Read it with get_execution_state if you want it.)"

// condense deals with a smart prompt whose context does not fit, so the smart
// model gets a shorter rendering of everything rather than the start of it.
// First the classifier, if there is one, says which reports do not bear on
// the step; those are left out. What still does not fit is condensed by the
// cheap model, one call per section, keeping task numbers so the smart model
// can ask for any part in full. If neither can be done the prompt is used as
// it was composed, cut to fit.
func (d *workflowDriver) condense(ctx context.Context, l *loadedTask, phase string, agent *db.Agent, input workflow.PromptInput, prompt workflow.Prompt) workflow.Prompt {
	usage := callContextFor(l.task, phase, "", agent)

	if gate := classifierFor(ctx, d.q, l.company, taskSession(l.task)); gate != nil {
		items := map[string]string{}
		for i, subtask := range input.Subtasks {
			items[fmt.Sprintf("subtask:%d", i)] = subtask.Title + "\n" + subtask.Summary
		}
		for i, answer := range input.Answers {
			items[fmt.Sprintf("answer:%d", i)] = answer.Question + "\n" + answer.Summary
		}
		deciding := fmt.Sprintf("what to do next on the task %q, which is in its %s step", l.task.Title, phase)
		scores := gate.relevance(ctx, usage, deciding, items)
		dropped := false
		for i := range input.Subtasks {
			if score, judged := scores[fmt.Sprintf("subtask:%d", i)]; judged && score < irrelevantBelow && input.Subtasks[i].Summary != "" {
				input.Subtasks[i].Summary, dropped = leftOutOfPrompt, true
			}
		}
		for i := range input.Answers {
			if score, judged := scores[fmt.Sprintf("answer:%d", i)]; judged && score < irrelevantBelow && input.Answers[i].Summary != "" {
				input.Answers[i].Summary, dropped = leftOutOfPrompt, true
			}
		}
		if dropped {
			prompt = workflow.Compose(input)
			if len(prompt.Overflow) == 0 {
				return prompt
			}
		}
	}

	if l.company.UserID == nil {
		return prompt
	}
	cheap, err := resolveTierDefault(ctx, d.q, *l.company.UserID, models.TierCheap)
	if err != nil {
		return prompt
	}
	usage.Purpose = "compress"
	condensed := map[string]string{}
	for _, section := range prompt.Overflow {
		if text := d.compress(ctx, l.task, cheap, usage, section); text != "" {
			condensed[section.Name] = text
		}
	}
	if len(condensed) == 0 {
		return prompt
	}
	input.Condensed = condensed
	return workflow.Compose(input)
}

// compress asks the cheap model for a shorter rendering of one section. It
// returns nothing if the call fails or the answer is no shorter.
func (d *workflowDriver) compress(ctx context.Context, task db.Task, target modelTarget, usage callContext, section workflow.Overflow) string {
	apiKey, err := secrets.Default().Decrypt(target.Provider.ApiKeyEncrypted)
	if err != nil {
		return ""
	}
	// Leave room for the note that says the section was condensed.
	limit := section.Limit - 200
	if limit < 500 {
		return ""
	}
	client := d.newClient(target.Provider.BaseUrl, apiKey, target.Model)
	client.SessionID = taskSession(task)
	if target.viaGateway() {
		token, revoke := runtokens.Default().IssueCompany(task.CompanyID)
		defer revoke()
		client.ExtraHeaders = map[string]string{runtokens.TokenHeader: token}
	}
	instructions := fmt.Sprintf(`You condense working notes for someone who has to decide what happens next on a task. Below is the section %q of their briefing. Rewrite it in at most %d characters.

Keep, in this order of importance: what failed and why; what was found or produced; what was decided. Keep every task number (such as "Task 12") next to what it refers to, so they can ask for the full text. Keep exact names, paths, numbers and error messages. Drop pleasantries, repetition and narration of steps that led nowhere unless the dead end itself matters. Do not add anything that is not in the text. Answer with the condensed text only.`, section.Title, limit)

	started := time.Now()
	response, _, err := client.Complete(ctx, aicli.ChatRequest{
		Messages: []aicli.Message{{Role: "system", Content: instructions}, {Role: "user", Content: section.Full}},
	})
	if err != nil {
		recordCall(context.Background(), d.q, usage, target, "", aicli.Usage{}, time.Since(started), models.LLMCallError, err.Error())
		return ""
	}
	recordCall(context.Background(), d.q, usage, target, response.Model, response.Usage, time.Since(started), models.LLMCallOK, "")
	if len(response.Choices) == 0 {
		return ""
	}
	text := strings.TrimSpace(response.Choices[0].Message.Content)
	if text == "" || len(text) >= len(section.Full) {
		return ""
	}
	if len(text) > limit {
		text = text[:limit]
	}
	return text
}
