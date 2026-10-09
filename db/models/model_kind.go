package models

import (
	"slices"
	"strings"
)

// A model is one of two kinds. A language model writes: it is called through
// chat completions and does a task's thinking and work. A System One model
// (TypeSafe's Jev) only decides: given a text and typed questions it returns
// a probability, a choice or a score, through its own endpoint. The two are
// never interchangeable, so wherever a model is chosen its kind is fixed by
// what it is chosen for.
const (
	ModelKindLLM       = "llm"
	ModelKindSystemOne = "system_one"
)

// systemOneFamilies are the names System One models go by. Providers list
// them among their other models with nothing to tell them apart but the ID
// (jev-1.13, jev-1.13-free, typesafe/jev-latest), so the ID is what decides.
var systemOneFamilies = []string{"jev"}

// IsSystemOneModel reports whether a model ID names a System One model.
func IsSystemOneModel(id string) bool {
	name := strings.ToLower(strings.TrimSpace(id))
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	for _, family := range systemOneFamilies {
		if name == family || strings.HasPrefix(name, family+"-") || strings.HasPrefix(name, family+"_") || strings.HasPrefix(name, family+".") {
			return true
		}
	}
	return false
}

// ModelKind is the kind of the model an ID names.
func ModelKind(id string) string {
	if IsSystemOneModel(id) {
		return ModelKindSystemOne
	}
	return ModelKindLLM
}

// IsModelKind reports whether a value is one of the two kinds.
func IsModelKind(kind string) bool {
	return kind == ModelKindLLM || kind == ModelKindSystemOne
}

// SplitModelCatalog divides a provider's model IDs by kind, keeping the order
// within each and dropping blanks and repeats.
func SplitModelCatalog(ids []string) (llm, systemOne []string) {
	seen := map[string]bool{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		if IsSystemOneModel(id) {
			systemOne = append(systemOne, id)
		} else {
			llm = append(llm, id)
		}
	}
	return llm, systemOne
}

// ParseModelList reads a stored comma-separated catalog.
func ParseModelList(list string) []string {
	var ids []string
	for _, id := range strings.Split(list, ",") {
		if id = strings.TrimSpace(id); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

// ModelsOfKind lists the provider's models of one kind.
func (provider LLMProvider) ModelsOfKind(kind string) []string {
	if kind == ModelKindSystemOne {
		return ParseModelList(provider.SystemOneModels)
	}
	return ParseModelList(provider.SupportedModels)
}

// SortCatalog puts every model the provider lists under its kind, whichever
// of the two lists it was written to, and keeps the default model pointing at
// something the provider has. It reports whether anything changed.
//
// The default is a language model when the provider has any, because that is
// what a default is used for; a provider that only serves System One models
// defaults to one of those.
func (provider *LLMProvider) SortCatalog() bool {
	llm, systemOne := SplitModelCatalog(append(ParseModelList(provider.SupportedModels), ParseModelList(provider.SystemOneModels)...))
	supported, classifiers := strings.Join(llm, ","), strings.Join(systemOne, ",")
	fallback := ""
	switch {
	case len(llm) > 0:
		fallback = llm[0]
	case len(systemOne) > 0:
		fallback = systemOne[0]
	}
	defaultModel := strings.TrimSpace(provider.DefaultModel)
	switch {
	case defaultModel == "":
		defaultModel = fallback
	case IsSystemOneModel(defaultModel) && len(llm) > 0:
		defaultModel = llm[0]
	case IsSystemOneModel(defaultModel) && !slices.Contains(systemOne, defaultModel) && len(systemOne) > 0:
		defaultModel = systemOne[0]
	}
	changed := supported != provider.SupportedModels || classifiers != provider.SystemOneModels || defaultModel != provider.DefaultModel
	provider.SupportedModels, provider.SystemOneModels, provider.DefaultModel = supported, classifiers, defaultModel
	return changed
}
