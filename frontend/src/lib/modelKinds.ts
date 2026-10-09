// A model is a language model, which writes, or a System One model (Jev),
// which only decides. A provider may serve both and lists them apart; a
// model group holds one kind. Wherever a model is chosen, the kind is fixed
// by what it is chosen for.
export type ModelKind = 'llm' | 'system_one';

interface ProviderCatalogs {
    supported_models?: string;
    system_one_models?: string;
}

// modelsOfKind lists a provider's models of one kind.
export const modelsOfKind = (provider: ProviderCatalogs | null | undefined, kind: ModelKind): string[] =>
    ((kind === 'system_one' ? provider?.system_one_models : provider?.supported_models) || '')
        .split(',').map(model => model.trim()).filter(model => model);

// groupKind is the kind of models a group routes between.
export const groupKind = (group: { kind?: string } | null | undefined): ModelKind =>
    (group?.kind === 'system_one' ? 'system_one' : 'llm');
