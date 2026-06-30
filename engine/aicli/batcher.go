package aicli

import "context"

// AskQuestionBatcher batches ask_question tool calls from one LLM turn into a
// single researcher sub-session. Set RunBatch on the Agent when ask_mode is
// active; the agent loop intercepts every "ask_question" tool call and routes
// the whole batch through RunBatch instead of through the Registry.
type AskQuestionBatcher struct {
	// RunBatch is called with all questions collected from a single LLM turn.
	// It must return answers in the same order (index 0 answers question 0, etc.).
	RunBatch func(ctx context.Context, questions []string) ([]string, error)
}
