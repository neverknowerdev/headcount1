package skills

import (
	"gorm.io/gorm"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"agent-orchestrator/db"
)

type Manager struct {
	q *db.Queries
}

func NewManager(database *gorm.DB) *Manager {
	return &Manager{
		q: db.New(database),
	}
}

func (m *Manager) ImportSkill(ctx context.Context, companyID int32, name, sourceURL string) (*db.Skill, error) {
	skillsDir := filepath.Join("data", "skills", fmt.Sprintf("company_%d", companyID))
	if err := os.MkdirAll(skillsDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create skills directory: %w", err)
	}

	localPath := filepath.Join(skillsDir, name)

	if strings.HasSuffix(sourceURL, ".git") {
		cmd := exec.CommandContext(ctx, "git", "clone", sourceURL, localPath)
		if output, err := cmd.CombinedOutput(); err != nil {
			return nil, fmt.Errorf("git clone failed: %v, output: %s", err, string(output))
		}
	} else if strings.HasPrefix(sourceURL, "http") {
		if err := downloadFile(sourceURL, localPath); err != nil {
			return nil, err
		}
	} else {
		return nil, fmt.Errorf("unsupported source URL format")
	}

	skill, err := m.q.CreateSkill(ctx, db.Skill{
		CompanyID: companyID,
		Name:      name,
		SourceUrl: sourceURL,
		LocalPath: localPath,
	})
	if err != nil {
		return nil, err
	}

	return &skill, nil
}

func downloadFile(url, filepath string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("bad status: %s", resp.Status)
	}

	out, err := os.Create(filepath)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	return err
}

func (m *Manager) EnsureSkillExists(localPath string) bool {
	_, err := os.Stat(localPath)
	return !os.IsNotExist(err)
}
