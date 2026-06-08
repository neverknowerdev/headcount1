import { execSync } from 'child_process';

/**
 * Asserts that the `opencode` CLI is installed and runnable. If missing, attempts
 * to install it via `npm install -g opencode-ai`. Throws with a clear message on
 * failure.
 */
export function assertOpenCodeInstalled(): void {
    let version: string;
    try {
        version = execSync('opencode --version', { stdio: 'pipe', encoding: 'utf8' }).trim();
    } catch {
        console.log('[e2e] opencode not found, attempting to install via npm...');
        try {
            execSync('npm install -g opencode-ai', { stdio: 'inherit' });
            version = execSync('opencode --version', { stdio: 'pipe', encoding: 'utf8' }).trim();
        } catch (installErr: any) {
            throw new Error(
                `E2E requires the 'opencode' CLI to be installed. ` +
                `Tried 'npm install -g opencode-ai' but it failed: ${installErr?.message || installErr}. ` +
                `Please install it manually (npm i -g opencode-ai) and ensure 'opencode --version' works.`,
            );
        }
    }
    console.log(`[e2e] opencode version: ${version}`);
}
