import { test, expect, APIRequestContext, Page } from '@playwright/test';
import * as fs from 'fs';
import * as path from 'path';
import { resetE2E } from '../helpers/reset';

type Company = { id: number; short_name: string };
type Task = { id: number; title: string; ref_key: string; parent_id?: number | null };

const names = {
    company: 'hierarchy-e2e',
    secondCompany: 'hierarchy-other',
    project: 'Hierarchy UX Project',
    root: 'Plan the task workspace redesign',
    child: 'Build the hierarchical task view',
    grandchild: 'Verify ancestor context in search',
    related: 'Document task relationships',
    otherRoot: 'Other company task view fixture',
};

let company: Company;
let otherCompany: Company;
let projectID: number;
let sprintID: number;
let otherSprintID: number;
let agentID: number;
let root: Task;
let child: Task;
let grandchild: Task;
let related: Task;
let otherRoot: Task;

test.describe.serial('Task views', () => {
    test.beforeAll(async ({ request }) => {
        await resetE2E(request);

        company = await createCompany(request, names.company, 'Hierarchy E2E');
        otherCompany = await createCompany(request, names.secondCompany, 'Other Hierarchy E2E');
        const projectResponse = await request.post('/api/projects', {
            data: {
                company_id: company.id,
                name: names.project,
                workspace_folder: `${names.company}/hierarchy-ux`,
            },
        });
        expect(projectResponse.ok(), await projectResponse.text()).toBeTruthy();
        projectID = (await projectResponse.json()).id;

        const sprintResponse = await request.post('/api/sprints', {
            data: { company_id: company.id, name: 'Hierarchy UX Sprint', goal: 'Exercise visible task metadata' },
        });
        expect(sprintResponse.ok(), await sprintResponse.text()).toBeTruthy();
        sprintID = (await sprintResponse.json()).id;
        const otherSprintResponse = await request.post('/api/sprints', {
            data: { company_id: otherCompany.id, name: 'Other Company Sprint', goal: 'Company-scoped fixture' },
        });
        expect(otherSprintResponse.ok(), await otherSprintResponse.text()).toBeTruthy();
        otherSprintID = (await otherSprintResponse.json()).id;

        const agentResponse = await request.post('/api/agents', {
            data: { company_id: company.id, name: 'Researcher', role_key: 'researcher', short_name: 'researcher' },
        });
        expect(agentResponse.ok(), await agentResponse.text()).toBeTruthy();
        agentID = (await agentResponse.json()).id;

        root = await createTask(request, company.id, names.root, { sprint_id: sprintID });
        child = await createTask(request, company.id, names.child, {
            parent_id: root.id, project_id: projectID, sprint_id: sprintID, agent_id: agentID, priority: 'High',
        });
        grandchild = await createTask(request, company.id, names.grandchild, {
            parent_id: child.id, project_id: projectID, sprint_id: sprintID, due_date: '2026-10-20T00:00:00Z',
        });
        related = await createTask(request, company.id, names.related, { sprint_id: sprintID });
        otherRoot = await createTask(request, otherCompany.id, names.otherRoot, { sprint_id: otherSprintID });

        const doneResponse = await request.put(`/api/tasks/${related.id}`, { data: { status: 'done' } });
        expect(doneResponse.ok(), await doneResponse.text()).toBeTruthy();

        const relation = await request.post(`/api/tasks/${root.id}/relations`, {
            data: { type: 'related_to', task_id: related.id },
        });
        expect(relation.ok(), await relation.text()).toBeTruthy();
    });

    test('keeps the board as the default and renders, collapses, and navigates a three-level hierarchy', async ({ page, request }) => {
        await page.setViewportSize({ width: 1680, height: 1000 });
        const websocketConnected = page.waitForEvent('websocket', { timeout: 10_000 });
        const hierarchyRequest = page.waitForRequest(request =>
            request.url().includes(`/api/tasks?company_id=${company.id}`) &&
            new URL(request.url()).searchParams.get('include_subtasks') === 'true',
        );
        await page.goto(`/companies/${company.short_name}/tasks`);

        await expect(page.getByRole('button', { name: 'Board', exact: true })).toHaveAttribute('aria-pressed', 'true');
        await expect(page.getByRole('button', { name: 'Hierarchy', exact: true })).toHaveAttribute('aria-pressed', 'false');
        await expect(page.getByText(names.root, { exact: true })).toBeVisible();
        await expect(page.getByText(names.child, { exact: true })).toBeHidden();
        await screenshotIfRequested(page, 'task-board-default');
        const requestedTasks = await hierarchyRequest;
        expect(new URL(requestedTasks.url()).searchParams.get('include_subtasks')).toBe('true');

        // Board cards continue to support keyboard drag and drop. Move the
        // root from Backlog to Blocked (not an executable queue state) and
        // verify persistence without launching an agent run.
        const draggable = page.locator(`[data-rfd-draggable-id="${root.id}"]`);
        await draggable.focus();
        await draggable.press('Space');
        for (let index = 0; index < 4; index++) await page.keyboard.press('ArrowRight');
        await page.keyboard.press('Space');
        await expect.poll(async () => {
            const response = await request.get(`/api/tasks/${root.id}`);
            return response.ok() ? (await response.json()).status : null;
        }, { message: 'keyboard drag should persist the task status' }).toBe('blocked');

        // Task updates are broadcast over the real websocket and should appear
        // on the active page without reloading it.
        const renamed = `${names.root} updated live`;
        const update = await request.put(`/api/tasks/${root.id}`, { data: { title: renamed } });
        expect(update.ok(), update.statusText()).toBeTruthy();
        await expect(page.getByText(renamed, { exact: true })).toBeVisible({ timeout: 10_000 });
        const restore = await request.put(`/api/tasks/${root.id}`, { data: { title: names.root } });
        expect(restore.ok(), restore.statusText()).toBeTruthy();
        await expect(page.getByText(names.root, { exact: true })).toBeVisible({ timeout: 10_000 });

        await page.getByRole('button', { name: 'Hierarchy', exact: true }).click();
        const tree = page.getByTestId('task-hierarchy');
        await expect(tree).toBeVisible();
        await expect(row(page, root)).toBeVisible();
        await expect(row(page, child)).toBeVisible();
        await expect(row(page, grandchild)).toBeVisible();
        await expect(row(page, child)).toHaveAttribute('data-depth', '1');
        await expect(row(page, grandchild)).toHaveAttribute('data-depth', '2');
        await websocketConnected;
        await expect(row(page, child)).toContainText('Researcher');
        await expect(row(page, child)).toContainText(names.project);
        await expect(row(page, related)).toContainText('Done');
        await screenshotIfRequested(page, 'task-hierarchy');

        // Relations are created through the live API, so the metadata column
        // proves the summary returned by the task-list endpoint is rendered.
        await expect(row(page, root).locator(`[title="Related to ${names.related}"]`)).toBeVisible();

        await page.getByRole('button', { name: `Collapse ${names.root}` }).click();
        await expect(row(page, child)).toBeHidden();
        await expect(row(page, grandchild)).toBeHidden();

        // A websocket refresh in hierarchy view must preserve the user's
        // expanded/collapsed state while refreshing task content.
        const renamedRelated = `${names.related} updated live`;
        const liveUpdate = await request.put(`/api/tasks/${related.id}`, { data: { title: renamedRelated } });
        expect(liveUpdate.ok(), liveUpdate.statusText()).toBeTruthy();
        await expect(row(page, related).getByRole('link', { name: renamedRelated })).toBeVisible({ timeout: 10_000 });
        await expect(row(page, child)).toBeHidden();
        const restoreRelated = await request.put(`/api/tasks/${related.id}`, { data: { title: names.related } });
        expect(restoreRelated.ok(), restoreRelated.statusText()).toBeTruthy();
        await expect(row(page, related).getByRole('link', { name: names.related })).toBeVisible({ timeout: 10_000 });
        await expect(row(page, child)).toBeHidden();

        await page.getByRole('button', { name: `Expand ${names.root}` }).click();
        await expect(row(page, grandchild)).toBeVisible();

        const taskTitle = row(page, grandchild).getByRole('link', { name: names.grandchild });
        await expect(taskTitle).toHaveAttribute('href', `/companies/${company.short_name}/tasks/${grandchild.id}`);
        await taskTitle.click();
        await expect(page).toHaveURL(new RegExp(`/companies/${company.short_name}/tasks/${grandchild.id}$`));
    });

    test('search keeps ancestor context and project filtering promotes an orphaned child to a root', async ({ page }) => {
        await page.goto(`/companies/${company.short_name}/tasks`);
        await page.getByRole('button', { name: 'Hierarchy', exact: true }).click();
        const search = page.getByPlaceholder('Search tasks...');
        await search.fill(names.grandchild);

        const tree = page.getByTestId('task-hierarchy');
        await expect(row(page, root)).toBeVisible();
        await expect(row(page, child)).toBeVisible();
        await expect(row(page, grandchild)).toBeVisible();
        await expect(row(page, related)).toBeHidden();

        await search.fill('');
        await page.getByRole('button', { name: 'Projects', exact: true }).click();
        await page.getByRole('checkbox', { name: names.project }).check();
        await expect(row(page, child)).toBeVisible();
        await expect(row(page, child)).toHaveAttribute('data-depth', '0');
        await expect(row(page, root)).toHaveCount(0);
        await expect(row(page, grandchild)).toHaveAttribute('data-depth', '1');
        await expect(tree).toBeVisible();
    });

    test('persists view and display columns by company and remains usable on mobile', async ({ page }) => {
        await page.goto(`/companies/${company.short_name}/tasks`);
        await page.getByRole('button', { name: 'Hierarchy', exact: true }).click();
        const settingsButton = page.getByRole('button', { name: 'Display settings', exact: true });
        await settingsButton.click();
        const settings = page.getByRole('dialog', { name: 'Display settings' });
        await expect(settings.getByRole('checkbox', { name: 'Status' })).toBeChecked();
        await expect(settings.getByRole('checkbox', { name: 'Sprint' })).not.toBeChecked();
        await expect(settings.getByRole('checkbox', { name: 'Priority' })).not.toBeChecked();
        await expect(settings.getByRole('checkbox', { name: 'Due date' })).not.toBeChecked();
        await settings.getByRole('checkbox', { name: 'Status' }).uncheck();
        await settings.getByRole('checkbox', { name: 'Project' }).uncheck();
        await settings.getByRole('checkbox', { name: 'Sprint' }).check();
        await settings.getByRole('checkbox', { name: 'Priority' }).check();
        await settings.getByRole('checkbox', { name: 'Due date' }).check();
        await expect(page.getByRole('columnheader', { name: 'Task', exact: true })).toBeVisible();
        await expect(page.getByRole('columnheader', { name: 'Sprint', exact: true })).toBeVisible();
        await expect(page.getByRole('columnheader', { name: 'Priority', exact: true })).toBeVisible();
        await expect(page.getByRole('columnheader', { name: 'Due date', exact: true })).toBeVisible();
        await expect(page.getByRole('columnheader', { name: 'Status', exact: true })).toHaveCount(0);
        await expect(page.getByRole('columnheader', { name: 'Project', exact: true })).toHaveCount(0);
        await expect(row(page, child)).toContainText('Hierarchy UX Sprint');
        await expect(row(page, child)).toContainText('High');
        await expect(row(page, grandchild)).toContainText('Oct 20');
        await screenshotIfRequested(page, 'task-hierarchy-display-settings');
        await page.keyboard.press('Escape');

        await page.reload();
        await expect(page.getByRole('button', { name: 'Hierarchy', exact: true })).toHaveAttribute('aria-pressed', 'true');
        await settingsButton.click();
        const persistedSettings = page.getByRole('dialog', { name: 'Display settings' });
        await expect(persistedSettings.getByRole('checkbox', { name: 'Status' })).not.toBeChecked();
        await expect(persistedSettings.getByRole('checkbox', { name: 'Project' })).not.toBeChecked();
        await expect(persistedSettings.getByRole('checkbox', { name: 'Sprint' })).toBeChecked();
        await expect(persistedSettings.getByRole('checkbox', { name: 'Priority' })).toBeChecked();
        await expect(persistedSettings.getByRole('checkbox', { name: 'Due date' })).toBeChecked();
        await page.keyboard.press('Escape');
        await expect(page.getByRole('columnheader', { name: 'Task', exact: true })).toBeVisible();
        await expect(page.getByRole('columnheader', { name: 'Sprint', exact: true })).toBeVisible();
        await expect(page.getByRole('columnheader', { name: 'Priority', exact: true })).toBeVisible();
        await expect(page.getByRole('columnheader', { name: 'Due date', exact: true })).toBeVisible();
        await expect(page.getByRole('columnheader', { name: 'Status', exact: true })).toHaveCount(0);
        await expect(page.getByRole('columnheader', { name: 'Project', exact: true })).toHaveCount(0);
        await expect(row(page, grandchild)).toContainText('Oct 20');

        // Preferences are scoped to a company: the second company starts with
        // its default board and column choices.
        await page.getByTitle('Other Hierarchy E2E', { exact: true }).click();
        await page.waitForURL(`**/companies/${otherCompany.short_name}`);
        await page.getByRole('link', { name: 'Tasks', exact: true }).click();
        await page.waitForURL(`**/companies/${otherCompany.short_name}/tasks`);
        await expect(page.getByRole('button', { name: 'Board', exact: true })).toHaveAttribute('aria-pressed', 'true');
        await expect(page.getByRole('button', { name: 'Hierarchy', exact: true })).toHaveAttribute('aria-pressed', 'false');
        await expect(page.getByText(names.otherRoot, { exact: true })).toBeVisible();

        await page.getByTitle('Hierarchy E2E', { exact: true }).click();
        await page.waitForURL(`**/companies/${company.short_name}`);
        await page.getByRole('link', { name: 'Tasks', exact: true }).click();
        await page.waitForURL(`**/companies/${company.short_name}/tasks`);
        await expect(page.getByRole('button', { name: 'Hierarchy', exact: true })).toHaveAttribute('aria-pressed', 'true');
        await expect(page.getByRole('columnheader', { name: 'Sprint', exact: true })).toBeVisible();
        await expect(page.getByRole('columnheader', { name: 'Status', exact: true })).toHaveCount(0);

        await page.setViewportSize({ width: 390, height: 844 });
        await expect(page.getByTestId('task-hierarchy')).toBeVisible();
        await expect(page.getByRole('button', { name: 'Display settings', exact: true })).toBeVisible();
        await expect(row(page, root)).toBeVisible();
        const mobileButtons = await page.getByRole('button', { name: 'New Task', exact: true }).evaluateAll(buttons =>
            buttons.map(button => {
                const rect = button.getBoundingClientRect();
                return { left: rect.left, right: rect.right };
            }),
        );
        expect(mobileButtons.length).toBeGreaterThan(0);
        for (const bounds of mobileButtons) {
            expect(bounds.left).toBeGreaterThanOrEqual(0);
            expect(bounds.right).toBeLessThanOrEqual(390);
        }
        const displaySettingsButton = page.getByRole('button', { name: 'Display settings', exact: true });
        const displaySettingsBounds = await displaySettingsButton.boundingBox();
        expect(displaySettingsBounds).not.toBeNull();
        expect(displaySettingsBounds!.x).toBeGreaterThanOrEqual(0);
        expect(displaySettingsBounds!.x + displaySettingsBounds!.width).toBeLessThanOrEqual(390);
        expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
        await screenshotIfRequested(page, 'task-hierarchy-mobile');
        await displaySettingsButton.click();
        const mobileSettings = page.getByRole('dialog', { name: 'Display settings' });
        await expect(mobileSettings).toBeVisible();
        const mobileSettingsBounds = await mobileSettings.boundingBox();
        expect(mobileSettingsBounds).not.toBeNull();
        expect(mobileSettingsBounds!.x).toBeGreaterThanOrEqual(0);
        expect(mobileSettingsBounds!.x + mobileSettingsBounds!.width).toBeLessThanOrEqual(390);
        expect(mobileSettingsBounds!.y).toBeGreaterThanOrEqual(0);
        expect(mobileSettingsBounds!.y + mobileSettingsBounds!.height).toBeLessThanOrEqual(844);
        await screenshotIfRequested(page, 'task-hierarchy-mobile-display-settings');
    });
});

function row(page: Page, task: Task) {
    return page.getByTestId(`task-row-${task.id}`);
}

async function createCompany(request: APIRequestContext, shortName: string, name: string): Promise<Company> {
    const response = await request.post('/api/companies', { data: { short_name: shortName, name } });
    expect(response.ok(), await response.text()).toBeTruthy();
    return response.json();
}

async function createTask(
    request: APIRequestContext,
    companyID: number,
    title: string,
    extra: { parent_id?: number; project_id?: number; sprint_id?: number; agent_id?: number; priority?: string; due_date?: string } = {},
): Promise<Task> {
    const response = await request.post('/api/tasks', {
        data: { company_id: companyID, title, priority: 'Normal', ...extra },
    });
    expect(response.ok(), await response.text()).toBeTruthy();
    return response.json();
}

async function screenshotIfRequested(page: Page, name: string): Promise<void> {
    const directory = process.env.E2E_TASK_SCREENSHOT_DIR;
    if (!directory) return;
    fs.mkdirSync(directory, { recursive: true });
    await page.screenshot({ path: path.join(directory, `${name}.png`), fullPage: true });
}
