import { test as setup } from '@playwright/test';

setup('wipe database for clean test run', async ({ request }) => {
    await request.post('/api/e2e/wipe-db');
});
