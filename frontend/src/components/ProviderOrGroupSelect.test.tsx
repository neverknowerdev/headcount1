import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { MemoryRouter } from 'react-router-dom';
import { ProviderOrGroupSelect } from './ProviderOrGroupSelect';
import type { ProviderOrGroupValue } from './ProviderOrGroupSelect';
import { groupKind, modelsOfKind } from '../lib/modelKinds';
import type { ModelKind } from '../lib/modelKinds';

afterEach(cleanup);

// A provider may serve language models and System One models side by side;
// a group holds one kind. What a picker offers depends on what is being chosen.
const providers = [
    { id: 1, name: 'OpenCode Zen', default_model: 'big-pickle', supported_models: 'big-pickle,deepseek-v4-flash', system_one_models: 'jev-1.13,jev-1.13-free' },
    { id: 2, name: 'TypeSafe', default_model: 'jev-latest', supported_models: '', system_one_models: 'jev-latest' },
    { id: 3, name: 'DeepSeek', default_model: 'deepseek-chat', supported_models: 'deepseek-chat', system_one_models: '' },
];
const groups = [
    { id: 10, name: 'Writers', kind: 'llm', members: [{ model: 'big-pickle' }] },
    { id: 11, name: 'Classifiers', kind: 'system_one', members: [{ model: 'jev-1.13-free' }, { all_models: true }] },
    { id: 12, name: 'From before kinds', members: [] },
];

const renderSelect = (kind: ModelKind | undefined, value: ProviderOrGroupValue, onChange = vi.fn()) => {
    render(
        <MemoryRouter>
            <ProviderOrGroupSelect label="Model" kind={kind} providers={providers} modelGroups={groups} value={value} onChange={onChange} />
        </MemoryRouter>,
    );
    return onChange;
};
const empty = { provider_id: '', model_group_id: '', model: '' };
const optionTexts = () => Array.from(screen.getAllByRole('combobox')[0].querySelectorAll('option')).map(option => option.textContent);

describe('ProviderOrGroupSelect', () => {
    it('offers language models and their groups unless told otherwise', () => {
        renderSelect(undefined, empty);
        expect(optionTexts()).toEqual(['-- Select Provider or Group --', 'Writers', 'From before kinds', 'OpenCode Zen', 'DeepSeek']);
    });

    it('offers only System One models and groups of them for the classifier', () => {
        const onChange = renderSelect('system_one', empty);
        expect(optionTexts()).toEqual(['-- Select Provider or Group --', 'Classifiers', 'OpenCode Zen', 'TypeSafe']);

        // Choosing a provider takes its free System One model, not its default
        // model, which is a language model.
        fireEvent.change(screen.getAllByRole('combobox')[0], { target: { value: 'provider:1' } });
        expect(onChange).toHaveBeenCalledWith({ provider_id: '1', model_group_id: '', model: 'jev-1.13-free' });
        fireEvent.change(screen.getAllByRole('combobox')[0], { target: { value: 'provider:2' } });
        expect(onChange).toHaveBeenLastCalledWith({ provider_id: '2', model_group_id: '', model: 'jev-latest' });
    });

    it('lists only the chosen provider\'s models of the kind', () => {
        renderSelect('system_one', { provider_id: '1', model_group_id: '', model: 'jev-1.13' });
        const models = Array.from(screen.getAllByRole('combobox')[1].querySelectorAll('option')).map(option => option.textContent);
        expect(models).toEqual(['-- Select Model --', 'jev-1.13', 'jev-1.13-free']);
        expect(screen.getByText('System One model')).toBeTruthy();
    });

    it('shows a chosen model that the provider no longer lists for what it is', () => {
        renderSelect('llm', { provider_id: '1', model_group_id: '', model: 'nemotron-3-ultra-free' });
        const models = Array.from(screen.getAllByRole('combobox')[1].querySelectorAll('option')).map(option => option.textContent);
        expect(models).toEqual(['-- Select Model --', 'nemotron-3-ultra-free (no longer offered)', 'big-pickle', 'deepseek-v4-flash']);
        expect(screen.getByTestId('model-no-longer-offered').textContent).toContain('Choose another model');
    });

    it('tells the kinds apart', () => {
        expect(modelsOfKind(providers[0], 'llm')).toEqual(['big-pickle', 'deepseek-v4-flash']);
        expect(modelsOfKind(providers[0], 'system_one')).toEqual(['jev-1.13', 'jev-1.13-free']);
        expect(modelsOfKind(undefined, 'system_one')).toEqual([]);
        expect(groupKind(groups[1])).toBe('system_one');
        expect(groupKind(groups[2])).toBe('llm');
    });
});
