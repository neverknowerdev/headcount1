package db

import (
	"strings"
	"time"

	"gorm.io/gorm"
)

// User is an account in cloud multi-user mode. Authentication is passwordless
// (WebAuthn passkeys — see WebAuthnCredential); the email identifies the
// account for login and for passkey recovery. Secrets are protected per-user
// via the passkey PRF-derived key, never a password (see pkg/secrets).
type User struct {
	ID        int32     `json:"id" gorm:"primaryKey"`
	Email     string    `json:"email" gorm:"uniqueIndex;not null"` // stored lowercased/trimmed
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// ReenrollTokenHash / ReenrollExpiresAt bind the re-enrollment that follows a
	// passkey recovery to the browser that performed it. Recovery crypto-shreds
	// the account's credentials, leaving it momentarily credential-less; without
	// this ticket anyone who knows the email could race in and enroll their own
	// passkey onto the (data-bearing) account. Set by RecoverConfirm, verified by
	// RegisterBegin, cleared on a successful RegisterFinish. Empty for accounts
	// that were never recovered (e.g. an abandoned first registration).
	ReenrollTokenHash string     `json:"-"`
	ReenrollExpiresAt *time.Time `json:"-"`
}

// Session is a server-side login session. The cookie carries an opaque random
// token; only its SHA-256 is stored, so a DB dump never yields live sessions.
type Session struct {
	ID        int32     `json:"id" gorm:"primaryKey"`
	TokenHash string    `json:"-" gorm:"uniqueIndex;not null"`
	UserID    int32     `json:"user_id" gorm:"not null;index"`
	User      User      `json:"-" gorm:"foreignKey:UserID;constraint:OnDelete:CASCADE;"`
	ExpiresAt time.Time `json:"expires_at" gorm:"not null"`
	// AbsoluteExpiresAt is the hard ceiling on this access session, carried from
	// the refresh-token family it was minted under. The 1h ExpiresAt slides
	// forward on activity, but it can never slide past this — so an access
	// cookie used hourly still dies at the family's absolute cap instead of
	// renewing forever. Zero means unbounded (legacy rows only).
	AbsoluteExpiresAt time.Time `json:"-"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// RefreshToken is the long-lived half of the access/refresh pair. Access
// sessions are short (~1h); the browser exchanges a refresh token for a new
// pair at /auth/refresh. Tokens rotate on every use and belong to a family:
// replaying an already-used token signals theft and revokes the whole family.
// Only the SHA-256 is stored. Auth only — never carries the encryption DEK.
type RefreshToken struct {
	ID                int32      `json:"-" gorm:"primaryKey"`
	FamilyID          string     `json:"-" gorm:"index;not null"`
	TokenHash         string     `json:"-" gorm:"uniqueIndex;not null"`
	UserID            int32      `json:"-" gorm:"not null;index"`
	User              User       `json:"-" gorm:"foreignKey:UserID;constraint:OnDelete:CASCADE;"`
	ExpiresAt         time.Time  `json:"-" gorm:"not null"`
	AbsoluteExpiresAt time.Time  `json:"-" gorm:"not null"` // hard cap for the whole family
	UsedAt            *time.Time `json:"-"`
	RevokedAt         *time.Time `json:"-"`
	CreatedAt         time.Time  `json:"-"`
}

// PasswordResetToken is a single-use, short-lived token mailed to a user for
// the forgot-password flow. Like sessions, only the SHA-256 is stored.
type PasswordResetToken struct {
	ID        int32      `json:"id" gorm:"primaryKey"`
	TokenHash string     `json:"-" gorm:"uniqueIndex;not null"`
	UserID    int32      `json:"user_id" gorm:"not null;index"`
	User      User       `json:"-" gorm:"foreignKey:UserID;constraint:OnDelete:CASCADE;"`
	ExpiresAt time.Time  `json:"expires_at" gorm:"not null"`
	UsedAt    *time.Time `json:"used_at"`
	CreatedAt time.Time  `json:"created_at"`
}

// WebAuthnCredential is one enrolled passkey. Login is passwordless: the
// credential authenticates the user (assertion), and its WebAuthn PRF output
// unlocks the user's data-encryption key. The DEK is wrapped once per
// credential (each passkey's PRF value differs), so WrappedDEK holds this
// credential's copy; PRFSalt is the constant, non-secret PRF eval input we
// replay at every login to get a stable PRF output. Deleting a user's
// credentials (recovery) crypto-shreds their secrets — the DEK is
// unrecoverable and their "enc:u1:" values become permanently dead.
type WebAuthnCredential struct {
	ID           int32  `json:"id" gorm:"primaryKey"`
	UserID       int32  `json:"user_id" gorm:"index;not null"`
	User         User   `json:"-" gorm:"foreignKey:UserID;constraint:OnDelete:CASCADE;"`
	CredentialID []byte `json:"-" gorm:"uniqueIndex;not null"` // raw WebAuthn credential id
	PublicKey    []byte `json:"-" gorm:"not null"`             // COSE public key
	SignCount    uint32 `json:"-"`
	Transports   string `json:"transports" gorm:"type:text"` // JSON array of authenticator transports
	AAGUID       []byte `json:"-"`
	// WebAuthn backup flags. BackupEligible (BE) is immutable per credential and
	// MUST be restored on login — go-webauthn rejects an assertion whose BE flag
	// differs from the stored one ("Backup Eligible flag inconsistency"). Synced
	// passkeys (iCloud Keychain, Chrome, 1Password, ...) report BE=true, so a
	// credential stored without it (BE=false) can never log in. BackupState (BS)
	// is mutable and refreshed on each successful assertion.
	BackupEligible bool      `json:"-"`
	BackupState    bool      `json:"-"`
	Nickname       string    `json:"nickname"`
	WrappedDEK     string    `json:"-" gorm:"not null"` // DEK sealed under this credential's PRF-derived key
	PRFSalt        []byte    `json:"-" gorm:"not null"` // constant PRF eval input for this credential
	LastUsedAt     time.Time `json:"last_used_at"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// WebAuthnSession stores an in-flight ceremony challenge (registration or
// login) for the short round-trip between begin and finish. Single-use and
// short-lived; only the challenge is kept.
type WebAuthnSession struct {
	ID        int32     `json:"id" gorm:"primaryKey"`
	UserID    *int32    `json:"user_id" gorm:"index"`        // nil until the user is known (registration)
	Purpose   string    `json:"purpose" gorm:"not null"`     // "register" | "login" | "add-credential" | "unlock"
	Data      string    `json:"-" gorm:"type:text;not null"` // serialized webauthn.SessionData
	ExpiresAt time.Time `json:"expires_at" gorm:"not null;index"`
	CreatedAt time.Time `json:"created_at"`
}

// Team roles. Designed for growth (admin tiers, per-role permissions) but
// deliberately flat for now: an owner and a member can do exactly the same
// things, with a single exception — only the owner can invite (and revoke
// invites for) new members. Keep new permission checks role-based so future
// roles slot in without schema changes.
const (
	TeamRoleOwner  = "owner"
	TeamRoleMember = "member"
)

// Team is the top of the tenancy hierarchy: companies (and everything under
// them) belong to a team, and every member of the team can work with them.
// Each user gets a team at registration (as its owner) unless they signed up
// through an invite, in which case they join the inviting team instead.
type Team struct {
	ID        int32     `json:"id" gorm:"primaryKey"`
	Name      string    `json:"name" gorm:"not null"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TeamMember links a user to a team with a role. The schema allows multiple
// memberships per user; current product behavior keeps it at exactly one.
type TeamMember struct {
	ID        int32     `json:"id" gorm:"primaryKey"`
	TeamID    int32     `json:"team_id" gorm:"not null;uniqueIndex:idx_team_member"`
	Team      Team      `json:"-" gorm:"foreignKey:TeamID;constraint:OnDelete:CASCADE;"`
	UserID    int32     `json:"user_id" gorm:"not null;uniqueIndex:idx_team_member"`
	User      User      `json:"-" gorm:"foreignKey:UserID;constraint:OnDelete:CASCADE;"`
	Role      string    `json:"role" gorm:"not null;default:'member'"`
	CreatedAt time.Time `json:"created_at"`
}

// TeamInvite is an owner-issued, emailed invitation. Only the SHA-256 of the
// token is stored (same discipline as sessions and reset tokens); accepting
// happens by registering with the raw token, which joins the new account to
// the team instead of creating a fresh one.
type TeamInvite struct {
	ID         int32      `json:"id" gorm:"primaryKey"`
	TeamID     int32      `json:"team_id" gorm:"not null;index"`
	Team       Team       `json:"-" gorm:"foreignKey:TeamID;constraint:OnDelete:CASCADE;"`
	Email      string     `json:"email" gorm:"not null"` // normalized; informational + pre-fill
	Role       string     `json:"role" gorm:"not null;default:'member'"`
	TokenHash  string     `json:"-" gorm:"uniqueIndex;not null"`
	InvitedBy  int32      `json:"invited_by" gorm:"not null"`
	ExpiresAt  time.Time  `json:"expires_at" gorm:"not null"`
	AcceptedAt *time.Time `json:"accepted_at"`
	CreatedAt  time.Time  `json:"created_at"`
}

type Company struct {
	ID        int32  `json:"id" gorm:"primaryKey"`
	Name      string `json:"name" gorm:"not null"`
	ShortName string `json:"short_name" gorm:"not null"`
	Color     string `json:"color"`
	// TeamID is the owning team: everything scoped to a company — projects,
	// agents, tasks, runs — is visible to every member of that team. UserID
	// records the member who created the company (and whose per-user Default
	// Models the engine resolves for its background LLM calls).
	TeamID    *int32    `json:"team_id" gorm:"index"`
	UserID    *int32    `json:"user_id" gorm:"index"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Project struct {
	ID                   int32     `json:"id" gorm:"primaryKey"`
	CompanyID            int32     `json:"company_id" gorm:"not null"`
	Company              Company   `json:"company" gorm:"foreignKey:CompanyID;constraint:OnDelete:CASCADE;"`
	Name                 string    `json:"name" gorm:"not null"`
	Description          string    `json:"description"`
	WorkspaceFolder      string    `json:"workspace_folder"`
	RepositoryUrl        string    `json:"repository_url"`
	GitHubRepositoryID   int64     `json:"github_repository_id" gorm:"index"`
	GitHubInstallationID int64     `json:"github_installation_id" gorm:"index"`
	GitHubDefaultBranch  string    `json:"github_default_branch"`
	IsExternal           bool      `json:"is_external" gorm:"not null;default:false"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
}

type Sprint struct {
	ID        int32      `json:"id" gorm:"primaryKey"`
	CompanyID int32      `json:"company_id" gorm:"not null"`
	Company   Company    `json:"company" gorm:"foreignKey:CompanyID;constraint:OnDelete:CASCADE;"`
	Name      string     `json:"name" gorm:"not null"`
	Goal      string     `json:"goal"`
	StartDate *time.Time `json:"start_date"`
	EndDate   *time.Time `json:"end_date"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

type LLMProvider struct {
	ID      int32  `json:"id" gorm:"primaryKey"`
	Name    string `json:"name" gorm:"not null"`
	BaseUrl string `json:"base_url" gorm:"not null"`
	// ApiKeyEncrypted holds the SEALED api key (ciphertext) — both at rest and in
	// memory. It is never serialized to clients (the frontend only sees
	// HasApiKey). Decrypt at the point of use via secrets.Default().Decrypt().
	ApiKeyEncrypted string `json:"-" gorm:"column:api_key;not null;serializer:sealed"`
	HasApiKey       bool   `json:"has_api_key" gorm:"-"` // computed: ApiKeyEncrypted != ""
	// UserID is the owning user; secrets on owned rows are sealed with that
	// user's DEK ("enc:u1:"). Nil = ownerless (sealed with the root DEK).
	UserID          *int32 `json:"user_id" gorm:"index"`
	ProviderType    string `json:"provider_type"`
	DefaultModel    string `json:"default_model"`
	SupportedModels string `json:"supported_models"`
	// Builtin marks providers seeded automatically on startup (e.g. OpenRouter,
	// OpenCode Zen) rather than added by hand. Used to know which providers'
	// model catalogs are safe to refresh from a live discovery fetch.
	Builtin bool `json:"builtin" gorm:"not null;default:false"`
	// Enabled lets a builtin provider be turned off without deleting it —
	// deleting a builtin row is pointless anyway, since EnsureBuiltinLLMProvidersForUser
	// just recreates it (blank) on the next startup. Defaults to true so
	// existing/freshly-seeded providers stay usable.
	Enabled bool `json:"enabled" gorm:"not null;default:true"`
	// PresetKey records which ProviderPreset (if any) this provider was
	// created from — e.g. "opencode-go", "minimax". Empty for the builtin
	// free providers and for fully custom ones. Lets RediscoverProviderModels
	// route to the right (authenticated) discovery logic without having to
	// match on Name/BaseUrl.
	PresetKey string `json:"preset_key" gorm:"default:''"`
	// ProviderName is a stable, non-editable identifier for a builtin free
	// provider ("OpenRouter", "OpenCode") — used to route to the right
	// discovery logic instead of matching the user-facing Name field, which
	// UpdateProvider allows changing. Empty for presets and fully custom
	// providers, which use PresetKey/BaseUrl matching instead.
	ProviderName string `json:"provider_name" gorm:"default:''"`
	// Slug is the provider's stable, portable domain identity — deterministically
	// derived from the fields that make one provider "the same" as another
	// (builtin vendor name, preset key, or name+base URL). Unlike the DB id it
	// survives an export/import into a different database, so tenant restore can
	// dedup providers by slug instead of duplicating them. Set by BeforeCreate
	// and backfilled for existing rows (BackfillProviderSlugs); deterministic so
	// two independently-seeded accounts share a slug for the same builtin.
	Slug      string    `json:"slug" gorm:"index;default:''"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ProviderSlug derives an LLMProvider's stable domain slug from the fields that
// define provider identity. Deterministic (no random component) so the same
// provider seeded in two different databases resolves to the same slug — which
// is what lets tenant import dedup providers across accounts.
func ProviderSlug(p LLMProvider) string {
	switch {
	case p.ProviderName != "":
		return "builtin:" + slugify(p.ProviderName)
	case p.PresetKey != "":
		return "preset:" + slugify(p.PresetKey)
	default:
		return "custom:" + slugify(p.Name) + ":" + slugify(p.BaseUrl)
	}
}

// BeforeCreate stamps the derived slug on any struct-based provider insert (all
// app create paths use api.db.Create(&p)). Map-based inserts — the tenant
// importer replaying archived rows — skip GORM hooks and keep the archive's
// slug verbatim, which is exactly what dedup needs.
func (p *LLMProvider) BeforeCreate(tx *gorm.DB) error {
	if p.Slug == "" {
		p.Slug = ProviderSlug(*p)
	}
	return nil
}

// slugify lowercases s and collapses every run of non-alphanumeric characters
// into a single '-', trimming leading/trailing dashes. Stable across platforms.
func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	prevDash := false
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevDash = false
		} else if !prevDash {
			b.WriteByte('-')
			prevDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

// ProviderPreset is a known third-party LLM gateway a user can pick from a
// dropdown when adding a provider, instead of typing in a base URL and
// provider type from scratch — they only need to paste in their own API
// key. Unlike the builtin free providers (OpenRouter, OpenCode Zen), a
// preset isn't auto-created at startup; it only becomes an actual
// LLMProvider row once the user picks it and supplies a key. Add a new
// provider by adding a row here (see EnsureProviderPresets), not by writing
// bespoke fetch code — unless its /models endpoint needs special handling,
// see pkg/llmdiscovery.PresetDiscoverer.
type ProviderPreset struct {
	ID           int32     `json:"id" gorm:"primaryKey"`
	Key          string    `json:"key" gorm:"uniqueIndex;not null"`
	Name         string    `json:"name" gorm:"not null"`
	BaseUrl      string    `json:"base_url" gorm:"not null"`
	ProviderType string    `json:"provider_type" gorm:"not null"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type Agent struct {
	ID           int32        `json:"id" gorm:"primaryKey"`
	CompanyID    int32        `json:"company_id" gorm:"not null"`
	Company      Company      `json:"company" gorm:"foreignKey:CompanyID;constraint:OnDelete:CASCADE;"`
	Name         string       `json:"name" gorm:"not null"`
	Description  string       `json:"description"`
	SystemPrompt string       `json:"system_prompt" gorm:"not null"`
	ProviderID   *int32       `json:"provider_id"`
	Provider     *LLMProvider `json:"provider" gorm:"foreignKey:ProviderID;constraint:OnDelete:SET NULL;"`
	// ModelGroupID, when set, takes precedence over ProviderID/Model: the
	// agent's LLM calls go through the model-group router (free-first,
	// auto-failover) instead of a fixed provider+model.
	ModelGroupID *int32      `json:"model_group_id"`
	ModelGroup   *ModelGroup `json:"model_group,omitempty" gorm:"foreignKey:ModelGroupID;constraint:OnDelete:SET NULL;"`
	Model        string      `json:"model"`
	Mode         string      `json:"mode" gorm:"not null;default:'primary'"`
	Permissions  string      `json:"permissions"`
	CreatedAt    time.Time   `json:"created_at"`
	UpdatedAt    time.Time   `json:"updated_at"`
	Skills       []Skill     `json:"skills" gorm:"many2many:agent_skills;"`
}

type Skill struct {
	ID          int32     `json:"id" gorm:"primaryKey"`
	CompanyID   int32     `json:"company_id" gorm:"not null"`
	Company     Company   `json:"company" gorm:"foreignKey:CompanyID;constraint:OnDelete:CASCADE;"`
	Name        string    `json:"name" gorm:"not null"`
	Description string    `json:"description"`
	SourceUrl   string    `json:"source_url"`
	LocalPath   string    `json:"local_path" gorm:"not null"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

const (
	TaskTypePlanAndImplement = "plan and implement"
	TaskTypeImplement        = "implement"
	// DefaultTaskGitBaseBranch is used by new and legacy tasks when no base
	// branch was explicitly selected.
	DefaultTaskGitBaseBranch = "main"
)

type Task struct {
	ID        int32   `json:"id" gorm:"primaryKey"`
	CompanyID int32   `json:"company_id" gorm:"not null"`
	Company   Company `json:"company" gorm:"foreignKey:CompanyID;constraint:OnDelete:CASCADE;"`
	// CreatedByUserID identifies the human whose task owns the Git changes.
	// It is optional for legacy tasks; the engine falls back to the company
	// owner for those rows.
	CreatedByUserID *int32   `json:"created_by_user_id" gorm:"index"`
	ProjectID       *int32   `json:"project_id"`
	Project         *Project `json:"project" gorm:"foreignKey:ProjectID;constraint:OnDelete:SET NULL;"`
	SprintID        int32    `json:"sprint_id" gorm:"not null"`
	Sprint          Sprint   `json:"sprint" gorm:"foreignKey:SprintID;constraint:OnDelete:CASCADE;"`
	AgentID         *int32   `json:"agent_id"`
	Agent           *Agent   `json:"agent" gorm:"foreignKey:AgentID;constraint:OnDelete:SET NULL;"`
	ParentID        *int32   `json:"parent_id"`
	Parent          *Task    `json:"parent" gorm:"foreignKey:ParentID;constraint:OnDelete:SET NULL;"`
	Title           string   `json:"title" gorm:"not null"`
	TaskType        string   `json:"task_type" gorm:"not null;default:'plan and implement'"`
	// Description holds the user's original input, untouched. For delegated
	// subtasks the owner's instructions land in RefinedDescription instead,
	// shown separately in the UI.
	Description string `json:"description"`
	// RefKey is the human-readable task key: "DEC-50" for a main task,
	// "DEC-50-1", "DEC-50-2", … for its subtasks. Set on creation.
	RefKey             string     `json:"ref_key" gorm:"index"`
	RefinedDescription string     `json:"refined_description" gorm:"type:text;default:''"`
	AcceptanceCriteria string     `json:"acceptance_criteria" gorm:"type:text;default:''"`
	TestCases          string     `json:"test_cases" gorm:"type:text;default:''"`
	Priority           string     `json:"priority" gorm:"not null;default:'Normal'"`
	Status             string     `json:"status" gorm:"not null;default:'backlog'"`
	DueDate            *time.Time `json:"due_date"`
	IsArchived         bool       `json:"is_archived" gorm:"not null;default:false"`
	RunID              *int32     `json:"run_id"`
	AgentConfigName    string     `json:"agent_config_name" gorm:"default:''"`
	GitHubPRNumber     int        `json:"github_pr_number"`
	GitHubPRURL        string     `json:"github_pr_url"`
	// GitHubBranch is canonical on the root task. Subtasks copy the same value
	// so every run in the task tree operates on one branch.
	GitHubBranch  string    `json:"github_branch" gorm:"index"`
	GitBaseBranch string    `json:"git_base_branch" gorm:"not null;default:'main'"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// EffectiveGitBaseBranch returns the branch used to create this task's
// worktree. Empty values are possible on tasks created before this field was
// added, and continue to mean main.
func (t Task) EffectiveGitBaseBranch() string {
	if branch := strings.TrimSpace(t.GitBaseBranch); branch != "" {
		return branch
	}
	return DefaultTaskGitBaseBranch
}

// GitHubOAuthState is short lived and prevents authorization callbacks from
// being attached to an unrelated Headcount1 session.
type GitHubOAuthState struct {
	ID          string `json:"-" gorm:"primaryKey"`
	RedirectURL string `json:"-"`
	// MCPAccount details bind an OAuth callback to the account the user chose
	// from the integrations screen.  This keeps personal and work identities
	// separate even when they use the same GitHub App installation.
	MCPServerID  int32      `json:"-" gorm:"default:0"`
	UserID       int32      `json:"-" gorm:"default:0"`
	MCPAccountID int32      `json:"-" gorm:"default:0"`
	ReturnPath   string     `json:"-"`
	ExpiresAt    time.Time  `json:"-"`
	UsedAt       *time.Time `json:"-"`
	CreatedAt    time.Time
}

// GitHubConnection links one GitHub App installation to an MCP account. A
// person may connect multiple GitHub identities (or the same identity with
// different repository selections) without exposing them to other users.
type GitHubConnection struct {
	ID int32 `json:"id" gorm:"primaryKey"`
	// One account may only record an installation once. The installation is
	// intentionally not globally unique: personal and work MCP accounts may
	// both be allowed to use the same organisation installation.
	InstallationID int64     `json:"installation_id" gorm:"index"`
	MCPAccountID   int32     `json:"mcp_account_id" gorm:"index"`
	UserID         int32     `json:"user_id" gorm:"index"`
	AccountLogin   string    `json:"account_login"`
	ConnectedAt    time.Time `json:"connected_at"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// GitHubIdentity is the durable, GitHub-only uniqueness boundary for OAuth
// accounts. A generic MCP account may have any number of credentials, while a
// given Headcount1 user must never connect the same GitHub person twice to the
// same GitHub MCP server.
type GitHubIdentity struct {
	ID           int32  `json:"id" gorm:"primaryKey"`
	MCPAccountID int32  `json:"mcp_account_id" gorm:"not null;uniqueIndex"`
	MCPServerID  int32  `json:"mcp_server_id" gorm:"not null;uniqueIndex:idx_github_identity"`
	UserID       int32  `json:"user_id" gorm:"not null;uniqueIndex:idx_github_identity"`
	GitHubUserID int64  `json:"github_user_id" gorm:"not null;uniqueIndex:idx_github_identity"`
	GitHubLogin  string `json:"github_login"`
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// GitHubWebhookDelivery records the lifecycle of a verified GitHub delivery.
// A delivery is complete only after its task comments have been committed; a
// failed delivery remains retryable by GitHub (and, when configured, by the
// staging relay).
type GitHubWebhookDelivery struct {
	ID          int32      `json:"id" gorm:"primaryKey"`
	DeliveryID  string     `json:"delivery_id" gorm:"uniqueIndex"`
	Event       string     `json:"event"`
	Status      string     `json:"status" gorm:"not null;default:'processing'"` // processing | failed | completed
	ForwardedAt *time.Time `json:"forwarded_at"`
	CompletedAt *time.Time `json:"completed_at"`
	LastError   string     `json:"last_error" gorm:"type:text"`
	// AttemptToken and LeaseExpiresAt form a compare-and-swap lease. They keep
	// a redelivery from completing or failing work claimed by a newer request.
	AttemptToken   string     `json:"-" gorm:"index"`
	LeaseExpiresAt *time.Time `json:"-"`
	Attempts       int        `json:"attempts" gorm:"not null;default:0"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// GitHubWebhookTarget makes each delivery/task pair idempotent. Comments and
// their target rows are created in one transaction, so a partial retry can
// never duplicate a comment that was already committed.
type GitHubWebhookTarget struct {
	ID         int32  `json:"id" gorm:"primaryKey"`
	DeliveryID string `json:"delivery_id" gorm:"not null;uniqueIndex:idx_github_delivery_task"`
	TaskID     int32  `json:"task_id" gorm:"not null;uniqueIndex:idx_github_delivery_task"`
	CommentID  int32  `json:"comment_id" gorm:"not null"`
	// A committed comment is an outbox item until the matching agent wake has
	// succeeded. This makes a failed engine invocation retryable without ever
	// creating the comment twice.
	WakeStatus         string     `json:"wake_status" gorm:"not null;default:'pending'"`
	WakeAttemptToken   string     `json:"-" gorm:"index"`
	WakeLeaseExpiresAt *time.Time `json:"-"`
	WakeAttempts       int        `json:"wake_attempts" gorm:"not null;default:0"`
	WakeLastError      string     `json:"wake_last_error" gorm:"type:text"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
}

type Comment struct {
	ID          int32     `json:"id" gorm:"primaryKey"`
	TaskID      int32     `json:"task_id" gorm:"not null"`
	Task        Task      `json:"task" gorm:"foreignKey:TaskID;constraint:OnDelete:CASCADE;"`
	AuthorType  string    `json:"author_type" gorm:"not null"`
	AuthorID    *int32    `json:"author_id"`
	Content     string    `json:"content" gorm:"not null"`
	CommentType string    `json:"comment_type" gorm:"default:''"`
	RunID       *int32    `json:"run_id"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type Attachment struct {
	ID        int32     `json:"id" gorm:"primaryKey"`
	TaskID    int32     `json:"task_id" gorm:"not null"`
	Task      Task      `json:"task" gorm:"foreignKey:TaskID;constraint:OnDelete:CASCADE;"`
	CommentID *int32    `json:"comment_id"`
	Comment   *Comment  `json:"comment" gorm:"foreignKey:CommentID;constraint:OnDelete:CASCADE;"`
	Filename  string    `json:"filename" gorm:"not null"`
	FilePath  string    `json:"file_path" gorm:"not null"`
	MimeType  string    `json:"mime_type"`
	CreatedAt time.Time `json:"created_at"`
}

type Run struct {
	ID      int32 `json:"id" gorm:"primaryKey"`
	TaskID  int32 `json:"task_id" gorm:"not null"`
	Task    Task  `json:"task" gorm:"foreignKey:TaskID;constraint:OnDelete:CASCADE;"`
	AgentID int32 `json:"agent_id" gorm:"not null"`
	Agent   Agent `json:"agent" gorm:"foreignKey:AgentID;constraint:OnDelete:CASCADE;"`
	// Name is the human-readable run key: "<task ref>-<AGENTSHORT>[-n]",
	// e.g. "DEC-50-CEO", "DEC-50-2-QA-2". Set when the run starts.
	Name string `json:"name" gorm:"index"`
	// ParentRunID links a delegated session to the run that spawned it.
	// RootRunID points at the top-level (CEO) run of the whole execution tree;
	// it equals ID for root runs so log files can be grouped per main run.
	ParentRunID     *int32 `json:"parent_run_id" gorm:"index"`
	RootRunID       *int32 `json:"root_run_id" gorm:"index"`
	AgentConfigName string `json:"agent_config_name" gorm:"default:''"`
	// CurrentStatus is a short free-text progress line set by the agent via
	// the report_status tool, shown live in the Run Log UI.
	CurrentStatus     string     `json:"current_status" gorm:"default:''"`
	Status            string     `json:"status" gorm:"not null"`
	SessionID         string     `json:"session_id"`
	LogFilePath       string     `json:"log_file_path"`
	LogContent        string     `json:"log_content"`
	LogEntries        string     `json:"log_entries" gorm:"type:text"`        // JSON array of structured log entries
	TokenStats        string     `json:"token_stats" gorm:"type:text"`        // JSON object with aggregated token counts
	ResultDescription string     `json:"result_description" gorm:"type:text"` // short summary set by finish_task_execution
	ResultExplanation string     `json:"result_explanation" gorm:"type:text"` // detailed explanation set by finish_task_execution
	StartedAt         time.Time  `json:"started_at"`
	EndedAt           *time.Time `json:"ended_at"`
	LastMessageTime   *time.Time `json:"last_message_time"`
	// PausedHistory is the serialized ([]aicli.Message JSON) conversation
	// captured when a graceful shutdown (e.g. applying an auto-update) paused
	// this run mid-flight, right after its current turn's LLM response
	// arrived. Set only while Status == "interrupted"; consumed and cleared
	// when the run resumes. Internal plumbing, not for display — omitted from
	// the JSON API.
	PausedHistory string `json:"-" gorm:"type:text"`
}

// RunTokenStats holds aggregated token counts for a run. Persisted to
// Run.TokenStats as JSON so the Run Logs UI can render an overall
// breakdown without re-reading the JSONL log on every request.
type RunTokenStats struct {
	PromptTokens     int            `json:"prompt_tokens"`               // sum of all LLM request input tokens (provider-reported)
	CompletionTokens int            `json:"completion_tokens"`           // sum of all LLM response output tokens (provider-reported)
	ReasoningTokens  int            `json:"reasoning_tokens"`            // sum of reasoning tokens (provider-reported, or estimated)
	ToolInputTokens  int            `json:"tool_input_tokens"`           // sum of tool call argument sizes (estimated, chars/4)
	ToolOutputTokens int            `json:"tool_output_tokens"`          // sum of tool response sizes (estimated, chars/4)
	CachedTokens     int            `json:"cached_tokens"`               // sum of cached prompt tokens (subset of PromptTokens)
	TotalTokens      int            `json:"total_tokens"`                // sum of everything above (excludes CachedTokens)
	MCPToolTokens    int            `json:"mcp_tool_tokens,omitempty"`   // subset of ToolOutputTokens from MCP dispatcher calls
	MCPServerTokens  map[string]int `json:"mcp_server_tokens,omitempty"` // per-server breakdown of MCPToolTokens
}

// IsEmpty reports whether all fields are zero, safe for use with map fields.
func (s RunTokenStats) IsEmpty() bool {
	return s.PromptTokens == 0 && s.CompletionTokens == 0 && s.ReasoningTokens == 0 &&
		s.ToolInputTokens == 0 && s.ToolOutputTokens == 0 && s.CachedTokens == 0 &&
		s.MCPToolTokens == 0 && len(s.MCPServerTokens) == 0
}

// MCPServer stores configuration for an MCP (Model Context Protocol) server.
// MCP servers are global (not company-scoped), like LLM providers.
type MCPServer struct {
	ID   int32  `json:"id" gorm:"primaryKey"`
	Name string `json:"name" gorm:"not null;uniqueIndex"` // unique slug, e.g. "github"
	// OwnerUserID is set for user-created custom servers only. Builtin rows
	// (the shared catalog) and codegraph servers (scoped via ProjectID →
	// Project → Company) stay NULL. Per-user credentials live in MCPAccount.
	OwnerUserID   *int32       `json:"owner_user_id" gorm:"index"`
	DisplayName   string       `json:"display_name"`
	Description   string       `json:"description"`
	Transport     string       `json:"transport" gorm:"not null"` // "stdio", "http", "builtin"
	Command       string       `json:"command"`
	Args          string       `json:"args" gorm:"type:text"`
	URL           string       `json:"url"`
	Headers       string       `json:"headers" gorm:"type:text"`
	AuthType      string       `json:"auth_type"`  // "none", "bearer", "credentials-file"
	AuthToken     string       `json:"-" gorm:"-"` // runtime-only carrier: filled from the per-account token when building a transport client (see engine synthetic servers); tokens live in MCPAccount
	AuthEnvVar    string       `json:"auth_env_var"`
	ToolsCache    string       `json:"tools_cache" gorm:"type:text"`
	LastError     string       `json:"last_error" gorm:"type:text"`
	InitStatus    string       `json:"init_status" gorm:"default:''"` // codegraph lifecycle: "initializing", "ready", "error: ..."
	DepsInstalled bool         `json:"deps_installed" gorm:"-"`       // computed at runtime
	Deps          string       `json:"deps" gorm:"type:text"`         // JSON array of npm packages to pre-install, e.g. ["@modelcontextprotocol/server-gdrive"]
	Enabled       bool         `json:"enabled" gorm:"not null;default:true"`
	Builtin       bool         `json:"builtin" gorm:"not null;default:false"`
	WorkDir       string       `json:"work_dir"` // working directory for stdio servers (e.g. project repo path)
	ProjectID     *int32       `json:"project_id" gorm:"index"`
	Project       *Project     `json:"project,omitempty" gorm:"foreignKey:ProjectID;constraint:OnDelete:CASCADE;"`
	Accounts      []MCPAccount `json:"accounts,omitempty" gorm:"foreignKey:MCPServerID"`
	Agents        []Agent      `json:"agents,omitempty" gorm:"many2many:agent_mcp_servers;"`
	CreatedAt     time.Time    `json:"created_at"`
	UpdatedAt     time.Time    `json:"updated_at"`
}

const (
	MCPServerNameGitHub  = "github"
	MCPAuthTypeGitHubApp = "github-app"
	MCPTransportBuiltin  = "builtin"
)

func (s MCPServer) IsGitHub() bool {
	return s.Builtin && s.Name == MCPServerNameGitHub && s.AuthType == MCPAuthTypeGitHubApp
}

// MCPAccount holds credentials for one identity on an MCPServer.
// A server can have multiple accounts (e.g., personal + work GitHub tokens).
type MCPAccount struct {
	ID                 int32     `json:"id" gorm:"primaryKey"`
	MCPServerID        int32     `json:"mcp_server_id" gorm:"not null;index"`
	Name               string    `json:"name" gorm:"not null"`                                   // user label: "Personal", "Work"
	AuthTokenEncrypted string    `json:"-" gorm:"column:auth_token;type:text;serializer:sealed"` // SEALED credential (ciphertext); decrypt at use via secrets.Default().Decrypt()
	HasToken           bool      `json:"has_token" gorm:"-"`                                     // computed: AuthTokenEncrypted != ""
	UserID             *int32    `json:"user_id" gorm:"index"`                                   // owning user; their DEK seals AuthTokenEncrypted
	LastError          string    `json:"last_error" gorm:"type:text"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// AgentMCPServer is the legacy join table for the Agent <-> MCPServer many-to-many.
// Still used for built-in (headcount1) assignments.
type AgentMCPServer struct {
	AgentID     int32 `json:"agent_id" gorm:"primaryKey"`
	MCPServerID int32 `json:"mcp_server_id" gorm:"primaryKey"`
	Enabled     bool  `json:"enabled" gorm:"not null;default:true"`
}

// AgentMCPAccount is the join table for Agent <-> MCPAccount.
// External MCP servers are assigned at the account level (not server level).
type AgentMCPAccount struct {
	AgentID      int32 `json:"agent_id" gorm:"primaryKey"`
	MCPAccountID int32 `json:"mcp_account_id" gorm:"primaryKey"`
	Enabled      bool  `json:"enabled" gorm:"not null;default:true"`
}

// MCPToolStat tracks cumulative call counts per tool per MCP server.
// Used to sort tools by popularity in the agent system prompt.
type MCPToolStat struct {
	ID          int32  `json:"id" gorm:"primaryKey;autoIncrement"`
	MCPServerID int32  `json:"mcp_server_id" gorm:"not null;uniqueIndex:idx_mcp_tool_stat"`
	ToolName    string `json:"tool_name" gorm:"not null;uniqueIndex:idx_mcp_tool_stat"`
	CallCount   int64  `json:"call_count" gorm:"not null;default:0"`
}

// AgentMCPToolFilter stores per-agent, per-server tool enable/disable settings.
// When no row exists for a (agent, server, tool) triple, the tool is enabled by default.
type AgentMCPToolFilter struct {
	AgentID     int32  `json:"agent_id" gorm:"primaryKey"`
	MCPServerID int32  `json:"mcp_server_id" gorm:"primaryKey"`
	ToolName    string `json:"tool_name" gorm:"primaryKey"`
	Enabled     bool   `json:"enabled" gorm:"not null;default:true"`
}

type Artifact struct {
	ID        int32    `json:"id" gorm:"primaryKey"`
	CompanyID *int32   `json:"company_id" gorm:"index"`
	Company   *Company `json:"company,omitempty" gorm:"foreignKey:CompanyID;constraint:OnDelete:CASCADE;"`
	ProjectID *int32   `json:"project_id" gorm:"index"`
	Project   *Project `json:"project,omitempty" gorm:"foreignKey:ProjectID;constraint:OnDelete:CASCADE;"`
	TaskID    int32    `json:"task_id" gorm:"not null"`
	Task      Task     `json:"task" gorm:"foreignKey:TaskID;constraint:OnDelete:CASCADE;"`
	RunID     int32    `json:"run_id" gorm:"not null"`
	Run       Run      `json:"run" gorm:"foreignKey:RunID;constraint:OnDelete:CASCADE;"`
	Filename  string   `json:"filename" gorm:"not null"`
	FilePath  string   `json:"file_path" gorm:"not null"`
	Content   string   `json:"content" gorm:"type:text"`
	// Description is a one-line summary provided by the producing agent.
	// IsVerified is set when a QA verification session passes all spec items
	// for the artifact's task.
	Description string    `json:"description" gorm:"type:text;default:''"`
	IsVerified  bool      `json:"is_verified" gorm:"not null;default:false"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type ActivityLog struct {
	ID         int32     `json:"id" gorm:"primaryKey"`
	CompanyID  int32     `json:"company_id" gorm:"not null"`
	Company    Company   `json:"company" gorm:"foreignKey:CompanyID;constraint:OnDelete:CASCADE;"`
	Action     string    `json:"action" gorm:"not null"` // e.g., "task_created", "task_status_updated", "agent_run_started"
	EntityID   int32     `json:"entity_id"`              // Optional, ID of the task, agent, etc.
	EntityType string    `json:"entity_type"`            // e.g., "task", "agent", "skill"
	Details    string    `json:"details"`                // JSON string with more context
	CreatedAt  time.Time `json:"created_at"`
}

// ModelGroup is a named set of provider+model pairs exposed behind a single
// OpenAI-compatible proxy URL (/api/proxy/group/{slug}/v1). The gateway
// routes each request to the healthiest member, preferring free models and
// failing over automatically on errors or rate limits.
type ModelGroup struct {
	ID   int32  `json:"id" gorm:"primaryKey"`
	Name string `json:"name" gorm:"not null"`
	// Slug stays globally unique (not per-user): the machine proxy route
	// /api/proxy/group/{slug} resolves it without any user context.
	Slug        string             `json:"slug" gorm:"not null;uniqueIndex"`
	UserID      *int32             `json:"user_id" gorm:"index"` // owning user
	Description string             `json:"description"`
	Members     []ModelGroupMember `json:"members" gorm:"foreignKey:GroupID"`
	CreatedAt   time.Time          `json:"created_at"`
	UpdatedAt   time.Time          `json:"updated_at"`
}

// ModelGroupMember is one provider+model pair inside a ModelGroup. Free
// members are always tried before paid ones; Priority orders members within
// the same tier (lower = tried first).
type ModelGroupMember struct {
	ID         int32       `json:"id" gorm:"primaryKey"`
	GroupID    int32       `json:"group_id" gorm:"not null;index"`
	ProviderID int32       `json:"provider_id" gorm:"not null"`
	Provider   LLMProvider `json:"provider" gorm:"foreignKey:ProviderID;constraint:OnDelete:CASCADE;"`
	// Model is the concrete model id to route to. Empty when AllModels is
	// set — the member then stands for every model currently listed in the
	// provider's SupportedModels, resolved at request time (so it tracks a
	// provider's catalog automatically as it changes).
	Model     string    `json:"model"`
	AllModels bool      `json:"all_models" gorm:"not null;default:false"`
	IsFree    bool      `json:"is_free" gorm:"not null;default:false"`
	Priority  int       `json:"priority" gorm:"not null;default:0"`
	CreatedAt time.Time `json:"created_at"`
}

// ModelRequestStat records the outcome of a single LLM request routed by the
// gateway: success, hard failure, or rate limit. Rate limits are tracked
// separately from failures so they don't count against a model's failure %.
// TokensPerSec is completion tokens divided by wall-clock duration.
type ModelRequestStat struct {
	ID               int32   `json:"id" gorm:"primaryKey"`
	GroupID          *int32  `json:"group_id" gorm:"index"`
	ProviderID       int32   `json:"provider_id" gorm:"not null;index"`
	Model            string  `json:"model" gorm:"not null;index"`
	Success          bool    `json:"success" gorm:"not null;default:false"`
	RateLimited      bool    `json:"rate_limited" gorm:"not null;default:false"`
	StatusCode       int     `json:"status_code"`
	DurationMs       int64   `json:"duration_ms"`
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	TokensPerSec     float64 `json:"tokens_per_sec"`
	// CooldownUntil is set on rate-limited rows: the gateway won't route to
	// this provider+model again until this time (unless nothing else works).
	CooldownUntil *time.Time `json:"cooldown_until"`
	ErrorMessage  string     `json:"error_message" gorm:"type:text"`
	CreatedAt     time.Time  `json:"created_at" gorm:"index"`
}

// DefaultModelSetting configures which provider+model (or model group) to
// use for one internal purpose (e.g. commit-message generation, the
// ask_artifact one-shot reader) — independent of any Model Group's own
// definition, so a purpose can point at a fixed provider+model, at any
// model group (built-in or custom), or fall back to the calling session's
// own LLM when left unconfigured (both fields nil).
type DefaultModelSetting struct {
	ID           int32        `json:"id" gorm:"primaryKey"`
	Purpose      string       `json:"purpose" gorm:"not null;uniqueIndex:idx_dms_user_purpose"`
	UserID       *int32       `json:"user_id" gorm:"uniqueIndex:idx_dms_user_purpose"` // owning user
	ProviderID   *int32       `json:"provider_id"`
	Provider     *LLMProvider `json:"provider,omitempty" gorm:"foreignKey:ProviderID;constraint:OnDelete:SET NULL;"`
	Model        string       `json:"model"`
	ModelGroupID *int32       `json:"model_group_id"`
	ModelGroup   *ModelGroup  `json:"model_group,omitempty" gorm:"foreignKey:ModelGroupID;constraint:OnDelete:SET NULL;"`
	CreatedAt    time.Time    `json:"created_at"`
	UpdatedAt    time.Time    `json:"updated_at"`
}

// UserGitCredential holds a user's own git credentials, encrypted at rest under
// their per-user DEK (same as provider keys). Each user has their own SSH key so
// the agent pushes under that user's identity — replacing the single shared key
// that any account could previously overwrite. SSHPrivateKeyEncrypted is only decryptable
// while the owner's vault is unlocked.
type UserGitCredential struct {
	ID                     int32     `json:"id" gorm:"primaryKey"`
	UserID                 *int32    `json:"user_id" gorm:"uniqueIndex;not null"`
	User                   *User     `json:"-" gorm:"foreignKey:UserID;constraint:OnDelete:CASCADE;"`
	SSHPrivateKeyEncrypted string    `json:"-" gorm:"column:ssh_private_key;type:text;serializer:sealed"` // SEALED PEM; decrypt at use via secrets.Default().Decrypt()
	CreatedAt              time.Time `json:"created_at"`
	UpdatedAt              time.Time `json:"updated_at"`
}

type ProxyRequestLog struct {
	ID               int32       `json:"id" gorm:"primaryKey"`
	AgentID          int32       `json:"agent_id" gorm:"not null"`
	Agent            Agent       `json:"agent" gorm:"foreignKey:AgentID;constraint:OnDelete:CASCADE;"`
	ProviderID       int32       `json:"provider_id" gorm:"not null"`
	Provider         LLMProvider `json:"provider" gorm:"foreignKey:ProviderID;constraint:OnDelete:CASCADE;"`
	Model            string      `json:"model" gorm:"not null"`
	PromptTokens     int         `json:"prompt_tokens"`
	CompletionTokens int         `json:"completion_tokens"`
	TotalTokens      int         `json:"total_tokens"`
	CreatedAt        time.Time   `json:"created_at"`
}
