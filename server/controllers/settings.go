package endpoints

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"

	"agent-orchestrator/db"
	"gopkg.in/yaml.v3"
)

type RoleModelConfig struct {
	SmartPlannerProviderID      int32  `json:"smart_planner_provider_id" yaml:"smart_planner_provider_id"`
	SmartPlannerModel           string `json:"smart_planner_model" yaml:"smart_planner_model"`
	TechResearcherProviderID    int32  `json:"tech_researcher_provider_id" yaml:"tech_researcher_provider_id"`
	TechResearcherModel         string `json:"tech_researcher_model" yaml:"tech_researcher_model"`
	WritingResearcherProviderID int32  `json:"writing_researcher_provider_id" yaml:"writing_researcher_provider_id"`
	WritingResearcherModel      string `json:"writing_researcher_model" yaml:"writing_researcher_model"`
	DesignResearcherProviderID  int32  `json:"design_researcher_provider_id" yaml:"design_researcher_provider_id"`
	DesignResearcherModel       string `json:"design_researcher_model" yaml:"design_researcher_model"`
	CoderProviderID             int32  `json:"coder_provider_id" yaml:"coder_provider_id"`
	CoderModel                  string `json:"coder_model" yaml:"coder_model"`
	TesterProviderID            int32  `json:"tester_provider_id" yaml:"tester_provider_id"`
	TesterModel                 string `json:"tester_model" yaml:"tester_model"`
}

type Settings struct {
	BasePath         string          `json:"base_path" yaml:"base_path"`
	WorkspaceFolders []string        `json:"workspace_folders" yaml:"workspace_folders"`
	GitRemoteURL     string          `json:"git_remote_url" yaml:"git_remote_url"`
	GitHubPAT        string          `json:"github_pat" yaml:"github_pat"`
	SystemLLMModel   string          `json:"system_llm_model" yaml:"system_llm_model"`
	RoleModels       RoleModelConfig `json:"role_models" yaml:"role_models"`
}

type SSHKeyPayload struct {
	Key string `json:"key"`
}

func getSettingsFilePath() string {
	return db.SettingsFilePath()
}

func LoadSettings() Settings {
	settingsPath := getSettingsFilePath()
	data, err := os.ReadFile(settingsPath)

	if err != nil {
		return Settings{BasePath: db.PaperclipHome(), WorkspaceFolders: []string{}}
	}

	var settings Settings
	if err := yaml.Unmarshal(data, &settings); err != nil {
		return Settings{BasePath: db.PaperclipHome(), WorkspaceFolders: []string{}}
	}

	if settings.BasePath == "" {
		settings.BasePath = db.PaperclipHome()
	}
	return settings
}

func SaveSettings(settings Settings) error {
	settingsPath := getSettingsFilePath()
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0755); err != nil {
		return err
	}
	data, err := yaml.Marshal(&settings)
	if err != nil {
		return err
	}
	return os.WriteFile(settingsPath, data, 0644)
}

func (api *API) GetSettings(w http.ResponseWriter, r *http.Request) {
	settings := LoadSettings()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(settings)
}

func (api *API) UpdateSettings(w http.ResponseWriter, r *http.Request) {
	var settings Settings
	if err := json.NewDecoder(r.Body).Decode(&settings); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := SaveSettings(settings); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(settings)
}

func (api *API) UploadSSHKey(w http.ResponseWriter, r *http.Request) {
	var payload SSHKeyPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	settings := LoadSettings()
	sshDir := filepath.Join(settings.BasePath, ".ssh")
	if err := os.MkdirAll(sshDir, 0700); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	keyPath := filepath.Join(sshDir, "id_rsa")
	if err := os.WriteFile(keyPath, []byte(payload.Key), 0600); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}
