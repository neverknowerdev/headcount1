package engine

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"agent-orchestrator/db"
	"agent-orchestrator/db/models"
	"agent-orchestrator/engine/aicli"
	"agent-orchestrator/engine/aicli/tools"
	"agent-orchestrator/engine/classifier"
	"agent-orchestrator/pkg/runtokens"
	"agent-orchestrator/pkg/secrets"
)

const (
	// gateThreshold is how sure the classifier must be for a yes to count.
	gateThreshold = 0.7
	// loopThreshold is how sure it must be that a session is going in circles.
	loopThreshold = 0.8
	// maxLoopHits is how many turns in a row may look like a loop before the
	// session is told to stop and report.
	maxLoopHits = 2
	// repeatedCallLimit is how many identical tool calls in a row are a loop
	// on their face, classifier or not.
	repeatedCallLimit = 6
	// irrelevantBelow is the probability under which a report is left out of
	// a prompt that does not fit.
	irrelevantBelow = 0.25
)

// gate is a company's classifier: a model that answers yes/no and
// which-of-these questions for almost nothing. The engine never depends on
// it. Every question it is asked has a fixed rule to fall back on, so with no
// classifier configured, or with one that is failing, the workflow behaves
// the same, only less finely.
type gate struct {
	q         *db.Queries
	client    *classifier.Client
	target    modelTarget
	companyID int32
	// failures counts the calls in a row that got no answer. A classifier
	// that keeps failing is left alone for the rest of what this gate serves
	// (one executor session, one prompt), which goes on by the fixed rules.
	failures int
}

// maxGateFailures is how many failed calls in a row end a gate's use.
const maxGateFailures = 3

// classifierFor returns the classifier configured for a company, or nil when
// there is none to use: the slot is empty, what it names is not a System One
// model, or the key is sealed. The slot holds a provider's System One model
// or a group of them; a group is reached through the gateway, which routes
// between its members as it does for language models. session names the
// conversation the calls belong to.
func classifierFor(ctx context.Context, q *db.Queries, company db.Company, session string) *gate {
	if company.UserID == nil {
		return nil
	}
	target, err := resolveDefaultModel(ctx, q, *company.UserID, db.PurposeClassifier, models.TierClassifier)
	if err != nil || target.vaultLocked() {
		return nil
	}
	// The slot only takes System One models, but what it names can change
	// after it was chosen; a language model here would only fail every call.
	if target.viaGateway() {
		if target.group.Kind != models.ModelKindSystemOne {
			return nil
		}
	} else if !models.IsSystemOneModel(target.Model) {
		return nil
	}
	apiKey, err := secrets.Default().Decrypt(target.Provider.ApiKeyEncrypted)
	if err != nil {
		return nil
	}
	client := classifier.New(target.Provider.BaseUrl, apiKey, target.Model)
	client.Headers = map[string]string{"User-Agent": aicli.UserAgent}
	if session != "" {
		client.Headers[aicli.SessionHeader] = session
	}
	return &gate{q: q, client: client, target: target, companyID: company.ID}
}

// ask puts questions to the classifier and records the call. It returns nil
// when the classifier could not answer; the caller then applies its fixed
// rule, as it would with no classifier at all.
func (g *gate) ask(ctx context.Context, usage callContext, state string, questions map[string]classifier.Question) map[string]classifier.Answer {
	if g == nil || len(questions) == 0 || g.failures >= maxGateFailures {
		return nil
	}
	client := g.client
	if g.target.viaGateway() {
		// The call belongs to a company but to no run, so it shows the
		// in-process gateway a token of its own, valid for this call.
		token, revoke := runtokens.Default().IssueCompany(g.companyID)
		defer revoke()
		if token == "" {
			return nil
		}
		routed := *g.client
		routed.Headers = map[string]string{runtokens.TokenHeader: token}
		for name, value := range g.client.Headers {
			routed.Headers[name] = value
		}
		client = &routed
	}
	answers, result, err := client.Ask(ctx, state, questions)
	tokens := aicli.Usage{PromptTokens: result.InputTokens, CompletionTokens: result.OutputTokens}
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			g.failures++
			recordCall(context.Background(), g.q, usage, g.target, result.Model, tokens, result.Duration, models.LLMCallError, err.Error())
		}
		return nil
	}
	g.failures = 0
	recordCall(context.Background(), g.q, usage, g.target, result.Model, tokens, result.Duration, models.LLMCallOK, "")
	return answers
}

// sinceLastCheckpoint renders what an executor did after its last checkpoint
// as the classifier sees it: what the model said, which tools it called with
// what, and the start of each result.
func sinceLastCheckpoint(history []aicli.Message) string {
	start := 0
	for i, message := range history {
		if message.Role != "assistant" {
			continue
		}
		for _, call := range message.ToolCalls {
			if call.Function.Name == string(aicli.ToolCheckpoint) {
				start = i + 1
			}
		}
	}
	var b strings.Builder
	for _, message := range history[start:] {
		switch message.Role {
		case "assistant":
			if text := strings.TrimSpace(message.Content); text != "" {
				fmt.Fprintf(&b, "AGENT: %s\n", oneLine(text, 600))
			}
			for _, call := range message.ToolCalls {
				fmt.Fprintf(&b, "CALL %s(%s)\n", call.Function.Name, oneLine(call.Function.Arguments, 300))
			}
		case "tool":
			fmt.Fprintf(&b, "RESULT: %s\n", oneLine(message.Content, 300))
		}
	}
	return b.String()
}

// repeatedCalls counts how many of the most recent tool calls are the same
// call with the same arguments.
func repeatedCalls(history []aicli.Message) int {
	last, count := "", 0
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role != "assistant" {
			continue
		}
		for j := len(history[i].ToolCalls) - 1; j >= 0; j-- {
			call := history[i].ToolCalls[j].Function
			signature := call.Name + "\x00" + call.Arguments
			if count == 0 {
				last = signature
			} else if signature != last {
				return count
			}
			count++
		}
	}
	return count
}

// turnVerdict is what the classifier made of an executor's recent turns.
type turnVerdict struct {
	// notable: something worth recording happened: a choice among
	// alternatives, an approach given up, an assumption left unverified.
	notable bool
	// looping: the session is repeating itself without learning anything.
	looping bool
}

// judgeTurns asks the classifier, in one request, what has happened in an
// executor session since its last checkpoint.
func (g *gate) judgeTurns(ctx context.Context, usage callContext, history []aicli.Message) turnVerdict {
	state := sinceLastCheckpoint(history)
	if g == nil || strings.TrimSpace(state) == "" {
		return turnVerdict{}
	}
	usage.Purpose = "checkpoint_gate"
	answers := g.ask(ctx, usage, state, map[string]classifier.Question{
		"committed": classifier.Noul("The log shows an agent working on a task. Did the agent settle on one approach after weighing it against at least one alternative?"),
		"abandoned": classifier.Noul("The log shows an agent working on a task. Did the agent give up on an approach because it failed or turned out not to work?"),
		"assumed":   classifier.Noul("The log shows an agent working on a task. Did the agent go ahead on something it assumed to be true without checking it?"),
		"looping":   classifier.Noul("The log shows an agent working on a task. Is the agent repeating the same actions and getting nothing new from them?"),
	})
	if answers == nil {
		return turnVerdict{}
	}
	return turnVerdict{
		notable: answers["committed"].Noul >= gateThreshold || answers["abandoned"].Noul >= gateThreshold || answers["assumed"].Noul >= gateThreshold,
		looping: answers["looping"].Noul >= loopThreshold,
	}
}

// sameRecords finds, among entries an executor wants recorded, the ones that
// say what a record already on file says in other words. It returns the ID of
// the existing record for each such entry, by the entry's key.
func (g *gate) sameRecords(ctx context.Context, usage callContext, fresh map[string]tools.RecordInput, existing []db.Decision) map[string]int64 {
	if g == nil || len(fresh) == 0 || len(existing) == 0 {
		return nil
	}
	if len(existing) > classifier.MaxChoiceOptions-1 {
		existing = existing[len(existing)-(classifier.MaxChoiceOptions-1):]
	}
	options := map[string]string{"none": "None of the records says this; it is new."}
	byLabel := make(map[string]int64, len(existing))
	var state strings.Builder
	state.WriteString("Records already on file for a task:\n")
	for _, decision := range existing {
		label := fmt.Sprintf("r%d", decision.ID)
		text := oneLine(decision.Title+": "+decision.Decision, 300)
		options[label] = text
		byLabel[label] = decision.ID
		fmt.Fprintf(&state, "%s [%s] %s\n", label, decision.Kind, text)
	}
	keys := make([]string, 0, len(fresh))
	for key := range fresh {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	questions := make(map[string]classifier.Question, len(keys))
	names := make(map[string]string, len(keys))
	for i, key := range keys {
		name := fmt.Sprintf("entry%d", i)
		names[name] = key
		entry := fresh[key]
		questions[name] = classifier.Choice(
			"A new entry is about to be recorded: \""+oneLine(entry.Title+": "+entry.Detail, 400)+
				"\". Which record already on file states the same thing? Choose none unless one clearly does.", options)
	}
	usage.Purpose = "record_dedupe"
	answers := g.ask(ctx, usage, state.String(), questions)
	same := map[string]int64{}
	for name, answer := range answers {
		if id, known := byLabel[answer.Choice]; known && answer.Confidence >= gateThreshold {
			same[names[name]] = id
		}
	}
	return same
}

// relevance asks which of several numbered items bear on a decision, and
// returns the probability for each item's key. Items the classifier did not
// answer for are absent, and are kept.
func (g *gate) relevance(ctx context.Context, usage callContext, deciding string, items map[string]string) map[string]float64 {
	if g == nil || len(items) == 0 {
		return nil
	}
	keys := make([]string, 0, len(items))
	for key := range items {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var state strings.Builder
	fmt.Fprintf(&state, "A decision is being made: %s\n\nReports available:\n", deciding)
	questions := make(map[string]classifier.Question, len(keys))
	names := make(map[string]string, len(keys))
	for i, key := range keys {
		name := fmt.Sprintf("item%d", i)
		names[name] = key
		fmt.Fprintf(&state, "\n[%s]\n%s\n", name, oneLine(items[key], 1500))
		questions[name] = classifier.Noul("Does the report marked [" + name + "] contain something needed to make the decision?")
	}
	usage.Purpose = "relevance_gate"
	answers := g.ask(ctx, usage, state.String(), questions)
	if answers == nil {
		return nil
	}
	scores := make(map[string]float64, len(answers))
	for name, answer := range answers {
		if key, known := names[name]; known {
			scores[key] = answer.Noul
		}
	}
	return scores
}
