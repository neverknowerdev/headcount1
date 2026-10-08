package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"agent-orchestrator/db"
	"agent-orchestrator/db/models"
	"agent-orchestrator/engine/workflow"
	"agent-orchestrator/pkg/secrets"

	"gorm.io/gorm"
)

// modelTarget is the model a task's calls go to: a concrete provider and
// model, or a model group addressed through the in-process gateway.
type modelTarget struct {
	Provider db.LLMProvider
	Model    string
	// Tier is the tier the call is billed to.
	Tier string
	// group holds the model group behind a gateway target, for the vault check.
	group *db.ModelGroup
}

func (t modelTarget) viaGateway() bool { return t.group != nil }

// errModelNotConfigured means no usable model is set for a task's tier. The
// task waits, visibly, until one is configured; it is not a failure of the
// task.
type errModelNotConfigured struct{ detail string }

func (e *errModelNotConfigured) Error() string { return e.detail }

// tierForTask is the model tier a task runs on: smart models drive managed
// tasks, cheap models execute direct ones.
func tierForTask(task db.Task) string {
	if task.Mode == models.TaskModeDirect {
		return models.TierCheap
	}
	return models.TierSmart
}

func purposeForTier(tier string) string {
	if tier == models.TierCheap {
		return db.PurposeCheap
	}
	return db.PurposeSmart
}

// resolveTaskModel picks the model for a task: its own override if it names
// one that still exists, otherwise the default of its tier from the Default
// Models settings of the company's owner.
func resolveTaskModel(ctx context.Context, q *db.Queries, task db.Task) (modelTarget, error) {
	tier := tierForTask(task)
	if task.ModelGroupID != nil {
		if group, err := q.GetModelGroup(ctx, *task.ModelGroupID); err == nil {
			return groupTarget(group, tier)
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return modelTarget{}, err
		}
		// The group was deleted: fall through to the tier default.
	}
	if task.ProviderID != nil {
		if provider, err := q.GetLLMProvider(ctx, *task.ProviderID); err == nil {
			return providerTarget(provider, task.Model, tier, "this task's model")
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return modelTarget{}, err
		}
	}

	company, err := q.GetCompany(ctx, task.CompanyID)
	if err != nil {
		return modelTarget{}, err
	}
	if company.UserID == nil {
		return modelTarget{}, &errModelNotConfigured{fmt.Sprintf("this company has no owner whose %s model could be used", tier)}
	}
	return resolveTierDefault(ctx, q, *company.UserID, tier)
}

// resolveTierDefault resolves the default model of a tier for a user.
func resolveTierDefault(ctx context.Context, q *db.Queries, userID int32, tier string) (modelTarget, error) {
	return resolveDefaultModel(ctx, q, userID, purposeForTier(tier), tier)
}

// resolveDefaultModel resolves one Default Models slot for a user. Calls made
// with the result are billed to tier.
func resolveDefaultModel(ctx context.Context, q *db.Queries, userID int32, purpose, tier string) (modelTarget, error) {
	missing := &errModelNotConfigured{fmt.Sprintf("no %s model is configured: set one under LLM Providers → Default Models", tier)}
	setting, err := q.GetDefaultModelSetting(ctx, userID, purpose)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return modelTarget{}, missing
	}
	if err != nil {
		return modelTarget{}, err
	}
	if setting.ModelGroupID != nil {
		group, err := q.GetModelGroup(ctx, *setting.ModelGroupID)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return modelTarget{}, missing
		}
		if err != nil {
			return modelTarget{}, err
		}
		return groupTarget(group, tier)
	}
	if setting.ProviderID == nil {
		return modelTarget{}, missing
	}
	provider, err := q.GetLLMProvider(ctx, *setting.ProviderID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return modelTarget{}, missing
	}
	if err != nil {
		return modelTarget{}, err
	}
	return providerTarget(provider, setting.Model, tier, "the default "+tier+" model")
}

func providerTarget(provider db.LLMProvider, model, tier, what string) (modelTarget, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		model = strings.TrimSpace(provider.DefaultModel)
	}
	if model == "" {
		return modelTarget{}, &errModelNotConfigured{fmt.Sprintf("%s names provider %q but no model", what, provider.Name)}
	}
	return modelTarget{Provider: provider, Model: model, Tier: tier}, nil
}

func groupTarget(group db.ModelGroup, tier string) (modelTarget, error) {
	provider, model, err := resolveModelGroupTarget(group)
	if err != nil {
		return modelTarget{}, &errModelNotConfigured{err.Error()}
	}
	return modelTarget{Provider: provider, Model: model, Tier: tier, group: &group}, nil
}

// vaultLocked reports whether the target cannot be called because the key it
// needs is sealed: its owner is signed out. A group is locked only when every
// member's key is; the gateway routes around the ones that are.
func (t modelTarget) vaultLocked() bool {
	locked := func(provider db.LLMProvider) bool {
		_, err := secrets.Default().Decrypt(provider.ApiKeyEncrypted)
		return errors.Is(err, secrets.ErrLocked)
	}
	if t.group == nil {
		return locked(t.Provider)
	}
	members := t.group.Members
	if len(members) == 0 {
		return false
	}
	for _, member := range members {
		if !locked(member.Provider) {
			return false
		}
	}
	return true
}

const vaultLockedDetail = "the owner of this model's API key is signed out: sign in to continue"

// modelStatus is the workflow's view of whether a task's model can be called
// right now, with the explanation shown to the user when it cannot.
func modelStatus(ctx context.Context, q *db.Queries, task db.Task) (modelTarget, workflow.ModelStatus, string) {
	target, err := resolveTaskModel(ctx, q, task)
	var notConfigured *errModelNotConfigured
	switch {
	case errors.As(err, &notConfigured):
		return target, workflow.ModelNotConfigured, notConfigured.detail
	case err != nil:
		return target, workflow.ModelNotConfigured, "the model for this task could not be resolved: " + err.Error()
	case target.vaultLocked():
		return target, workflow.ModelVaultLocked, vaultLockedDetail
	}
	return target, workflow.ModelReady, ""
}
