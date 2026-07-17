import { test, expect } from '@playwright/test';

// Teams & members: the e2e fixture user (e2e@local) is auto-logged-in by the
// E2E auth bypass and owns its own team. This suite covers the Team page UI
// (member list, owner-only invite form, pending-invite lifecycle) and the
// invite-token registration flow joining a second user to the same team.
test.describe.serial('Team & members', () => {
    test.beforeAll(async ({ request }) => {
        await request.post('/api/e2e/wipe-db');
    });

    test('owner sees their team with themselves as the only member', async ({ page }) => {
        await page.goto('/team');
        await expect(page.getByRole('heading', { name: "e2e's team" })).toBeVisible({ timeout: 30_000 });

        const members = page.getByTestId('team-member');
        await expect(members).toHaveCount(1);
        await expect(members.first()).toContainText('e2e@local');
        await expect(members.first()).toContainText('owner');

        // The owner-only invite form is present.
        await expect(page.getByPlaceholder('teammate@company.com')).toBeVisible();
    });

    test('owner invites a teammate; pending invite is listed and revocable', async ({ page }) => {
        await page.goto('/team');
        await page.getByPlaceholder('teammate@company.com').fill('revoked@corp.io');
        await page.getByRole('button', { name: 'Invite' }).click();

        // The invite link is surfaced for out-of-band sharing.
        await expect(page.getByText('Invite sent — link:')).toBeVisible();
        const pending = page.getByTestId('pending-invite');
        await expect(pending).toHaveCount(1);
        await expect(pending.first()).toContainText('revoked@corp.io');

        // Revoking removes it from the list.
        await pending.first().getByTitle('Revoke invite').click();
        await expect(page.getByTestId('pending-invite')).toHaveCount(0);
    });

    test('registering through an invite link joins the team as member', async ({ page, playwright }) => {
        // Create the invite through the UI and capture the link.
        await page.goto('/team');
        await page.getByPlaceholder('teammate@company.com').fill('dev@corp.io');
        await page.getByRole('button', { name: 'Invite' }).click();
        const linkText = await page.getByText('Invite sent — link:').innerText();
        const token = linkText.match(/invite=([A-Za-z0-9_-]+)/)?.[1];
        expect(token).toBeTruthy();

        // Register the teammate in an isolated request context so the
        // browser's (auto-authenticated) session stays the owner's.
        const api = await playwright.request.newContext({ baseURL: 'http://localhost:8080' });
        const reg = await api.post('/api/auth/register', {
            data: { email: 'dev@corp.io', password: 'password-dev', invite_token: token },
        });
        expect(reg.status(), await reg.text()).toBe(201);

        // The teammate sees the same team, as member — verified through
        // their own authenticated context.
        const teamRes = await api.get('/api/team');
        expect(teamRes.status()).toBe(200);
        const team = await teamRes.json();
        expect(team.name).toBe("e2e's team");
        expect(team.role).toBe('member');
        expect(team.members).toHaveLength(2);
        expect(team.invites).toBeUndefined(); // members don't see invites

        // A member cannot invite — owner-only capability.
        const forbidden = await api.post('/api/team/invites', { data: { email: 'x@corp.io' } });
        expect(forbidden.status()).toBe(403);
        await api.dispose();

        // The owner's Team page now shows both members and no pending invites.
        await page.reload();
        await expect(page.getByTestId('team-member')).toHaveCount(2);
        await expect(page.getByTestId('team-member').nth(1)).toContainText('dev@corp.io');
        await expect(page.getByTestId('team-member').nth(1)).toContainText('member');
        await expect(page.getByTestId('pending-invite')).toHaveCount(0);
    });
});
