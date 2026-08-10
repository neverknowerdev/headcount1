package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"agent-orchestrator/engine/aicli"
)

// AskArtifact answers a question about one artifact's content without loading
// the content into the calling agent's context: the engine runs a separate
// one-shot LLM call that reads the artifact and returns a short answer.
type AskArtifact struct {
	fn func(ctx context.Context, filename, question string) (string, error)
}

// ArtifactReaderTarget describes the one-shot model endpoint used by the
// ask_artifact tool. Cleanup is called after the request for any caller-owned
// temporary resources.
type ArtifactReaderTarget struct {
	BaseURL      string
	APIKey       string
	Model        string
	ProviderName string
	ExtraHeaders map[string]string
	Cleanup      func()
}

// ArtifactReader contains the implementation of the ask_artifact tool. The
// engine supplies storage/model/auth callbacks, while prompt construction and
// the one-shot completion stay with the per-tool implementation.
type ArtifactReader struct {
	FindArtifact  func(ctx context.Context, filename string) (string, error)
	ResolveTarget func(ctx context.Context) (ArtifactReaderTarget, error)
	RecordUsage   func(ctx context.Context, usage aicli.Usage)
	LogRequest    func(model, provider string, body []byte)
	LogResponse   func(model, provider string, body []byte, usage aicli.Usage)
}

// Answer reads one artifact through a separate model call and returns only a
// concise answer to the asking agent.
func (r *ArtifactReader) Answer(ctx context.Context, filename, question string) (string, error) {
	content, err := r.FindArtifact(ctx, filename)
	if err != nil {
		return "", err
	}

	const maxArtifactChars = 100000
	truncNote := ""
	if len(content) > maxArtifactChars {
		content = content[:maxArtifactChars]
		truncNote = "\n\n[Document truncated for length — the answer is based on the first part only.]"
	}
	prompt := fmt.Sprintf(`You answer questions about a document. Answer concisely — a short, direct answer (a few sentences at most), quoting brief evidence from the document when helpful. Base the answer ONLY on the document; if the document does not contain the answer, say so plainly.

Document %q:
%s%s

Question: %s`, filename, content, truncNote, question)

	target, err := r.ResolveTarget(ctx)
	if err != nil {
		return "", err
	}
	if target.Cleanup != nil {
		defer target.Cleanup()
	}
	client := aicli.NewClient(target.BaseURL, target.APIKey, target.Model)
	client.ExtraHeaders = target.ExtraHeaders
	request := aicli.ChatRequest{
		Messages:  []aicli.Message{{Role: "user", Content: prompt}},
		MaxTokens: 500,
	}
	requestBody, err := json.Marshal(request)
	if err != nil {
		return "", fmt.Errorf("artifact reader: marshal request: %w", err)
	}
	if r.LogRequest != nil {
		r.LogRequest(target.Model, target.ProviderName, requestBody)
	}
	resp, responseBody, err := client.Complete(ctx, request)
	if err != nil {
		return "", fmt.Errorf("artifact reader call failed: %w", err)
	}
	if r.LogResponse != nil {
		r.LogResponse(target.Model, target.ProviderName, responseBody, resp.Usage)
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("artifact reader returned no answer")
	}
	if r.RecordUsage != nil {
		r.RecordUsage(ctx, resp.Usage)
	}
	answer := strings.TrimSpace(resp.Choices[0].Message.Content)
	if answer == "" {
		return "", fmt.Errorf("artifact reader returned an empty answer")
	}
	return fmt.Sprintf("Answer about %q: %s", filename, answer), nil
}

func NewAskArtifact(fn func(ctx context.Context, filename, question string) (string, error)) *AskArtifact {
	return &AskArtifact{fn: fn}
}

func (t *AskArtifact) Def() aicli.ToolDef {
	return aicli.ToolDef{
		Type: "function",
		Function: aicli.FuncMeta{
			Name: string(aicli.ToolAskArtifact),
			Description: "Ask a question about an artifact's content and get a short answer, without reading the artifact " +
				"into your own context (a separate lightweight reader answers from the document). " +
				"Use it to verify deliverables: \"Does it contain a Roadmap section?\", \"Which files does it say were changed?\". " +
				"Ask one specific question at a time; use list_artifacts first to see what exists.",
			Parameters: json.RawMessage(`{
				"type":"object",
				"properties":{
					"filename":{
						"type":"string",
						"description":"Artifact filename, e.g. \"report.md\""
					},
					"question":{
						"type":"string",
						"description":"One specific question about the artifact's content"
					}
				},
				"required":["filename","question"]
			}`),
		},
	}
}

func (t *AskArtifact) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Filename string `json:"filename"`
		Question string `json:"question"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("ask_artifact: %w", err)
	}
	if p.Filename == "" {
		return "", fmt.Errorf("filename is required")
	}
	if p.Question == "" {
		return "", fmt.Errorf("question is required")
	}
	return t.fn(ctx, p.Filename, p.Question)
}
