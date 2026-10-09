import { test, expect } from '@playwright/test';
import { loadE2EEnv } from '../helpers/env';
import { resetE2E } from '../helpers/reset';
import { waitForTaskStatus } from '../helpers/wait-for';
import { CHEAP_MODEL, SMART_MODEL, call, createTask, decided, finishWork, getJSON, mockLog, setScenario, startTask } from '../helpers/workflow';

const env = loadE2EEnv();

// Jev, TypeSafe's System One model, is served by other providers too, beside
// their language models. A provider's System One models are recognised, kept
// apart, and offered only where a classifier is chosen; groups hold one kind
// and route between System One models as they do between language models.
test.describe('System One models', () => {
    test.beforeEach(async ({ request }) => {
        await resetE2E(request, env.E2E_MOCK_PROVIDER_URL);
    });

    test('a provider\'s Jev models become the classifier, alone or as a group', async ({ request, page }) => {
        // One provider that serves both kinds, as OpenCode Zen and AI Surplus do.
        const created = await request.post('/api/providers', {
            data: {
                name: 'Mixed provider', base_url: env.E2E_MOCK_PROVIDER_URL, api_key: 'test-key', provider_type: 'openai',
                default_model: SMART_MODEL, supported_models: `${SMART_MODEL},jev-e2e,${CHEAP_MODEL},jev-e2e-free`,
            },
        });
        expect(created.ok(), await created.text()).toBeTruthy();
        const provider = await created.json();
        expect(provider.supported_models, 'language models only').toBe(`${SMART_MODEL},${CHEAP_MODEL}`);
        expect(provider.system_one_models, 'the System One models, kept apart').toBe('jev-e2e,jev-e2e-free');
        expect(provider.default_model).toBe(SMART_MODEL);

        const slot = async (purpose: string) => (await getJSON(request, '/api/default-model-settings')).find((s: any) => s.purpose === purpose);
        // Connecting a provider that brings a System One model fills the empty
        // classifier slot, with the free one.
        const adopted = await slot('classifier');
        expect(adopted.provider_id).toBe(provider.id);
        expect(adopted.model).toBe('jev-e2e-free');

        const companyRes = await request.post('/api/companies', { data: { name: 'Jev Co', short_name: 'jev-co', provider_id: provider.id, model: SMART_MODEL } });
        expect(companyRes.ok(), await companyRes.text()).toBeTruthy();
        const company = await companyRes.json();
        const put = (purpose: string, data: Record<string, unknown>) => request.put(`/api/default-model-settings/${purpose}`, { data });
        expect((await put('smart', { provider_id: provider.id, model: SMART_MODEL })).ok()).toBeTruthy();
        expect((await put('cheap', { provider_id: provider.id, model: CHEAP_MODEL })).ok()).toBeTruthy();

        // A System One model is not a language model and the other way round.
        expect((await put('cheap', { provider_id: provider.id, model: 'jev-e2e' })).status()).toBe(400);
        expect((await put('classifier', { provider_id: provider.id, model: CHEAP_MODEL })).status()).toBe(400);
        const asTaskModel = await request.post('/api/tasks', { data: { company_id: company.id, title: 'Runs on Jev?', provider_id: provider.id, model: 'jev-e2e' } });
        expect(asTaskModel.ok(), 'a task cannot run on a System One model').toBeFalsy();

        // Groups hold one kind.
        const mixed = await request.post('/api/model-groups', {
            data: { name: 'Mixed', members: [{ provider_id: provider.id, model: CHEAP_MODEL }, { provider_id: provider.id, model: 'jev-e2e' }] },
        });
        expect(mixed.status()).toBe(400);
        expect((await mixed.json()).error).toContain('jev-e2e is a System One model and cannot be in a group of language models');
        const groupRes = await request.post('/api/model-groups', {
            data: {
                name: 'Classifiers', kind: 'system_one',
                members: [{ provider_id: provider.id, model: 'jev-e2e' }, { provider_id: provider.id, model: 'jev-e2e-free', is_free: true }],
            },
        });
        expect(groupRes.ok(), await groupRes.text()).toBeTruthy();
        const group = await groupRes.json();
        expect(group.kind).toBe('system_one');
        expect((await put('smart', { model_group_id: group.id })).status(), 'a System One group fits no language slot').toBe(400);
        expect((await put('classifier', { model_group_id: group.id })).ok()).toBeTruthy();

        // The free member is rate limited: the group falls over to the paid
        // one, as a group of language models would.
        const failing = await fetch(`${env.E2E_MOCK_PROVIDER_URL}/__test/systemone-failure`, {
            method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ model: 'jev-e2e-free', status: 429 }),
        });
        expect(failing.ok).toBeTruthy();

        const sprint = await (await request.post('/api/sprints', { data: { company_id: company.id, name: 'Sprint 1' } })).json();
        const task = await createTask(request, { companyId: company.id, shortName: 'jev-co', providerId: provider.id, sprintId: sprint.id },
            { title: 'Find the entry point', description: 'Say where the program starts.', task_type: 'research' });
        await setScenario([
            { match: { phase: 'plan' }, replies: [
                call('create_tasks', {
                    tasks: [{ key: 'look', title: 'Look for main', instructions: 'Find the entry point.', done_when: 'It is named.', type: 'research' }],
                    decisions: decided('One look is enough'),
                }),
            ] },
            { match: { phase: 'executor', contains: 'Look for main' }, replies: [
                call('ls', { path: '.' }),
                call('ls', { path: 'cmd' }),
                finishWork('done', 'It starts in main.go.'),
            ] },
        ]);
        await startTask(request, task.id);
        await waitForTaskStatus(request, task.id, 'in-review', 120_000);

        // The classifier was asked through the group: the free model first,
        // then the paid one, each at the provider's systemone endpoint.
        const asked = (await mockLog()).systemOne;
        expect(asked.length).toBeGreaterThanOrEqual(2);
        expect(asked[0].path).toBe('/v1/systemone');
        expect((asked[0].body as any).model).toBe('jev-e2e-free');
        expect(asked.some((entry) => (entry.body as any).model === 'jev-e2e')).toBeTruthy();
        expect(Object.keys((asked[0].body as any).questions)).toContain('looping');
        for (const entry of asked) {
            expect(entry.session).toMatch(/^hc1-[0-9a-f]{32}$/);
            expect(entry.userAgent).toBe('headcount1');
        }
        // No language model was sent a classifier question, nor Jev a chat.
        for (const entry of (await mockLog()).completions) expect((entry.body as any).model).not.toContain('jev');

        // The calls are in the ledger under the classifier tier, by the model that answered.
        const usage = await getJSON(request, `/api/usage?company_id=${company.id}&group_by=tier,model`);
        const classifierTier = usage.groups.tier.find((row: any) => row.key === 'classifier');
        expect(classifierTier.calls).toBeGreaterThanOrEqual(1);
        expect(usage.groups.model.map((row: any) => row.key)).toContain('jev-e2e');
        // And the group counted each attempt, the refused ones as rate limited.
        const stats = await getJSON(request, `/api/model-groups/${group.id}/stats`);
        const paid = stats.members.find((member: any) => member.model === 'jev-e2e');
        const free = stats.members.find((member: any) => member.model === 'jev-e2e-free');
        expect(paid.window_5h.requests).toBeGreaterThanOrEqual(1);
        expect(free.window_5h.rate_limited).toBeGreaterThanOrEqual(1);

        // ── The same in the app ──
        await page.goto('/companies/jev-co/providers');
        await expect(page.getByTestId('provider-system-one-models')).toContainText('jev-e2e, jev-e2e-free');
        await expect(page.getByTestId('group-kind')).toHaveText('System One');
        await expect(page.getByTestId('classifier-warning')).toHaveCount(0);

        // The classifier slot offers the System One group and the provider's
        // System One models; the cheap slot offers neither.
        const classifierSlot = page.getByTestId('model-slot-classifier');
        const classifierChoice = classifierSlot.getByRole('combobox').first();
        await expect(classifierChoice.locator('option')).toHaveText(['Not used', 'Classifiers', 'Mixed provider']);
        await classifierChoice.selectOption({ label: 'Mixed provider' });
        await expect(classifierSlot.getByRole('combobox').nth(1).locator('option')).toHaveText(['-- Select Model --', 'jev-e2e', 'jev-e2e-free']);
        await expect(classifierSlot.getByRole('combobox').nth(1)).toHaveValue('jev-e2e-free');

        const cheapSlot = page.getByTestId('model-slot-cheap');
        const cheapChoices = await cheapSlot.getByRole('combobox').first().locator('option').allTextContents();
        expect(cheapChoices).toContain('Mixed provider');
        expect(cheapChoices, 'a System One group is not offered for a language model slot').not.toContain('Classifiers');
        await expect(cheapSlot.getByRole('combobox').nth(1).locator('option')).toHaveText(['-- Select Model --', SMART_MODEL, CHEAP_MODEL]);

        // Making a group: the kind is chosen first, and only models of that
        // kind are then offered.
        await page.getByRole('button', { name: 'Add Group' }).click();
        await page.getByRole('button', { name: 'System One (classifiers)' }).click();
        const dialog = page.locator('.fixed').filter({ hasText: 'Add Model Group' });
        await dialog.locator('select').first().selectOption({ label: 'Mixed provider' });
        await expect(dialog.locator('select').nth(1).locator('option')).toHaveText(['Model…', 'Any System One model of this provider', 'jev-e2e', 'jev-e2e-free']);
    });
});
