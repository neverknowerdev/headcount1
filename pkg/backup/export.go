package backup

import (
	"archive/tar"
	"encoding/json"
	"fmt"
	"log"
	"path"
	"sort"

	"gorm.io/gorm"
)

// The entity export/import format (backup manifest version 2).
//
// Every DB table is exported as raw column-name → value JSON so the archive
// is a faithful, human-readable snapshot of the database, organized as a
// company-scoped tree:
//
//	entities/
//	  llm-providers.json  model-groups.json  model-group-members.json
//	  default-model-settings.json  mcp-servers.json  mcp-accounts.json
//	  agent-mcp-servers.json  agent-mcp-accounts.json  agent-mcp-tool-filters.json
//	  activity-logs.json
//	  companies/{shortName}/company.json
//	    agents/{id}.json
//	    sprints/{id}.json
//	    skills.json
//	    projects/{name}/project.json
//	    tasks/{id}/task.json
//	    tasks/{id}/comments.json
//	    tasks/{id}/attachments.json
//	    tasks/{id}/artifacts.json
//	    tasks/{id}/runs/{runID}.json
//
// IDs are exported verbatim and preserved on restore (restore wipes the
// tables first), so cross-entity references and on-disk paths that embed
// IDs (logs/{company}/{taskID}/..., uploads/{taskID}/...) stay valid.
//
// Deliberately not exported: provider_presets (reseeded at startup),
// model_request_stats and proxy_request_logs (high-volume request
// telemetry), run_log_entries-style derived data (rebuilt from JSONL logs).

type row = map[string]interface{}

func readTable(database *gorm.DB, table string) ([]row, error) {
	var rows []row
	if err := database.Table(table).Find(&rows).Error; err != nil {
		return nil, err
	}
	sort.Slice(rows, func(i, j int) bool { return toID(rows[i]) < toID(rows[j]) })
	return rows, nil
}

// toID extracts the numeric id column of an exported row. Rows without an
// id (join tables) sort as 0, keeping their original order.
func toID(r row) int64 {
	switch v := r["id"].(type) {
	case int64:
		return v
	case int32:
		return int64(v)
	case float64:
		return int64(v)
	default:
		return 0
	}
}

func marshalRows(rows []row) []byte {
	data, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		return []byte("[]")
	}
	return data
}

func marshalRow(r row) []byte {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return []byte("{}")
	}
	return data
}

// exportEntities writes the full entity tree under entities/ into the tar
// archive. Returns the number of files written.
func exportEntities(tw *tar.Writer, database *gorm.DB) (int, error) {
	count := 0
	write := func(name string, data []byte) {
		writeTarEntry(tw, path.Join("entities", name), data)
		count++
	}

	// Global (non-company-scoped) tables, one JSON array per file.
	globals := []struct{ table, file string }{
		{"llm_providers", "llm-providers.json"},
		{"model_groups", "model-groups.json"},
		{"model_group_members", "model-group-members.json"},
		{"default_model_settings", "default-model-settings.json"},
		{"mcp_servers", "mcp-servers.json"},
		{"mcp_accounts", "mcp-accounts.json"},
		{"agent_mcp_servers", "agent-mcp-servers.json"},
		{"agent_mcp_accounts", "agent-mcp-accounts.json"},
		{"agent_mcp_tool_filters", "agent-mcp-tool-filters.json"},
		{"activity_logs", "activity-logs.json"},
	}
	for _, g := range globals {
		rows, err := readTable(database, g.table)
		if err != nil {
			log.Printf("Warning: backup: failed to read table %s: %v", g.table, err)
			continue
		}
		write(g.file, marshalRows(rows))
	}

	companies, err := readTable(database, "companies")
	if err != nil {
		return count, fmt.Errorf("failed to read companies: %w", err)
	}

	// Pre-read company-scoped and task-scoped tables, then group in memory.
	byCompany := map[string]map[int64][]row{} // table -> companyID -> rows
	for _, table := range []string{"agents", "sprints", "skills", "projects", "tasks"} {
		rows, err := readTable(database, table)
		if err != nil {
			log.Printf("Warning: backup: failed to read table %s: %v", table, err)
			continue
		}
		grouped := map[int64][]row{}
		for _, r := range rows {
			cid := asInt64(r["company_id"])
			grouped[cid] = append(grouped[cid], r)
		}
		byCompany[table] = grouped
	}
	byTask := map[string]map[int64][]row{} // table -> taskID -> rows
	for _, table := range []string{"comments", "attachments", "artifacts", "runs"} {
		rows, err := readTable(database, table)
		if err != nil {
			log.Printf("Warning: backup: failed to read table %s: %v", table, err)
			continue
		}
		grouped := map[int64][]row{}
		for _, r := range rows {
			tid := asInt64(r["task_id"])
			grouped[tid] = append(grouped[tid], r)
		}
		byTask[table] = grouped
	}

	for _, comp := range companies {
		cid := toID(comp)
		shortName, _ := comp["short_name"].(string)
		if shortName == "" {
			shortName = fmt.Sprintf("company-%d", cid)
		}
		dir := path.Join("companies", shortName)

		write(path.Join(dir, "company.json"), marshalRow(comp))

		for _, a := range byCompany["agents"][cid] {
			write(path.Join(dir, "agents", fmt.Sprintf("%d.json", toID(a))), marshalRow(a))
		}
		for _, s := range byCompany["sprints"][cid] {
			write(path.Join(dir, "sprints", fmt.Sprintf("%d.json", toID(s))), marshalRow(s))
		}
		if skills := byCompany["skills"][cid]; len(skills) > 0 {
			write(path.Join(dir, "skills.json"), marshalRows(skills))
		}
		for _, p := range byCompany["projects"][cid] {
			projName, _ := p["name"].(string)
			if projName == "" {
				projName = fmt.Sprintf("project-%d", toID(p))
			}
			write(path.Join(dir, "projects", projName, "project.json"), marshalRow(p))
		}
		for _, t := range byCompany["tasks"][cid] {
			tid := toID(t)
			taskDir := path.Join(dir, "tasks", fmt.Sprintf("%d", tid))
			write(path.Join(taskDir, "task.json"), marshalRow(t))
			if rows := byTask["comments"][tid]; len(rows) > 0 {
				write(path.Join(taskDir, "comments.json"), marshalRows(rows))
			}
			if rows := byTask["attachments"][tid]; len(rows) > 0 {
				write(path.Join(taskDir, "attachments.json"), marshalRows(rows))
			}
			if rows := byTask["artifacts"][tid]; len(rows) > 0 {
				write(path.Join(taskDir, "artifacts.json"), marshalRows(rows))
			}
			for _, r := range byTask["runs"][tid] {
				write(path.Join(taskDir, "runs", fmt.Sprintf("%d.json", toID(r))), marshalRow(r))
			}
		}
	}

	return count, nil
}

func asInt64(v interface{}) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int32:
		return int64(n)
	case int:
		return int64(n)
	case float64:
		return int64(n)
	default:
		return 0
	}
}
