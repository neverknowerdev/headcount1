package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"agent-orchestrator/engine/aicli"
)

type WorkerSummary struct {
	ID           int32  `json:"id"`
	Status       string `json:"status"`
	LatestStatus string `json:"latest_status,omitempty"`
	StartedAt    string `json:"started_at,omitempty"`
	EndedAt      string `json:"ended_at,omitempty"`
	Summary      string `json:"summary,omitempty"`
}

type WorkerControlCallbacks struct {
	RunWorker     func(context.Context, string) (string, error)
	ListWorkers   func(context.Context) ([]WorkerSummary, error)
	GetWorkerInfo func(context.Context, int32) (string, error)
	StopWorker    func(context.Context, int32, string) (string, error)
}

type workerControlTool struct {
	name aicli.ToolName
	def  aicli.ToolDef
	fn   func(context.Context, json.RawMessage) (string, error)
}

func (t *workerControlTool) Def() aicli.ToolDef { return t.def }
func (t *workerControlTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	return t.fn(ctx, args)
}

func NewWorkerControlRegistry(cb WorkerControlCallbacks) *aicli.Registry {
	r := aicli.NewRegistry()
	r.Register(&workerControlTool{name: aicli.ToolRunWorker, def: workerDef(aicli.ToolRunWorker, "Start one bounded ephemeral helper worker and return its run ID immediately.", `{"type":"object","properties":{"prompt":{"type":"string"}},"required":["prompt"]}`), fn: func(ctx context.Context, args json.RawMessage) (string, error) {
		var p struct {
			Prompt string `json:"prompt"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return "", fmt.Errorf("run_worker: %w", err)
		}
		if strings.TrimSpace(p.Prompt) == "" {
			return "", fmt.Errorf("prompt is required")
		}
		return cb.RunWorker(ctx, p.Prompt)
	}})
	r.Register(&workerControlTool{name: aicli.ToolWorkerList, def: workerDef(aicli.ToolWorkerList, "List helper workers owned by this run.", `{"type":"object","properties":{}}`), fn: func(ctx context.Context, _ json.RawMessage) (string, error) {
		v, err := cb.ListWorkers(ctx)
		if err != nil {
			return "", err
		}
		b, e := json.Marshal(v)
		return string(b), e
	}})
	r.Register(&workerControlTool{name: aicli.ToolGetWorkerInfo, def: workerDef(aicli.ToolGetWorkerInfo, "Read one owned helper worker's bounded prompt, progress, and terminal result.", `{"type":"object","properties":{"worker_id":{"type":"integer"}},"required":["worker_id"]}`), fn: func(ctx context.Context, args json.RawMessage) (string, error) {
		var p struct {
			ID int32 `json:"worker_id"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return "", fmt.Errorf("get_worker_info: %w", err)
		}
		if p.ID <= 0 {
			return "", fmt.Errorf("worker_id must be positive")
		}
		return cb.GetWorkerInfo(ctx, p.ID)
	}})
	r.Register(&workerControlTool{name: aicli.ToolStopWorker, def: workerDef(aicli.ToolStopWorker, "Stop one owned helper worker. It is idempotent for terminal workers.", `{"type":"object","properties":{"worker_id":{"type":"integer"},"reason":{"type":"string"}},"required":["worker_id","reason"]}`), fn: func(ctx context.Context, args json.RawMessage) (string, error) {
		var p struct {
			ID     int32  `json:"worker_id"`
			Reason string `json:"reason"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return "", fmt.Errorf("stop_worker: %w", err)
		}
		if p.ID <= 0 || strings.TrimSpace(p.Reason) == "" {
			return "", fmt.Errorf("worker_id and reason are required")
		}
		return cb.StopWorker(ctx, p.ID, p.Reason)
	}})
	return r
}

func workerDef(name aicli.ToolName, description, schema string) aicli.ToolDef {
	return aicli.ToolDef{Type: "function", Function: aicli.FuncMeta{Name: string(name), Description: description, Parameters: json.RawMessage(schema)}}
}
