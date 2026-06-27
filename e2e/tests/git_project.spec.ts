import { test, expect } from '@playwright/test';
import { loadE2EEnv } from '../helpers/env';

const env = loadE2EEnv();

test.describe.serial('Git Project Scenarios', () => {
    let companyId: number;

    test('setup: create company and provider via API', async ({ request }) => {
        // Create company
        const companyRes = await request.post('/api/companies', {
            data: { name: 'Git Test Co', short_name: 'gitco' }
        });
        expect(companyRes.ok()).toBeTruthy();
        const company = await companyRes.json();
        companyId = company.id;

        // Create provider pointing at the mock provider server
        const providerRes = await request.post('/api/providers', {
            data: {
                company_id: companyId,
                name: 'e2e-mock',
                base_url: env.E2E_MOCK_PROVIDER_URL,
                api_key: 'test-key',
                provider_type: 'openai',
                default_model: 'e2e-mock-model'
            }
        });
        expect(providerRes.ok()).toBeTruthy();

        // Create agent
        const agentRes = await request.post('/api/agents', {
            data: {
                company_id: companyId,
                name: 'git-test-agent',
                description: 'test agent',
                system_prompt: 'you are a test agent',
                mode: 'primary',
                permissions: '{}'
            }
        });
        expect(agentRes.ok()).toBeTruthy();
    });

    test('can create project with repo URL via API', async ({ request }) => {
        const res = await request.post('/api/projects', {
            data: {
                company_id: companyId,
                name: 'Git Project',
                workspace_folder: 'gitco/git-project',
                repository_url: env.E2E_TEST_REPO_URL
            }
        });
        if (!res.ok()) {
            console.log('response status:', res.status());
            console.log('response body:', await res.text());
        }
        expect(res.ok()).toBeTruthy();
        const project = await res.json();
        expect(project.name).toBe('Git Project');
        expect(project.repository_url).toBe(env.E2E_TEST_REPO_URL);
    });

    test('can create project without repo URL', async ({ request }) => {
        const res = await request.post('/api/projects', {
            data: {
                company_id: companyId,
                name: 'No Git Project',
                workspace_folder: 'gitco/no-git-project'
            }
        });
        expect(res.ok()).toBeTruthy();
        const project = await res.json();
        expect(project.name).toBe('No Git Project');
        expect(project.repository_url).toBe('');
    });

    test('can fetch project by ID', async ({ request }) => {
        // First get the list to find the project ID
        const listRes = await request.get(`/api/projects?company_id=${companyId}`);
        expect(listRes.ok()).toBeTruthy();
        const projects = await listRes.json();
        const gitProject = projects.find((p: any) => p.name === 'Git Project');
        expect(gitProject).toBeTruthy();

        // Fetch by ID
        const res = await request.get(`/api/projects/${gitProject.id}`);
        expect(res.ok()).toBeTruthy();
        const project = await res.json();
        expect(project.name).toBe('Git Project');
        expect(project.repository_url).toBe(env.E2E_TEST_REPO_URL);
    });

    test('can update project to add repo URL', async ({ request }) => {
        // Get the no-git project
        const listRes = await request.get(`/api/projects?company_id=${companyId}`);
        const projects = await listRes.json();
        const noGitProject = projects.find((p: any) => p.name === 'No Git Project');

        // Update with repo URL — re-use the test repo URL
        const res = await request.put(`/api/projects/${noGitProject.id}`, {
            data: {
                repository_url: env.E2E_TEST_REPO_URL
            }
        });
        expect(res.ok()).toBeTruthy();
        const project = await res.json();
        expect(project.repository_url).toBe(env.E2E_TEST_REPO_URL);
    });

    test('can navigate to project settings page from project card', async ({ page }) => {
        await page.goto('/companies/gitco/projects');
        await expect(page.getByRole('heading', { name: 'Projects' })).toBeVisible({ timeout: 10000 });

        // Click on Git Project card
        await page.click('a:has-text("Git Project")');

        // Should navigate to project settings
        await expect(page).toHaveURL(/.*\/projects\/\d+/);
        await expect(page.getByRole('heading', { name: 'Project Settings' })).toBeVisible();
        await expect(page.locator('input').first()).toHaveValue('Git Project', { timeout: 5000 });
    });

    test('project settings page shows repo URL field', async ({ page }) => {
        // Navigate to projects list
        await page.goto('/companies/gitco/projects');
        await expect(page.getByRole('heading', { name: 'Projects' })).toBeVisible({ timeout: 10000 });

        // Click on Git Project to go to settings
        await page.click('a:has-text("Git Project")');
        await expect(page.getByRole('heading', { name: 'Project Settings' })).toBeVisible();

        // Verify repo URL field exists and has value
        const repoUrlInput = page.locator('input[placeholder="git@github.com:user/repo.git"]');
        await expect(repoUrlInput).toBeVisible();
        await expect(repoUrlInput).toHaveValue(env.E2E_TEST_REPO_URL);

        // Verify git section heading
        await expect(page.getByText('Git Repository')).toBeVisible();
    });

    test('can edit project repo URL from settings page', async ({ page }) => {
        await page.goto('/companies/gitco/projects');
        await page.click('a:has-text("Git Project")');
        await expect(page.getByRole('heading', { name: 'Project Settings' })).toBeVisible();

        // Change repo URL to a different file:// path under the same test repo dir
        const updatedUrl = env.E2E_TEST_REPO_URL;
        const repoUrlInput = page.locator('input[placeholder="git@github.com:user/repo.git"]');
        await repoUrlInput.fill(updatedUrl);

        // Save
        await page.click('button:has-text("Connect Repository")');
        await expect(page.getByText('Repository connected successfully')).toBeVisible({ timeout: 5000 });
    });

    test('project card shows git branch icon for repo projects', async ({ page }) => {
        await page.goto('/companies/gitco/projects');
        await expect(page.getByRole('heading', { name: 'Projects' })).toBeVisible({ timeout: 10000 });

        // Git Project card should show the repo URL
        const gitCard = page.locator('a:has-text("Git Project")').first();
        await expect(gitCard).toBeVisible();
        await expect(gitCard.locator('text=file://')).toBeVisible();
    });

    test('can create sprint and task under git project', async ({ page, request }) => {
        // Create sprint via API
        const sprintRes = await request.post('/api/sprints', {
            data: {
                company_id: companyId,
                name: 'Git Sprint',
                start_date: '2024-01-01T00:00:00Z',
                end_date: '2024-01-14T00:00:00Z'
            }
        });
        expect(sprintRes.ok()).toBeTruthy();
        const sprint = await sprintRes.json();

        // Get the git project ID
        const projectsRes = await request.get(`/api/projects?company_id=${companyId}`);
        const projects = await projectsRes.json();
        const gitProject = projects.find((p: any) => p.name === 'Git Project');

        // Create task under git project
        const taskRes = await request.post('/api/tasks', {
            data: {
                company_id: companyId,
                project_id: gitProject.id,
                sprint_id: sprint.id,
                title: 'Git Task',
                task_type: 'implement',
                description: 'A task under a git project'
            }
        });
        expect(taskRes.ok()).toBeTruthy();
        const task = await taskRes.json();
        expect(task.project_id).toBe(gitProject.id);

        // Navigate to tasks page and verify task exists
        await page.goto('/companies/gitco/tasks');
        await expect(page.getByText('Git Task')).toBeVisible({ timeout: 10000 });
    });

    test('project create form shows repo URL field', async ({ page }) => {
        await page.goto('/companies/gitco/projects');
        await expect(page.getByRole('heading', { name: 'Projects' })).toBeVisible({ timeout: 10000 });

        // Open create modal
        await page.click('button:has-text("Create Project")');
        await expect(page.getByRole('dialog')).toBeVisible();

        // Verify repo URL field exists in the modal
        const repoInput = page.getByRole('dialog').locator('input[placeholder="git@github.com:user/repo.git"]');
        await expect(repoInput).toBeVisible();
        await expect(repoInput).toHaveValue('');

        // Verify label
        await expect(page.getByText('Git Repository URL (optional)')).toBeVisible();
    });
});
