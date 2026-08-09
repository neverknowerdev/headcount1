import { test, expect } from '@playwright/test';

test('deployment source can be saved and survives a reload', async ({ page, request }) => {
    const providerRes = await request.post('/api/providers', {
        data: {
            name: 'Deployment UI Provider',
            base_url: 'http://127.0.0.1:1',
            api_key: 'e2e-test-key',
            provider_type: 'openai',
            default_model: 'e2e-model',
            supported_models: 'e2e-model',
        },
    });
    expect(providerRes.ok()).toBeTruthy();
    const provider = await providerRes.json();

    const companyRes = await request.post('/api/companies', {
        data: { name: 'Deployment UI Company', short_name: 'deploy-ui', color: '#4f46e5' },
    });
    expect(companyRes.ok()).toBeTruthy();
    const company = await companyRes.json();

    const agentRes = await request.post('/api/agents', {
        data: {
            company_id: company.id,
            name: 'Deployment UI Agent',
            description: 'E2E test agent',
            system_prompt: 'You are an E2E test agent.',
            model: 'e2e-model',
            provider_id: provider.id,
        },
    });
    expect(agentRes.ok()).toBeTruthy();

    await expect.poll(async () => {
        const setupRes = await request.get('/api/setup-status');
        const setup = await setupRes.json();
        return setup.ok === true;
    }, { timeout: 120_000 }).toBe(true);

    await page.goto('/companies/deploy-ui/settings');
    await expect(page.getByRole('heading', { name: 'Company Settings' })).toBeVisible();

    const deploySource = page.locator('select').last();
    await expect(deploySource).toHaveValue('releases');
    await deploySource.selectOption('main');
    await page.getByRole('button', { name: 'Save Deployment Settings' }).click();
    await expect(page.getByRole('button', { name: 'Save Deployment Settings' })).toBeVisible();

    await page.reload();
    await expect(page.locator('select').last()).toHaveValue('main');

    const settingsRes = await request.get('/api/settings');
    expect(settingsRes.ok()).toBeTruthy();
    const settings = await settingsRes.json();
    expect(settings.deploy_source).toBe('main');
});
