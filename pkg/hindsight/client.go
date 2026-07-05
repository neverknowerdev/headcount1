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
		return nil, fmt.Errorf("hindsight %s %s: %d — %s", method, path, resp.StatusCode, strings.TrimSpace(string(b)))
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
