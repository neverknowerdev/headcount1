import { test, expect } from '@playwright/test';
import fs from 'fs';
import path from 'path';
import os from 'os';
import { loadE2EEnv } from '../helpers/env';

test.describe('Agent Settings', () => {
  test('should allow modifying agent tools and save them to the global config', async ({ page, request }) => {
    const env = loadE2EEnv();

    // Create company using API
    const companyRes = await request.post('/api/companies', {
        data: { name: 'E2E Company', short_name: 'e2ecompany' }
    });
    const company = await companyRes.json();
    expect(companyRes.ok()).toBeTruthy();

    // Create Agent using API
    const agentRes = await request.post('/api/agents', {
        data: {
            company_id: company.id,
            name: 'e2e-agent',
            description: 'test agent',
            system_prompt: 'system prompt',
            mode: 'primary',
            permissions: '{}'
        }
    });
    const agent = await agentRes.json();
    expect(agentRes.ok()).toBeTruthy();

    // Go to Agent Details directly
    await page.goto(`/companies/e2ecompany/agents/${agent.id}`);

    // Go to tools tab
    await page.waitForSelector('button:has-text("tools")');
    await page.click('button:has-text("tools")');

    // Verify update_task_status is disabled but checked
    const updateTaskStatusCheckbox = await page.locator('input[id="tool-update_task_status"]');
    await expect(updateTaskStatusCheckbox).toBeChecked();
    await expect(updateTaskStatusCheckbox).toBeDisabled();

    // Disable bash tool
    const bashCheckbox = await page.locator('input[id="tool-bash"]');
    await bashCheckbox.uncheck();

    // Save changes
    await page.click('button:has-text("Save Changes")');

    // Wait for save logic to complete
    await page.waitForTimeout(2000);

    // Verify that the file was created in E2E_HOME/.config/opencode/agents/
    const agentFilePath = path.join(env.E2E_HOME, '.config', 'opencode', 'agents', 'e2e-agent.md');
    expect(fs.existsSync(agentFilePath)).toBe(true);

    const fileContent = fs.readFileSync(agentFilePath, 'utf8');
    expect(fileContent).toContain('bash: deny');
    expect(fileContent).toContain('update_task_status: allow');
    expect(fileContent).toContain('mode: primary');
  });
});
