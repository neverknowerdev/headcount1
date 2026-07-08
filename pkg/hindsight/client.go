// Package hindsight integrates the Hindsight memory engine
// (https://github.com/vectorize-io/hindsight) as the long-term memory layer.
// Client is a thin HTTP client for the Hindsight REST API; Service layers the
// bank-naming and ingestion conventions of this app on top of it; Manager
// runs a bare-metal hindsight-api process configured with the cheap utility
// model.
package hindsight

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	BaseURL string
	HTTP    *http.Client
}

func NewClient(baseURL string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		HTTP:    &http.Client{Timeout: 120 * time.Second},
	}
}

// MemoryItem is one unit of content for retain.
type MemoryItem struct {
	Content    string            `json:"content"`
	Timestamp  string            `json:"timestamp,omitempty"` // ISO 8601, or "unset" for timeless reference docs
	Context    string            `json:"context,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
	DocumentID string            `json:"document_id,omitempty"`
	Tags       []string          `json:"tags,omitempty"`
	UpdateMode string            `json:"update_mode,omitempty"` // "replace" (default) or "append"
	// ObservationScopes controls at which tag granularity Hindsight builds
	// consolidated observations from this item, e.g. [["project:12"]] scopes
	// consolidation to that project instead of only the whole-bank default.
	ObservationScopes [][]string `json:"observation_scopes,omitempty"`
}

type retainRequest struct {
	Items []MemoryItem `json:"items"`
	Async bool         `json:"async"`
}

type RecallRequest struct {
	Query     string   `json:"query"`
	Types     []string `json:"types,omitempty"`
	Budget    string   `json:"budget,omitempty"` // low | mid | high
	MaxTokens int      `json:"max_tokens,omitempty"`
	Tags      []string `json:"tags,omitempty"`
	TagsMatch string   `json:"tags_match,omitempty"` // any | all | any_strict | all_strict | exact
	// PreferObservations drops raw world/experience facts that a returned
	// observation already consolidates, backfilling the freed slots — so the
	// higher-quality, deduplicated observation is what callers see instead
	// of both the observation and the raw facts it was built from.
	PreferObservations bool `json:"prefer_observations,omitempty"`
}

type RecallResult struct {
	ID         string            `json:"id"`
	Text       string            `json:"text"`
	Type       string            `json:"type,omitempty"`
	Entities   []string          `json:"entities,omitempty"`
	Context    string            `json:"context,omitempty"`
	OccurredAt string            `json:"occurred_start,omitempty"`
	DocumentID string            `json:"document_id,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
	Tags       []string          `json:"tags,omitempty"`
}

type RecallResponse struct {
	Results []RecallResult `json:"results"`
}

type ReflectResponse struct {
	Text string `json:"text"`
}

// APIError is a non-2xx response from the Hindsight API. Callers that proxy
// Hindsight to the frontend use StatusCode to pass the upstream status
// through instead of flattening everything into 502.
type APIError struct {
	StatusCode int
	Method     string
	Path       string
	Body       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("hindsight %s %s: %d — %s", e.Method, e.Path, e.StatusCode, e.Body)
}

// IsNotFound reports whether err is a Hindsight 404.
func IsNotFound(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound
}

func (c *Client) do(ctx context.Context, method, path string, body io.Reader, contentType string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		return nil, &APIError{
			StatusCode: resp.StatusCode, Method: method, Path: path,
			Body: strings.TrimSpace(string(b)),
		}
	}
	return resp, nil
}

func (c *Client) doJSON(ctx context.Context, method, path string, in, out interface{}) error {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	resp, err := c.do(ctx, method, path, body, "application/json")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// doRaw returns the raw JSON response body, used by API proxy endpoints where
// the frontend consumes Hindsight's response shape directly.
func (c *Client) doRaw(ctx context.Context, method, path string, in interface{}) ([]byte, error) {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(data)
	}
	resp, err := c.do(ctx, method, path, body, "application/json")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func bankPath(bankID, suffix string) string {
	return "/v1/default/banks/" + url.PathEscape(bankID) + suffix
}

func (c *Client) Health(ctx context.Context) error {
	return c.doJSON(ctx, http.MethodGet, "/health", nil, nil)
}

func (c *Client) Retain(ctx context.Context, bankID string, items []MemoryItem, async bool) error {
	if len(items) == 0 {
		return nil
	}
	return c.doJSON(ctx, http.MethodPost, bankPath(bankID, "/memories"), retainRequest{Items: items, Async: async}, nil)
}

func (c *Client) Recall(ctx context.Context, bankID string, req RecallRequest) (RecallResponse, error) {
	var out RecallResponse
	err := c.doJSON(ctx, http.MethodPost, bankPath(bankID, "/memories/recall"), req, &out)
	return out, err
}

func (c *Client) Reflect(ctx context.Context, bankID, query, budget string) (ReflectResponse, error) {
	var out ReflectResponse
	req := map[string]interface{}{"query": query}
	if budget != "" {
		req["budget"] = budget
	}
	err := c.doJSON(ctx, http.MethodPost, bankPath(bankID, "/reflect"), req, &out)
	return out, err
}

type BankInfo struct {
	BankID string `json:"bank_id"`
	Name   string `json:"name,omitempty"`
}

func (c *Client) ListBanks(ctx context.Context) ([]BankInfo, error) {
	var out struct {
		Banks []BankInfo `json:"banks"`
	}
	err := c.doJSON(ctx, http.MethodGet, "/v1/default/banks", nil, &out)
	return out.Banks, err
}

func (c *Client) DeleteBank(ctx context.Context, bankID string) error {
	return c.doJSON(ctx, http.MethodDelete, bankPath(bankID, ""), nil, nil)
}

// CreateBank makes sure the bank row exists. Hindsight creates banks lazily
// on retain, but PATCH /config is a no-op UPDATE against a missing bank and
// POST /directives fails its foreign key — so bank setup must start here.
func (c *Client) CreateBank(ctx context.Context, bankID string) error {
	return c.doJSON(ctx, http.MethodPut, bankPath(bankID, ""), map[string]interface{}{}, nil)
}

// UpdateBankConfig applies per-bank configuration overrides (mission,
// disposition, etc). Keys use Hindsight's Python field names, e.g.
// "reflect_mission", "disposition_skepticism".
func (c *Client) UpdateBankConfig(ctx context.Context, bankID string, updates map[string]interface{}) error {
	return c.doJSON(ctx, http.MethodPatch, bankPath(bankID, "/config"), map[string]interface{}{"updates": updates}, nil)
}

func (c *Client) GetBankConfigRaw(ctx context.Context, bankID string) ([]byte, error) {
	return c.doRaw(ctx, http.MethodGet, bankPath(bankID, "/config"), nil)
}

// CreateDirective adds a hard rule enforced during reflect (as opposed to
// disposition, which only softly influences it).
func (c *Client) CreateDirective(ctx context.Context, bankID, name, content string) error {
	return c.doJSON(ctx, http.MethodPost, bankPath(bankID, "/directives"),
		map[string]string{"name": name, "content": content}, nil)
}

func (c *Client) ListDirectivesRaw(ctx context.Context, bankID string) ([]byte, error) {
	return c.doRaw(ctx, http.MethodGet, bankPath(bankID, "/directives"), nil)
}

// ListMemoriesRaw proxies GET /memories/list (params: q, type, document_id, limit, offset).
func (c *Client) ListMemoriesRaw(ctx context.Context, bankID string, params url.Values) ([]byte, error) {
	return c.doRaw(ctx, http.MethodGet, bankPath(bankID, "/memories/list")+"?"+params.Encode(), nil)
}

func (c *Client) GetMemoryRaw(ctx context.Context, bankID, memoryID string) ([]byte, error) {
	return c.doRaw(ctx, http.MethodGet, bankPath(bankID, "/memories/"+url.PathEscape(memoryID)), nil)
}

// UpdateMemory curates a memory: patch may contain text, context and/or
// state ("invalidated" to soft-delete, "active" to restore).
func (c *Client) UpdateMemory(ctx context.Context, bankID, memoryID string, patch map[string]interface{}) ([]byte, error) {
	return c.doRaw(ctx, http.MethodPatch, bankPath(bankID, "/memories/"+url.PathEscape(memoryID)), patch)
}

func (c *Client) GraphRaw(ctx context.Context, bankID string, params url.Values) ([]byte, error) {
	return c.doRaw(ctx, http.MethodGet, bankPath(bankID, "/graph")+"?"+params.Encode(), nil)
}

func (c *Client) EntitiesGraphRaw(ctx context.Context, bankID string, params url.Values) ([]byte, error) {
	return c.doRaw(ctx, http.MethodGet, bankPath(bankID, "/entities/graph")+"?"+params.Encode(), nil)
}

func (c *Client) StatsRaw(ctx context.Context, bankID string) ([]byte, error) {
	return c.doRaw(ctx, http.MethodGet, bankPath(bankID, "/stats"), nil)
}

func (c *Client) ListDocumentsRaw(ctx context.Context, bankID string) ([]byte, error) {
	return c.doRaw(ctx, http.MethodGet, bankPath(bankID, "/documents"), nil)
}

func (c *Client) DeleteDocument(ctx context.Context, bankID, documentID string) error {
	return c.doJSON(ctx, http.MethodDelete, bankPath(bankID, "/documents/"+url.PathEscape(documentID)), nil, nil)
}

// ExportDocuments downloads the bank's document-transfer ZIP archive
// (documents, chunks, facts, entities; embeddings are rebuilt on import).
func (c *Client) ExportDocuments(ctx context.Context, bankID string) ([]byte, error) {
	resp, err := c.do(ctx, http.MethodGet, bankPath(bankID, "/document-transfer?include_observations=true"), nil, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

// ImportDocuments uploads a document-transfer ZIP archive into a bank.
func (c *Client) ImportDocuments(ctx context.Context, bankID string, archive []byte, onConflict string) error {
	if onConflict == "" {
		onConflict = "replace"
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", bankID+".zip")
	if err != nil {
		return err
	}
	if _, err := fw.Write(archive); err != nil {
		return err
	}
	mw.Close()
	resp, err := c.do(ctx, http.MethodPost,
		bankPath(bankID, "/document-transfer?on_conflict="+url.QueryEscape(onConflict)),
		&buf, mw.FormDataContentType())
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// CreateMentalModelRequest mirrors Hindsight's mental-model creation body.
// A custom lowercase-hyphenated ID lets callers fetch a model deterministically
// without maintaining an ID-lookup table.
type CreateMentalModelRequest struct {
	ID                        string   `json:"id,omitempty"`
	Name                      string   `json:"name"`
	SourceQuery               string   `json:"source_query"`
	Tags                      []string `json:"tags,omitempty"`
	MaxTokens                 int      `json:"max_tokens,omitempty"`
	RefreshAfterConsolidation bool     `json:"-"` // translated into the trigger object below
}

type mentalModelTrigger struct {
	RefreshAfterConsolidation bool `json:"refresh_after_consolidation"`
}

type createMentalModelBody struct {
	ID          string             `json:"id,omitempty"`
	Name        string             `json:"name"`
	SourceQuery string             `json:"source_query"`
	Tags        []string           `json:"tags,omitempty"`
	MaxTokens   int                `json:"max_tokens,omitempty"`
	Trigger     mentalModelTrigger `json:"trigger"`
}

// CreateMentalModel creates a mental model (async — content generates in the
// background via reflect). Returns the mental_model_id (echoes req.ID when
// set) and an operation_id for progress tracking; callers typically ignore
// the operation and just poll GetMentalModelRaw later.
func (c *Client) CreateMentalModel(ctx context.Context, bankID string, req CreateMentalModelRequest) (string, error) {
	body := createMentalModelBody{
		ID: req.ID, Name: req.Name, SourceQuery: req.SourceQuery,
		Tags: req.Tags, MaxTokens: req.MaxTokens,
		Trigger: mentalModelTrigger{RefreshAfterConsolidation: req.RefreshAfterConsolidation},
	}
	var out struct {
		MentalModelID string `json:"mental_model_id"`
	}
	if err := c.doJSON(ctx, http.MethodPost, bankPath(bankID, "/mental-models"), body, &out); err != nil {
		return "", err
	}
	return out.MentalModelID, nil
}

func (c *Client) GetMentalModelRaw(ctx context.Context, bankID, id string) ([]byte, error) {
	return c.doRaw(ctx, http.MethodGet, bankPath(bankID, "/mental-models/"+url.PathEscape(id)), nil)
}

func (c *Client) ListMentalModelsRaw(ctx context.Context, bankID string) ([]byte, error) {
	return c.doRaw(ctx, http.MethodGet, bankPath(bankID, "/mental-models"), nil)
}

// CreateMentalModelRaw creates a mental model from an arbitrary user-supplied
// payload (the Memory UI's "new mental model" form) and returns Hindsight's
// raw JSON response ({mental_model_id, operation_id}).
func (c *Client) CreateMentalModelRaw(ctx context.Context, bankID string, body map[string]interface{}) ([]byte, error) {
	return c.doRaw(ctx, http.MethodPost, bankPath(bankID, "/mental-models"), body)
}

// UpdateMentalModel edits a mental model's name/source_query/tags/max_tokens/
// trigger. Only fields present in patch are changed.
func (c *Client) UpdateMentalModel(ctx context.Context, bankID, id string, patch map[string]interface{}) ([]byte, error) {
	return c.doRaw(ctx, http.MethodPatch, bankPath(bankID, "/mental-models/"+url.PathEscape(id)), patch)
}

func (c *Client) RefreshMentalModel(ctx context.Context, bankID, id string) error {
	return c.doJSON(ctx, http.MethodPost, bankPath(bankID, "/mental-models/"+url.PathEscape(id)+"/refresh"), nil, nil)
}

func (c *Client) DeleteMentalModel(ctx context.Context, bankID, id string) error {
	return c.doJSON(ctx, http.MethodDelete, bankPath(bankID, "/mental-models/"+url.PathEscape(id)), nil, nil)
}

// GetMentalModelHistoryRaw returns the model's refresh history — content
// snapshots over time, the closest thing to "past LLM runs" for a model
// that the Memory UI can show without a full LLM-trace viewer.
func (c *Client) GetMentalModelHistoryRaw(ctx context.Context, bankID, id string) ([]byte, error) {
	return c.doRaw(ctx, http.MethodGet, bankPath(bankID, "/mental-models/"+url.PathEscape(id)+"/history"), nil)
}

// ListTagsRaw lists a bank's known tags with usage counts, used by the
// Memory UI to suggest tags when creating/editing a mental model.
func (c *Client) ListTagsRaw(ctx context.Context, bankID string) ([]byte, error) {
	return c.doRaw(ctx, http.MethodGet, bankPath(bankID, "/tags"), nil)
}

// ExportBankTemplate exports a bank's config, mental models and directives
// as an opaque JSON manifest. Unlike ExportDocuments (document-transfer),
// this does NOT carry memories — the two exports are complementary and both
// needed for a lossless bank backup once EnsureBank/mental models are in use.
func (c *Client) ExportBankTemplate(ctx context.Context, bankID string) ([]byte, error) {
	return c.doRaw(ctx, http.MethodGet, bankPath(bankID, "/export"), nil)
}

// ImportBankTemplate applies a previously exported manifest to a bank,
// creating it if missing. Mental models are matched by id, directives by
// name — existing ones are updated in place, so this is safe to call
// repeatedly (e.g. on every restore).
func (c *Client) ImportBankTemplate(ctx context.Context, bankID string, manifest []byte) error {
	var body interface{}
	if err := json.Unmarshal(manifest, &body); err != nil {
		return fmt.Errorf("invalid bank template manifest: %w", err)
	}
	return c.doJSON(ctx, http.MethodPost, bankPath(bankID, "/import"), body, nil)
}
