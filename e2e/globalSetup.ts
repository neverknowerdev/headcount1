import { FullConfig, request } from '@playwright/test';

async function globalSetup(config: FullConfig) {
    const baseURL = config.projects[0]?.use?.baseURL || 'http://localhost:8080';
    const requestContext = await request.newContext({ baseURL });
    await requestContext.post('/api/e2e/wipe-db');
    await requestContext.dispose();
}

export default globalSetup;
