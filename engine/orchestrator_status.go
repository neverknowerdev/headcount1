package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/engine/aicli/tools"
	"gorm.io/gorm"
)

const maxNestedStatusDepth = 5

func (e *NativeEngine) orchestratorSessionLastRunStatus(ctx context.Context, task db.Task, orchestratorRunID, id int32) (tools.ManagedSessionStatusReport, error) {
	run, err := e.orchestratorSessionRun(ctx, orchestratorRunID, id)
	if err != nil {
		return tools.ManagedSessionStatusReport{}, err
	}
	report, reportErr := e.q.GetLatestRunStatusReport(ctx, id)
	if reportErr != nil && !errors.Is(reportErr, gorm.ErrRecordNotFound) {
		return tools.ManagedSessionStatusReport{}, reportErr
	}
	now := time.Now()
	result := tools.ManagedSessionStatusReport{ID: run.ID, Name: run.Name, TaskID: run.TaskID, AgentID: run.AgentID, AgentName: run.Agent.Name}
	if reportErr == nil {
		result.OwnReportedStatus = report.Status
		result.LastReportedAt = report.ReportedAt.Format(time.RFC3339Nano)
		result.LastReportedMessageID = report.MessageID
	}
	children, truncated, err := e.nestedSessionStatuses(ctx, run, now, 0)
	if err != nil {
		return tools.ManagedSessionStatusReport{}, err
	}
	result.ChildStatuses = children
	result.NestedStatusTruncated = truncated
	result.LastReportedStatus = aggregateSessionStatus(result.OwnReportedStatus, children)
	result.StatusReportStale = isStatusReportStale(report, reportErr == nil, now)
	if result.StatusReportStale && !isTerminalRunStatus(run.Status) {
		requested, requestErr := e.requestWorkerStatus(ctx, task, orchestratorRunID, id)
		if requestErr != nil {
			return tools.ManagedSessionStatusReport{}, requestErr
		}
		result.StatusRefreshRequested = requested
	}
	return result, nil
}

func (e *NativeEngine) nestedSessionStatuses(ctx context.Context, run db.Run, now time.Time, depth int) ([]tools.ManagedSessionChildStatus, bool, error) {
	children, err := e.q.ListChildRuns(ctx, run.ID)
	if err != nil {
		return nil, false, err
	}
	if depth >= maxNestedStatusDepth {
		return nil, len(children) > 0, nil
	}
	result := make([]tools.ManagedSessionChildStatus, 0, len(children))
	truncated := false
	for _, child := range children {
		node, childTruncated, nodeErr := e.nestedSessionStatus(ctx, child, now, depth+1)
		if nodeErr != nil {
			return nil, false, nodeErr
		}
		result = append(result, node)
		truncated = truncated || childTruncated
	}
	return result, truncated, nil
}

func (e *NativeEngine) nestedSessionStatus(ctx context.Context, run db.Run, now time.Time, depth int) (tools.ManagedSessionChildStatus, bool, error) {
	report, reportErr := e.q.GetLatestRunStatusReport(ctx, run.ID)
	if reportErr != nil && !errors.Is(reportErr, gorm.ErrRecordNotFound) {
		return tools.ManagedSessionChildStatus{}, false, reportErr
	}
	hasReport := reportErr == nil
	node := tools.ManagedSessionChildStatus{ID: run.ID, Name: run.Name, AgentName: run.Agent.Name, StatusReportStale: isStatusReportStale(report, hasReport, now)}
	if node.AgentName == "" {
		node.AgentName = run.Name
	}
	if hasReport {
		node.OwnReportedStatus = report.Status
		node.LastReportedAt = report.ReportedAt.Format(time.RFC3339Nano)
		node.LastReportedMessageID = report.MessageID
	}
	node.Status = node.OwnReportedStatus
	if node.Status == "" && !isTerminalRunStatus(run.Status) {
		node.Status = run.Status
	}
	children, truncated, err := e.nestedSessionStatuses(ctx, run, now, depth)
	if err != nil {
		return tools.ManagedSessionChildStatus{}, false, err
	}
	node.ChildStatuses = children
	node.NestedStatusTruncated = truncated
	node.Status = aggregateSessionStatus(node.Status, children)
	return node, truncated, nil
}

func aggregateSessionStatus(own string, children []tools.ManagedSessionChildStatus) string {
	result := own
	for _, child := range children {
		status := child.Status
		if status == "" {
			status = "no status reported"
		}
		label := child.AgentName
		if label == "" {
			label = child.Name
		}
		if label == "" {
			label = fmt.Sprintf("session %d", child.ID)
		}
		if result != "" {
			if !strings.HasSuffix(result, ".") {
				result += "."
			}
			result += " "
		}
		result += fmt.Sprintf("%s status: %s", label, status)
	}
	return result
}

func isTerminalRunStatus(status string) bool {
	switch status {
	case "completed", "failed", "canceled", db.RunStatusRecoverableFailed, db.RunStatusStale, "interrupted":
		return true
	default:
		return false
	}
}

func isStatusReportStale(report db.RunStatusReport, hasReport bool, now time.Time) bool {
	return !hasReport || now.Sub(report.ReportedAt) > statusReportFreshness
}
