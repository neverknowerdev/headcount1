package filesystem

import (
	"fmt"
	"path/filepath"
)

// Paths is the single source of truth for the on-disk layout under BasePath.
// Every package that touches a file under BasePath must resolve its location
// through these helpers instead of joining path segments inline.
//
// Runtime layout:
//
//	{base}/
//	  settings.yaml
//	  db/paperclip.db                        shared SQLite (WAL)
//	  repos/{company}/{project}/             project git clones
//	  workspace/{company}/task-{id}/         task git worktrees
//	  uploads/{taskID}/...                   comment attachments
//	  artifacts/{company}/{rootTaskID}/...   agent deliverables
//	  logs/{company}/{rootTaskID}/run-{id}/  run logs (JSONL)
//	  skills/{company}/{skill}/
//	  ssh/id_rsa
//	  credentials/
//	  venv/
//	  backups/
//	  archive/
type Paths struct {
	Base string
}

func NewPaths(base string) Paths { return Paths{Base: base} }

func (p Paths) SettingsFile() string { return filepath.Join(p.Base, "settings.yaml") }

func (p Paths) DBDir() string { return filepath.Join(p.Base, "db") }
func (p Paths) DBFile(fileName string) string {
	return filepath.Join(p.DBDir(), fileName)
}

func (p Paths) SSHDir() string     { return filepath.Join(p.Base, "ssh") }
func (p Paths) SSHKeyFile() string { return filepath.Join(p.SSHDir(), "id_rsa") }

func (p Paths) CredentialsDir() string { return filepath.Join(p.Base, "credentials") }

func (p Paths) UploadsDir() string { return filepath.Join(p.Base, "uploads") }
func (p Paths) TaskUploadsDir(taskID int32) string {
	return filepath.Join(p.UploadsDir(), fmt.Sprintf("%d", taskID))
}

func (p Paths) BackupsDir() string { return filepath.Join(p.Base, "backups") }
func (p Paths) ArchiveDir() string { return filepath.Join(p.Base, "archive") }
func (p Paths) VenvDir() string    { return filepath.Join(p.Base, "venv") }

func (p Paths) ReposDir() string { return filepath.Join(p.Base, "repos") }
func (p Paths) CompanyReposDir(companyShortName string) string {
	return filepath.Join(p.ReposDir(), companyShortName)
}
func (p Paths) RepoDir(companyShortName, projectName string) string {
	return filepath.Join(p.CompanyReposDir(companyShortName), projectName)
}

func (p Paths) WorkspaceDir() string { return filepath.Join(p.Base, "workspace") }
func (p Paths) CompanyWorkspaceDir(companyShortName string) string {
	return filepath.Join(p.WorkspaceDir(), companyShortName)
}
func (p Paths) WorktreeDir(companyShortName string, taskID int32) string {
	return filepath.Join(p.CompanyWorkspaceDir(companyShortName), fmt.Sprintf("task-%d", taskID))
}

func (p Paths) ArtifactsDir() string { return filepath.Join(p.Base, "artifacts") }
func (p Paths) CompanyArtifactsDir(companyShortName string) string {
	return filepath.Join(p.ArtifactsDir(), companyShortName)
}
func (p Paths) TaskArtifactsDir(companyShortName string, rootTaskID int32) string {
	return filepath.Join(p.CompanyArtifactsDir(companyShortName), fmt.Sprintf("%d", rootTaskID))
}

func (p Paths) LogsDir() string { return filepath.Join(p.Base, "logs") }
func (p Paths) CompanyLogsDir(companyShortName string) string {
	return filepath.Join(p.LogsDir(), companyShortName)
}
func (p Paths) TaskLogsDir(companyShortName string, rootTaskID int32) string {
	return filepath.Join(p.CompanyLogsDir(companyShortName), fmt.Sprintf("%d", rootTaskID))
}
func (p Paths) RunLogsDir(companyShortName string, rootTaskID, rootRunID int32) string {
	return filepath.Join(p.TaskLogsDir(companyShortName, rootTaskID), fmt.Sprintf("run-%d", rootRunID))
}

func (p Paths) SkillsDir() string { return filepath.Join(p.Base, "skills") }
func (p Paths) CompanySkillsDir(companyShortName string) string {
	return filepath.Join(p.SkillsDir(), companyShortName)
}
func (p Paths) SkillDir(companyShortName, skillName string) string {
	return filepath.Join(p.CompanySkillsDir(companyShortName), skillName)
}

// CompanyDirs lists every company-scoped directory root. Used by company
// archive/delete to cover the whole footprint of a company on disk.
func (p Paths) CompanyDirs(companyShortName string) []string {
	return []string{
		p.CompanyReposDir(companyShortName),
		p.CompanyWorkspaceDir(companyShortName),
		p.CompanyArtifactsDir(companyShortName),
		p.CompanyLogsDir(companyShortName),
		p.CompanySkillsDir(companyShortName),
	}
}
