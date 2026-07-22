package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/pkg/filesystem"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Tenant-scoped backup & restore (archive format "tenant-v1").
//
// Unlike the whole-DB backup in backup.go/export.go/restore.go (an operator DR
// tool that swaps every tenant at once), this exports exactly one user's / team's
// company subtree and re-imports it into ANY database — including one that
// already holds other tenants — without ID collisions or cross-tenant clobbering.
//
// The two hard problems it solves:
//
//   - ID remapping. The archive's primary keys are NOT assumed free. Every row is
//     re-inserted letting the DB assign a fresh id; an old→new id map is built per
//     table and every foreign key (company→project→task(parent/child)→run,
//     provider/model-group links, agent/mcp assignments, artifacts, comments,
//     attachments) is rewritten through it.
//
//   - Path rewriting. On-disk directories embed IDs and the company short name
//     (uploads/{taskID}, artifacts/{co}/{rootTaskID}, logs/{co}/{rootTaskID}/run-{rootRunID},
//     workspace/{co}/task-{id}, repos/{co}, skills/{co}). Files are copied to their
//     remapped locations and the absolute *_path columns (attachments.file_path,
//     artifacts.file_path, runs.log_file_path) are rewritten to match.
//
// Re-owning: the imported subtree is bound to the importing user and their team,
// not the original owner ids from the archive. enc:u1:<srcUser>: ciphertext labels
// are rewritten to enc:u1:<dstUser>: so the values decrypt once the importer
// unlocks (their DEK opens them when it is the same passkey-derived key — the
// common "move my account to a new deployment" case; in E2E the DEK is a fixed
// stand-in, so the round-trip is deterministic).

// TenantArchiveFormat / TenantArchiveVersion stamp the archive so future format
// changes can be detected and migrated on import.
const (
	TenantArchiveFormat  = "tenant-v1"
	TenantArchiveVersion = 1
)

// TenantManifest is the archive's self-describing header (entities/manifest is
// carried inside tenant.json; this is the top-level marker).
type TenantManifest struct {
	Format       string    `json:"format"`
	Version      int       `json:"version"`
	Timestamp    time.Time `json:"timestamp"`
	SourceUserID int32     `json:"source_user_id"`
}

// tenantData is the serialized payload: one row slice per table, scoped to the
// exported subtree.
type tenantData struct {
	Manifest TenantManifest   `json:"manifest"`
	Tables   map[string][]row `json:"tables"`
}

// tenantTables is the full ordered list of subtree tables. Order matters for
// import: a table is inserted only after every table it references by foreign
// key. web_authn_credentials is exported (it carries the passkey-wrapped DEKs,
// per the ticket) but is NOT re-inserted on import — the importer keeps their
// own credentials; see importTenant.
var tenantInsertOrder = []string{
	"companies",
	"llm_providers",
	"model_groups",
	"model_group_members",
	"default_model_settings",
	"sprints",
	"projects",
	"agents",
	"skills",
	"tasks",
	"runs",
	"comments",
	"attachments",
	"artifacts",
	"mcp_servers",
	"mcp_accounts",
	"agent_mcp_servers",
	"agent_mcp_accounts",
	"agent_mcp_tool_filters",
	"activity_logs",
}

// ---------------------------------------------------------------------------
// Export
// ---------------------------------------------------------------------------

// ExportTenant writes a tenant-scoped archive for userID to w. basePath is the
// runtime data root whose on-disk footprint (for the exported subtree) is
// included under files/.
func ExportTenant(ctx context.Context, w io.Writer, basePath string, database *gorm.DB, userID int32) error {
	data, footprint, err := collectTenant(ctx, database, userID)
	if err != nil {
		return err
	}

	gz := gzip.NewWriter(w)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()

	manifestBytes, _ := json.MarshalIndent(data.Manifest, "", "  ")
	writeTarEntry(tw, "tenant_manifest.json", manifestBytes)

	payload, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal tenant data: %w", err)
	}
	writeTarEntry(tw, "entities/tenant.json", payload)

	// On-disk footprint for the subtree, under files/, mirroring the runtime
	// layout with the ORIGINAL ids/short names (import remaps them).
	count := 0
	for _, dir := range footprint {
		abs := filepath.Join(basePath, dir)
		if err := addDirectoryToTar(tw, abs, filepath.Join("files", dir), &count); err != nil {
			log.Printf("Warning: export tenant: %s: %v", dir, err)
		}
	}
	return nil
}

// collectTenant reads the user's subtree into a tenantData payload and returns
// the list of on-disk directories (relative to basePath) that make up its
// footprint.
func collectTenant(ctx context.Context, database *gorm.DB, userID int32) (*tenantData, []string, error) {
	db := database.WithContext(ctx)
	tables := map[string][]row{}

	read := func(table, where string, args ...interface{}) []row {
		var rows []row
		q := db.Table(table)
		if where != "" {
			q = q.Where(where, args...)
		}
		if err := q.Find(&rows).Error; err != nil {
			log.Printf("Warning: export tenant: read %s: %v", table, err)
			return nil
		}
		sort.Slice(rows, func(i, j int) bool { return toID(rows[i]) < toID(rows[j]) })
		return rows
	}

	// Companies the user can work with are the root of the subtree.
	companies := read("companies",
		"team_id IN (SELECT team_id FROM team_members WHERE user_id = ?) OR (team_id IS NULL AND user_id = ?)",
		userID, userID)
	tables["companies"] = companies
	companyIDs := ids(companies)
	if len(companyIDs) == 0 {
		// Nothing to export, but providers/groups may still be the user's own.
		companyIDs = []int64{-1}
	}

	tables["projects"] = read("projects", "company_id IN ?", companyIDs)
	projectIDs := ids(tables["projects"])
	tables["sprints"] = read("sprints", "company_id IN ?", companyIDs)
	tables["agents"] = read("agents", "company_id IN ?", companyIDs)
	agentIDs := ids(tables["agents"])
	tables["skills"] = read("skills", "company_id IN ?", companyIDs)
	tables["tasks"] = read("tasks", "company_id IN ?", companyIDs)
	taskIDs := ids(tables["tasks"])

	tables["comments"] = read("comments", "task_id IN ?", orNone(taskIDs))
	tables["attachments"] = read("attachments", "task_id IN ?", orNone(taskIDs))
	tables["artifacts"] = read("artifacts", "task_id IN ?", orNone(taskIDs))
	tables["runs"] = read("runs", "task_id IN ?", orNone(taskIDs))

	// Owner-scoped global tables.
	tables["llm_providers"] = read("llm_providers", "user_id = ?", userID)
	tables["model_groups"] = read("model_groups", "user_id = ?", userID)
	groupIDs := ids(tables["model_groups"])
	tables["model_group_members"] = read("model_group_members", "group_id IN ?", orNone(groupIDs))
	tables["default_model_settings"] = read("default_model_settings", "user_id = ?", userID)

	// MCP: the user's own servers + project-scoped codegraph servers under the subtree.
	tables["mcp_servers"] = read("mcp_servers", "owner_user_id = ? OR project_id IN ?", userID, orNone(projectIDs))
	mcpServerIDs := ids(tables["mcp_servers"])
	tables["mcp_accounts"] = read("mcp_accounts", "user_id = ? OR mcp_server_id IN ?", userID, orNone(mcpServerIDs))

	tables["agent_mcp_servers"] = read("agent_mcp_servers", "agent_id IN ?", orNone(agentIDs))
	tables["agent_mcp_accounts"] = read("agent_mcp_accounts", "agent_id IN ?", orNone(agentIDs))
	tables["agent_mcp_tool_filters"] = read("agent_mcp_tool_filters", "agent_id IN ?", orNone(agentIDs))
	tables["activity_logs"] = read("activity_logs", "company_id IN ?", companyIDs)

	// Passkey-wrapped DEKs, carried so the account can unlock after a
	// same-passkey restore (not re-inserted on import — informational).
	tables["web_authn_credentials"] = read("web_authn_credentials", "user_id = ?", userID)

	data := &tenantData{
		Manifest: TenantManifest{
			Format:       TenantArchiveFormat,
			Version:      TenantArchiveVersion,
			Timestamp:    time.Now(),
			SourceUserID: userID,
		},
		Tables: tables,
	}

	// On-disk footprint: the company-scoped roots plus task-keyed uploads.
	footprint := []string{}
	seen := map[string]bool{}
	add := func(rel string) {
		if rel != "" && !seen[rel] {
			seen[rel] = true
			footprint = append(footprint, rel)
		}
	}
	for _, c := range companies {
		short, _ := c["short_name"].(string)
		if short == "" {
			continue
		}
		add(filepath.Join("repos", short))
		add(filepath.Join("workspace", short))
		add(filepath.Join("artifacts", short))
		add(filepath.Join("logs", short))
		add(filepath.Join("skills", short))
	}
	for _, tid := range taskIDs {
		add(filepath.Join("uploads", strconv.FormatInt(tid, 10)))
	}
	return data, footprint, nil
}

// ---------------------------------------------------------------------------
// Import
// ---------------------------------------------------------------------------

// TenantImportStats reports how many rows of each kind were imported.
type TenantImportStats struct {
	Companies int `json:"companies_restored"`
	Projects  int `json:"projects_restored"`
	Agents    int `json:"agents_restored"`
	Tasks     int `json:"tasks_restored"`
	Runs      int `json:"runs_restored"`
	Providers int `json:"providers_restored"`
	Sprints   int `json:"sprints_restored"`
	Skills    int `json:"skills_restored"`
	Artifacts int `json:"artifacts_restored"`
}

// ImportTenant reads a tenant-v1 archive and imports its subtree into database,
// re-owned to (targetUserID, targetTeamID), assigning fresh IDs and remapping
// every foreign key and on-disk path. The DB work runs in a single transaction;
// files are copied only after it commits.
func ImportTenant(ctx context.Context, archivePath, basePath string, database *gorm.DB, targetUserID, targetTeamID int32) (TenantImportStats, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return TenantImportStats{}, err
	}
	defer f.Close()
	return ImportTenantFromReader(ctx, f, basePath, database, targetUserID, targetTeamID)
}

// ImportTenantFromReader is ImportTenant reading the archive from r (e.g. an
// HTTP upload) instead of a file on disk.
func ImportTenantFromReader(ctx context.Context, r io.Reader, basePath string, database *gorm.DB, targetUserID, targetTeamID int32) (TenantImportStats, error) {
	tempDir, err := os.MkdirTemp("", "headcount1-tenant-import-*")
	if err != nil {
		return TenantImportStats{}, err
	}
	defer os.RemoveAll(tempDir)

	if err := extractArchiveReader(r, tempDir); err != nil {
		return TenantImportStats{}, fmt.Errorf("extract archive: %w", err)
	}

	payloadPath := filepath.Join(tempDir, "entities", "tenant.json")
	raw, err := os.ReadFile(payloadPath)
	if err != nil {
		return TenantImportStats{}, fmt.Errorf("archive has no entities/tenant.json (not a tenant-v1 backup?): %w", err)
	}
	var data tenantData
	if err := json.Unmarshal(raw, &data); err != nil {
		return TenantImportStats{}, fmt.Errorf("parse tenant.json: %w", err)
	}
	if data.Manifest.Format != TenantArchiveFormat {
		return TenantImportStats{}, fmt.Errorf("unsupported tenant archive format %q (want %q)", data.Manifest.Format, TenantArchiveFormat)
	}

	imp := &tenantImporter{
		basePath:    basePath,
		tempDir:     tempDir,
		srcUser:     data.Manifest.SourceUserID,
		dstUser:     targetUserID,
		dstTeam:     targetTeamID,
		data:        &data,
		idMap:       map[string]map[int64]int64{},
		insertedIDs: map[string]map[int64]bool{},
		taskWasNew:  map[int64]bool{},
	}

	var stats TenantImportStats
	err = database.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		s, err := imp.run(tx)
		stats = s
		return err
	})
	if err != nil {
		return TenantImportStats{}, err
	}

	// Files are copied after the DB transaction commits (file moves are not
	// transactional; a failure here is logged, never fatal — the DB is the
	// source of truth and its paths already point at the new locations).
	imp.restoreFiles()

	return stats, nil
}

type tenantImporter struct {
	basePath string
	tempDir  string
	srcUser  int32
	dstUser  int32
	dstTeam  int32
	data     *tenantData

	idMap map[string]map[int64]int64 // table -> oldID -> newID (existing OR freshly inserted)

	// insertedIDs[table][newID] is true only for rows this import actually
	// inserted (not the ones it reused via dedup). Path-column rewrites and file
	// copies key off this so a merge never rewrites or clobbers an entity that
	// already existed in the target.
	insertedIDs map[string]map[int64]bool

	// taskWasNew[newTaskID] reports whether that task row was freshly inserted
	// (true) or reused via dedup (false). Comments — which have no stable domain
	// key of their own — are imported only under freshly-inserted tasks, so a
	// re-import doesn't duplicate them.
	taskWasNew map[int64]bool

	// Resolved during import, used for path rewriting.
	companyShort map[int64]shortNames // old companyID -> {old, new} short name
	taskCompany  map[int64]int64      // old taskID -> old companyID
	taskRoot     map[int64]int64      // old taskID -> old root taskID
	runRoot      map[int64]int64      // old runID -> old root runID
	runTask      map[int64]int64      // old runID -> old taskID

	// Snapshots of path-bearing rows, captured BEFORE insertion mutates the row
	// maps (insert deletes the old id, remaps FKs, and backfills the new id), so
	// path rewriting can still resolve each row's original id / task / paths.
	attSnaps   []pathSnap
	artSnaps   []pathSnap
	runSnaps   []runSnap
	skillSnaps []skillSnap
}

// skillSnap is a skill's pre-import identity. Its local_path is an absolute
// on-disk path (skills/{short}/{name}) that must be recomputed on import.
type skillSnap struct {
	oldID      int64
	oldCompany int64
	name       string
	oldPath    string
}

type shortNames struct{ old, new string }

// pathSnap is an attachment or artifact's pre-import identity.
type pathSnap struct {
	oldID   int64
	oldTask int64
	oldPath string
}

// runSnap is a run's pre-import identity, including its root run (whose id names
// the on-disk log directory).
type runSnap struct {
	oldID      int64
	oldTask    int64
	oldRootRun int64
	oldPath    string
}

func (imp *tenantImporter) run(tx *gorm.DB) (TenantImportStats, error) {
	imp.buildGraphs()

	var stats TenantImportStats
	for _, table := range tenantInsertOrder {
		rows := imp.data.Tables[table]
		sortRowsByID(rows)
		imp.idMap[table] = map[int64]int64{}
		imp.insertedIDs[table] = map[int64]bool{}
		insertedCount := 0
		for _, r := range rows {
			oldID := toID(r)
			newID, mapped, inserted, err := imp.resolveOrInsertRow(tx, table, r, oldID)
			if err != nil {
				return stats, fmt.Errorf("import %s (old id %d): %w", table, oldID, err)
			}
			if mapped && oldID != 0 {
				imp.idMap[table][oldID] = newID
			}
			if inserted {
				insertedCount++
				if newID != 0 {
					imp.insertedIDs[table][newID] = true
				}
			}
		}
		// Stats report only freshly-imported rows, not the ones deduped onto
		// entities the importer already had.
		imp.tally(&stats, table, insertedCount)
	}

	// Path columns depend on the newly-assigned ids/short names; rewrite them
	// now that every id map is complete.
	if err := imp.rewritePathColumns(tx); err != nil {
		return stats, err
	}
	return stats, nil
}

// buildGraphs precomputes the old-id graphs (task parent chains, run roots,
// company short names) used for path resolution.
func (imp *tenantImporter) buildGraphs() {
	imp.companyShort = map[int64]shortNames{}
	for _, c := range imp.data.Tables["companies"] {
		short, _ := c["short_name"].(string)
		imp.companyShort[toID(c)] = shortNames{old: short, new: short}
	}

	imp.taskCompany = map[int64]int64{}
	parent := map[int64]int64{}
	for _, t := range imp.data.Tables["tasks"] {
		tid := toID(t)
		imp.taskCompany[tid] = asInt64(t["company_id"])
		if p := asInt64(t["parent_id"]); p != 0 {
			parent[tid] = p
		}
	}
	imp.taskRoot = map[int64]int64{}
	for _, t := range imp.data.Tables["tasks"] {
		tid := toID(t)
		root := tid
		for {
			p, ok := parent[root]
			if !ok || p == 0 || p == root {
				break
			}
			root = p
		}
		imp.taskRoot[tid] = root
	}

	imp.runRoot = map[int64]int64{}
	imp.runTask = map[int64]int64{}
	for _, rn := range imp.data.Tables["runs"] {
		rid := toID(rn)
		imp.runTask[rid] = asInt64(rn["task_id"])
		root := asInt64(rn["root_run_id"])
		if root == 0 {
			root = rid
		}
		imp.runRoot[rid] = root
		imp.runSnaps = append(imp.runSnaps, runSnap{
			oldID: rid, oldTask: asInt64(rn["task_id"]), oldRootRun: root,
			oldPath: strVal(rn["log_file_path"]),
		})
	}

	for _, a := range imp.data.Tables["attachments"] {
		imp.attSnaps = append(imp.attSnaps, pathSnap{
			oldID: toID(a), oldTask: asInt64(a["task_id"]), oldPath: strVal(a["file_path"]),
		})
	}
	for _, a := range imp.data.Tables["artifacts"] {
		imp.artSnaps = append(imp.artSnaps, pathSnap{
			oldID: toID(a), oldTask: asInt64(a["task_id"]), oldPath: strVal(a["file_path"]),
		})
	}
	for _, s := range imp.data.Tables["skills"] {
		imp.skillSnaps = append(imp.skillSnaps, skillSnap{
			oldID: toID(s), oldCompany: asInt64(s["company_id"]),
			name: strVal(s["name"]), oldPath: strVal(s["local_path"]),
		})
	}
}

// resolveOrInsertRow rewrites a row (FKs, owner, secret labels) and then either
// reuses an existing row that shares its domain key (dedup / merge) or inserts a
// fresh one. Returns:
//   - id:       the resolved primary key (existing or new); 0 for join tables
//   - mapped:   whether id should be recorded in the old→new id map
//   - inserted: whether a NEW row was actually inserted (vs reused/skipped)
func (imp *tenantImporter) resolveOrInsertRow(tx *gorm.DB, table string, r row, oldID int64) (id int64, mapped, inserted bool, err error) {
	// Drop the archive's primary key so the DB assigns a fresh one. Original
	// created_at/updated_at are kept verbatim to preserve history.
	delete(r, "id")

	imp.remapFKs(table, r)
	imp.reowner(table, r)
	imp.relabelSecrets(r)

	// Comments have no stable domain key of their own. Import them only under a
	// freshly-inserted task; on a task the importer already had, skip them so a
	// re-import never duplicates a discussion.
	if table == "comments" {
		if newTask := asInt64(r["task_id"]); newTask != 0 && !imp.taskWasNew[newTask] {
			return 0, false, false, nil
		}
	}

	// Dedup: if an entity with the same domain key already exists in the
	// importer's scope, reuse it (merge) instead of creating a duplicate.
	if existing, found := imp.findExisting(tx, table, r); found {
		if table == "companies" {
			imp.recordCompanyShort(oldID, strVal(r["short_name"]))
		}
		if table == "tasks" {
			imp.taskWasNew[existing] = false
		}
		return existing, existing != 0, false, nil
	}

	// No match → a brand-new row. For globally-named entities, make the name
	// unique so the new row never shares a directory/slug with another tenant.
	switch table {
	case "companies":
		imp.ensureUniqueCompanyShort(tx, r)
		imp.recordCompanyShort(oldID, strVal(r["short_name"]))
	case "model_groups":
		imp.ensureUnique(tx, table, "slug", r)
	case "mcp_servers":
		imp.ensureUnique(tx, table, "name", r)
	}

	// Join tables (composite primary key, no auto-increment id) insert without a
	// RETURNING clause and contribute no id mapping.
	if joinTables[table] {
		if err := tx.Table(table).Create(r).Error; err != nil {
			return 0, false, false, err
		}
		return 0, false, true, nil
	}

	if err := tx.Table(table).
		Clauses(clause.Returning{Columns: []clause.Column{{Name: "id"}}}).
		Create(r).Error; err != nil {
		return 0, false, false, err
	}
	newID := asInt64(r["id"])
	if table == "tasks" {
		imp.taskWasNew[newID] = true
	}
	return newID, newID != 0, true, nil
}

// findExisting looks up a row already present in the target DB that shares this
// row's domain key, scoped to the importing user/team. The row's foreign keys
// are already remapped, so scoping columns (company_id, task_id, …) hold the
// target-side ids. Returns the existing primary key and true on a hit.
//
// Identity is the domain-level slug/key, never the archive's DB id: companies by
// short_name, tasks by ref_key, runs/agents/skills/… by name within their
// parent, providers by their derived slug, model groups / MCP servers by their
// unique slug/name. Rows without a stable key (comments) never match here.
func (imp *tenantImporter) findExisting(tx *gorm.DB, table string, r row) (int64, bool) {
	switch table {
	case "companies":
		return imp.existingID(tx, "companies",
			"short_name = ? AND (team_id = ? OR user_id = ?)",
			strVal(r["short_name"]), imp.dstTeam, imp.dstUser)
	case "projects", "sprints", "agents", "skills":
		return imp.byParentName(tx, table, "company_id", r["company_id"], strVal(r["name"]))
	case "tasks":
		refKey := strVal(r["ref_key"])
		if refKey == "" {
			return 0, false
		}
		return imp.existingID(tx, "tasks", "company_id = ? AND ref_key = ?", r["company_id"], refKey)
	case "runs":
		name := strVal(r["name"])
		if name == "" {
			return 0, false
		}
		return imp.existingID(tx, "runs", "task_id = ? AND name = ?", r["task_id"], name)
	case "artifacts":
		return imp.existingID(tx, "artifacts", "task_id = ? AND filename = ?", r["task_id"], strVal(r["filename"]))
	case "attachments":
		return imp.existingID(tx, "attachments", "task_id = ? AND filename = ?", r["task_id"], strVal(r["filename"]))
	case "llm_providers":
		slug := strVal(r["slug"])
		if slug == "" {
			slug = db.ProviderSlug(providerFromRow(r)) // older archives lack the slug column
		}
		return imp.existingID(tx, "llm_providers", "slug = ? AND user_id = ?", slug, imp.dstUser)
	case "model_groups":
		return imp.existingID(tx, "model_groups", "slug = ? AND user_id = ?", strVal(r["slug"]), imp.dstUser)
	case "model_group_members":
		return imp.existingID(tx, "model_group_members",
			"group_id = ? AND provider_id = ? AND model = ?", r["group_id"], r["provider_id"], strVal(r["model"]))
	case "default_model_settings":
		return imp.existingID(tx, "default_model_settings", "purpose = ? AND user_id = ?", strVal(r["purpose"]), imp.dstUser)
	case "mcp_servers":
		if owner := asInt64(r["owner_user_id"]); owner != 0 {
			return imp.existingID(tx, "mcp_servers", "name = ? AND owner_user_id = ?", strVal(r["name"]), imp.dstUser)
		}
		// Codegraph servers (no owner) are scoped by their project.
		return imp.existingID(tx, "mcp_servers", "name = ? AND project_id = ?", strVal(r["name"]), r["project_id"])
	case "mcp_accounts":
		return imp.existingID(tx, "mcp_accounts", "mcp_server_id = ? AND name = ?", r["mcp_server_id"], strVal(r["name"]))
	case "agent_mcp_servers":
		return imp.existingID(tx, "agent_mcp_servers", "agent_id = ? AND mcp_server_id = ?", r["agent_id"], r["mcp_server_id"])
	case "agent_mcp_accounts":
		return imp.existingID(tx, "agent_mcp_accounts", "agent_id = ? AND mcp_account_id = ?", r["agent_id"], r["mcp_account_id"])
	case "agent_mcp_tool_filters":
		return imp.existingID(tx, "agent_mcp_tool_filters",
			"agent_id = ? AND mcp_server_id = ? AND tool_name = ?", r["agent_id"], r["mcp_server_id"], strVal(r["tool_name"]))
	}
	return 0, false
}

// byParentName finds a row by (parentCol, name) — the dedup key for entities
// that are unique-by-name within their company (projects, sprints, agents,
// skills). A missing parent id or name never matches.
func (imp *tenantImporter) byParentName(tx *gorm.DB, table, parentCol string, parentVal interface{}, name string) (int64, bool) {
	if asInt64(parentVal) == 0 || name == "" {
		return 0, false
	}
	return imp.existingID(tx, table, parentCol+" = ? AND name = ?", parentVal, name)
}

// existingID returns the id of the first row matching where, and whether one was
// found. A join table (no id column) returns a non-zero sentinel on a match so
// callers can still detect "already present" and skip the insert.
func (imp *tenantImporter) existingID(tx *gorm.DB, table, where string, args ...interface{}) (int64, bool) {
	if joinTables[table] {
		return -1, imp.rowExists(tx, table, where, args...)
	}
	var id int64
	if err := tx.Table(table).Where(where, args...).Select("id").Limit(1).Scan(&id).Error; err != nil {
		return 0, false
	}
	return id, id != 0
}

// recordCompanyShort stores the final on-disk short name chosen for a company
// (unchanged on a dedup reuse, possibly suffixed on a fresh insert) so path
// rewriting and file copying target the right directory.
func (imp *tenantImporter) recordCompanyShort(oldID int64, finalShort string) {
	sn := imp.companyShort[oldID]
	sn.new = finalShort
	imp.companyShort[oldID] = sn
}

// providerFromRow builds a minimal LLMProvider from an archive row so
// db.ProviderSlug can derive a slug for archives predating the slug column.
func providerFromRow(r row) db.LLMProvider {
	return db.LLMProvider{
		Name:         strVal(r["name"]),
		BaseUrl:      strVal(r["base_url"]),
		PresetKey:    strVal(r["preset_key"]),
		ProviderName: strVal(r["provider_name"]),
	}
}

// joinTables have a composite primary key and no auto-increment id column.
var joinTables = map[string]bool{
	"agent_mcp_servers":      true,
	"agent_mcp_accounts":     true,
	"agent_mcp_tool_filters": true,
}

// remapFKs rewrites every foreign-key column of a row through the id maps built
// so far. Referenced tables are always inserted before their referrers (see
// tenantInsertOrder), so the maps are populated by the time we need them.
func (imp *tenantImporter) remapFKs(table string, r row) {
	remap := func(col, refTable string) {
		old := asInt64(r[col])
		if old == 0 {
			return
		}
		if m, ok := imp.idMap[refTable]; ok {
			if nw, ok := m[old]; ok {
				r[col] = nw
				return
			}
		}
		// Reference outside the exported subtree — drop it rather than dangle.
		r[col] = nil
	}

	switch table {
	case "projects", "sprints", "skills":
		remap("company_id", "companies")
	case "agents":
		remap("company_id", "companies")
		remap("provider_id", "llm_providers")
		remap("model_group_id", "model_groups")
	case "tasks":
		remap("company_id", "companies")
		remap("project_id", "projects")
		remap("sprint_id", "sprints")
		remap("agent_id", "agents")
		remap("parent_id", "tasks")
		// run_id points at a run inserted later; clear it (patched to nil, the
		// task still functions and its runs list is intact).
		r["run_id"] = nil
	case "runs":
		remap("task_id", "tasks")
		remap("agent_id", "agents")
		remap("parent_run_id", "runs")
		remap("root_run_id", "runs")
	case "comments":
		remap("task_id", "tasks")
		remap("run_id", "runs")
	case "attachments":
		remap("task_id", "tasks")
		remap("comment_id", "comments")
	case "artifacts":
		remap("company_id", "companies")
		remap("project_id", "projects")
		remap("task_id", "tasks")
		remap("run_id", "runs")
	case "model_group_members":
		remap("group_id", "model_groups")
		remap("provider_id", "llm_providers")
	case "default_model_settings":
		remap("provider_id", "llm_providers")
		remap("model_group_id", "model_groups")
	case "mcp_servers":
		remap("project_id", "projects")
	case "mcp_accounts":
		remap("mcp_server_id", "mcp_servers")
	case "agent_mcp_servers":
		remap("agent_id", "agents")
		remap("mcp_server_id", "mcp_servers")
	case "agent_mcp_accounts":
		remap("agent_id", "agents")
		remap("mcp_account_id", "mcp_accounts")
	case "agent_mcp_tool_filters":
		remap("agent_id", "agents")
		remap("mcp_server_id", "mcp_servers")
	case "activity_logs":
		remap("company_id", "companies")
	}
}

// reowner binds ownership columns to the importing user/team instead of the
// archive's original owner ids, while preserving the internal graph.
func (imp *tenantImporter) reowner(table string, r row) {
	setUser := func() { r["user_id"] = imp.dstUser }
	switch table {
	case "companies":
		r["team_id"] = imp.dstTeam
		r["user_id"] = imp.dstUser
	case "llm_providers", "model_groups", "default_model_settings", "mcp_accounts":
		setUser()
	case "mcp_servers":
		// Only re-owner user-owned servers; codegraph rows scope via project_id
		// and keep a NULL owner.
		if asInt64(r["owner_user_id"]) != 0 {
			r["owner_user_id"] = imp.dstUser
		}
	case "comments":
		// A human comment's author_id referenced the source user; rebind it.
		if at, _ := r["author_type"].(string); at == "human" || at == "user" {
			if asInt64(r["author_id"]) != 0 {
				r["author_id"] = imp.dstUser
			}
		}
	}
}

// relabelSecrets rewrites enc:u1:<srcUser>: ciphertext labels to the importer's
// id across every string field, so the values decrypt under the importer's DEK.
func (imp *tenantImporter) relabelSecrets(r row) {
	if imp.srcUser == imp.dstUser {
		return
	}
	oldPrefix := fmt.Sprintf("enc:u1:%d:", imp.srcUser)
	newPrefix := fmt.Sprintf("enc:u1:%d:", imp.dstUser)
	for k, v := range r {
		if s, ok := v.(string); ok && strings.HasPrefix(s, oldPrefix) {
			r[k] = newPrefix + strings.TrimPrefix(s, oldPrefix)
		}
	}
}

// ---------------------------------------------------------------------------
// Uniqueness helpers
// ---------------------------------------------------------------------------

func (imp *tenantImporter) rowExists(tx *gorm.DB, table, where string, args ...interface{}) bool {
	var n int64
	tx.Table(table).Where(where, args...).Count(&n)
	return n > 0
}

// ensureUnique makes r[col] unique in table by appending "-2", "-3", … on
// collision (global-unique columns: model_groups.slug, mcp_servers.name).
func (imp *tenantImporter) ensureUnique(tx *gorm.DB, table, col string, r row) {
	base, _ := r[col].(string)
	if base == "" {
		return
	}
	candidate := base
	for n := 2; imp.rowExists(tx, table, col+" = ?", candidate); n++ {
		candidate = fmt.Sprintf("%s-%d", base, n)
	}
	r[col] = candidate
}

// ensureUniqueCompanyShort keeps a freshly-inserted company's short name unique
// in the target DB so its on-disk directories (artifacts/{short}, logs/{short},
// …) never collide with another tenant's. Only called on a dedup MISS; a reused
// company keeps its (matched) short name.
func (imp *tenantImporter) ensureUniqueCompanyShort(tx *gorm.DB, r row) {
	base, _ := r["short_name"].(string)
	if base == "" {
		return
	}
	candidate := base
	for n := 2; imp.rowExists(tx, "companies", "short_name = ?", candidate); n++ {
		candidate = fmt.Sprintf("%s-%d", base, n)
	}
	r["short_name"] = candidate
}

// ---------------------------------------------------------------------------
// Path rewriting
// ---------------------------------------------------------------------------

// rewritePathColumns updates the absolute *_path columns of restored rows to
// point at their remapped on-disk locations. Only freshly-inserted rows are
// touched; a row reused via dedup keeps its existing (already-correct) path.
func (imp *tenantImporter) rewritePathColumns(tx *gorm.DB) error {
	// attachments.file_path -> uploads/{newTaskID}/<basename>
	for _, s := range imp.attSnaps {
		newAttID, ok := imp.idMap["attachments"][s.oldID]
		if !ok || s.oldPath == "" || !imp.insertedIDs["attachments"][newAttID] {
			continue
		}
		newTask, ok := imp.idMap["tasks"][s.oldTask]
		if !ok {
			continue
		}
		np := filepath.Join(imp.paths().TaskUploadsDir(int32(newTask)), filepath.Base(s.oldPath))
		tx.Table("attachments").Where("id = ?", newAttID).Update("file_path", np)
	}

	// artifacts.file_path -> artifacts/{newShort}/{newRootTaskID}/<basename>
	for _, s := range imp.artSnaps {
		newArtID, ok := imp.idMap["artifacts"][s.oldID]
		if !ok || s.oldPath == "" || !imp.insertedIDs["artifacts"][newArtID] {
			continue
		}
		newShort, newRoot, ok := imp.taskPathTarget(s.oldTask)
		if !ok {
			continue
		}
		np := filepath.Join(imp.paths().TaskArtifactsDir(newShort, newRoot), filepath.Base(s.oldPath))
		tx.Table("artifacts").Where("id = ?", newArtID).Update("file_path", np)
	}

	// runs.log_file_path -> logs/{newShort}/{newRootTaskID}/run-{newRootRunID}/<basename>
	for _, s := range imp.runSnaps {
		newRunID, ok := imp.idMap["runs"][s.oldID]
		if !ok || s.oldPath == "" || !imp.insertedIDs["runs"][newRunID] {
			continue
		}
		newShort, newRoot, ok := imp.taskPathTarget(s.oldTask)
		if !ok {
			continue
		}
		newRootRun, ok := imp.idMap["runs"][s.oldRootRun]
		if !ok {
			newRootRun = newRunID
		}
		np := filepath.Join(imp.paths().RunLogsDir(newShort, newRoot, int32(newRootRun)), filepath.Base(s.oldPath))
		tx.Table("runs").Where("id = ?", newRunID).Update("log_file_path", np)
	}

	// skills.local_path -> skills/{newShort}/{name}
	for _, s := range imp.skillSnaps {
		newSkillID, ok := imp.idMap["skills"][s.oldID]
		if !ok || s.oldPath == "" || !imp.insertedIDs["skills"][newSkillID] {
			continue
		}
		sn, ok := imp.companyShort[s.oldCompany]
		if !ok {
			continue
		}
		np := imp.paths().SkillDir(sn.new, s.name)
		tx.Table("skills").Where("id = ?", newSkillID).Update("local_path", np)
	}
	return nil
}

// taskPathTarget returns the new company short name and new root-task id used to
// build the on-disk artifacts/logs directory for a task.
func (imp *tenantImporter) taskPathTarget(oldTask int64) (string, int32, bool) {
	oldRoot, ok := imp.taskRoot[oldTask]
	if !ok {
		oldRoot = oldTask
	}
	newRoot, ok := imp.idMap["tasks"][oldRoot]
	if !ok {
		return "", 0, false
	}
	oldCompany := imp.taskCompany[oldTask]
	sn, ok := imp.companyShort[oldCompany]
	if !ok {
		return "", 0, false
	}
	return sn.new, int32(newRoot), true
}

func (imp *tenantImporter) paths() filesystem.Paths {
	return filesystem.NewPaths(imp.basePath)
}

// ---------------------------------------------------------------------------
// File copying
// ---------------------------------------------------------------------------

// restoreFiles copies the archived subtree files from the extracted files/ tree
// (old layout) to their remapped locations under basePath.
func (imp *tenantImporter) restoreFiles() {
	src := filepath.Join(imp.tempDir, "files")
	p := imp.paths()

	// uploads/{oldTaskID} -> uploads/{newTaskID}
	for oldTask, newTask := range imp.idMap["tasks"] {
		from := filepath.Join(src, "uploads", strconv.FormatInt(oldTask, 10))
		if dirExists(from) {
			imp.copyInto(from, p.TaskUploadsDir(int32(newTask)))
		}
	}

	// artifacts & logs are keyed by (company short, root task id).
	rootTasks := map[int64]bool{}
	for _, root := range imp.taskRoot {
		rootTasks[root] = true
	}
	for oldRoot := range rootTasks {
		sn, ok := imp.companyShort[imp.taskCompany[oldRoot]]
		if !ok {
			continue
		}
		newRoot, ok := imp.idMap["tasks"][oldRoot]
		if !ok {
			continue
		}
		// artifacts/{oldShort}/{oldRoot} -> artifacts/{newShort}/{newRoot}
		aFrom := filepath.Join(src, "artifacts", sn.old, strconv.FormatInt(oldRoot, 10))
		if dirExists(aFrom) {
			imp.copyInto(aFrom, p.TaskArtifactsDir(sn.new, int32(newRoot)))
		}
		// logs/{oldShort}/{oldRoot} -> logs/{newShort}/{newRoot}, renaming
		// run-{oldRootRunID} subdirs to their remapped ids.
		lFrom := filepath.Join(src, "logs", sn.old, strconv.FormatInt(oldRoot, 10))
		if dirExists(lFrom) {
			imp.copyLogsDir(lFrom, p.TaskLogsDir(sn.new, int32(newRoot)))
		}
	}

	// Company-level trees whose names change with the short name: repos, skills.
	for _, sn := range imp.uniqueShortNames() {
		for _, root := range []string{"repos", "skills"} {
			from := filepath.Join(src, root, sn.old)
			if dirExists(from) {
				imp.copyInto(from, filepath.Join(imp.basePath, root, sn.new))
			}
		}
	}

	// workspace/{oldShort}/task-{oldTaskID} -> workspace/{newShort}/task-{newTaskID}
	for oldTask, newTask := range imp.idMap["tasks"] {
		sn, ok := imp.companyShort[imp.taskCompany[oldTask]]
		if !ok {
			continue
		}
		from := filepath.Join(src, "workspace", sn.old, fmt.Sprintf("task-%d", oldTask))
		if dirExists(from) {
			imp.copyInto(from, filepath.Join(p.CompanyWorkspaceDir(sn.new), fmt.Sprintf("task-%d", newTask)))
		}
	}

	repairWorktrees(imp.basePath)
}

// copyLogsDir copies a task's logs directory, renaming each run-{oldRootRunID}
// subdirectory to run-{newRootRunID}.
func (imp *tenantImporter) copyLogsDir(from, to string) {
	entries, err := os.ReadDir(from)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() && strings.HasPrefix(name, "run-") {
			oldRun, err := strconv.ParseInt(strings.TrimPrefix(name, "run-"), 10, 64)
			if err == nil {
				if newRun, ok := imp.idMap["runs"][oldRun]; ok {
					imp.copyInto(filepath.Join(from, name), filepath.Join(to, fmt.Sprintf("run-%d", newRun)))
					continue
				}
			}
		}
		// Non-run entries (or unmapped runs) copy verbatim.
		imp.copyInto(filepath.Join(from, name), filepath.Join(to, name))
	}
}

// copyInto copies from → to WITHOUT overwriting files that already exist at the
// destination. Merge semantics: new files (e.g. a new run's logs under a reused
// task's directory) are added, while anything already present — including files
// the user may have changed since the export — is left untouched.
func (imp *tenantImporter) copyInto(from, to string) {
	if err := copyDirNoClobber(from, to); err != nil {
		log.Printf("Warning: import tenant: copy %s -> %s: %v", from, to, err)
	}
}

// copyDirNoClobber is copyDir but skips any destination file that already
// exists, so an import merges into existing directories instead of overwriting.
func copyDirNoClobber(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		dstPath := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(dstPath, 0755)
		}
		if _, err := os.Stat(dstPath); err == nil {
			return nil // already present — do not clobber
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dstPath), 0755); err != nil {
			return err
		}
		return os.WriteFile(dstPath, data, info.Mode().Perm())
	})
}

func (imp *tenantImporter) uniqueShortNames() []shortNames {
	seen := map[string]bool{}
	var out []shortNames
	for _, sn := range imp.companyShort {
		if !seen[sn.old] {
			seen[sn.old] = true
			out = append(out, sn)
		}
	}
	return out
}

func (imp *tenantImporter) tally(stats *TenantImportStats, table string, n int) {
	switch table {
	case "companies":
		stats.Companies = n
	case "projects":
		stats.Projects = n
	case "agents":
		stats.Agents = n
	case "tasks":
		stats.Tasks = n
	case "runs":
		stats.Runs = n
	case "llm_providers":
		stats.Providers = n
	case "sprints":
		stats.Sprints = n
	case "skills":
		stats.Skills = n
	case "artifacts":
		stats.Artifacts = n
	}
}

// ---------------------------------------------------------------------------
// small helpers
// ---------------------------------------------------------------------------

// ids returns the numeric ids of a row slice.
func ids(rows []row) []int64 {
	out := make([]int64, 0, len(rows))
	for _, r := range rows {
		if id := toID(r); id != 0 {
			out = append(out, id)
		}
	}
	return out
}

// orNone returns ids, or a slice that matches nothing when ids is empty (so an
// "IN ?" clause with no values is still valid SQL).
func orNone(ids []int64) []int64 {
	if len(ids) == 0 {
		return []int64{-1}
	}
	return ids
}

func strVal(v interface{}) string {
	s, _ := v.(string)
	return s
}

func dirExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

// extractArchiveReader extracts a gzip'd tar from r into destDir (same traversal
// guards as extractArchive, but streaming from a reader).
func extractArchiveReader(r io.Reader, destDir string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		target := filepath.Join(destDir, header.Name)
		if !strings.HasPrefix(target, filepath.Clean(destDir)+string(os.PathSeparator)) {
			return fmt.Errorf("archive entry escapes destination: %s", header.Name)
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(header.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			f.Close()
		}
	}
	return nil
}
