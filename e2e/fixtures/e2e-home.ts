import * as fs from 'fs';
import * as os from 'os';
import * as path from 'path';

/**
 * Creates a temporary home directory for E2E tests to avoid
 * interfering with the user's local paperclip2 setup.
 *
 * Returns the path to the temporary home directory.
 */
export function createE2EHome(): string {
    const e2eHome = path.join(os.tmpdir(), `paperclip-e2e-home-${Date.now()}`);
    fs.mkdirSync(e2eHome, { recursive: true });

    // Create necessary subdirectories
    const dirs = [
        path.join(e2eHome, '.paperclip2', 'data'),
        path.join(e2eHome, '.paperclip2', 'companies'),
        path.join(e2eHome, '.config', 'opencode', 'agents'),
        path.join(e2eHome, '.config', 'opencode', 'tools'),
    ];
    for (const dir of dirs) {
        fs.mkdirSync(dir, { recursive: true });
    }

    return e2eHome;
}

/**
 * Returns the paperclip2 base path within the given home directory.
 */
export function getPaperclipBase(homeDir: string): string {
    return path.join(homeDir, '.paperclip2');
}

/**
 * Cleans up the E2E home directory.
 */
export function cleanupE2EHome(homeDir: string): void {
    if (fs.existsSync(homeDir)) {
        fs.rmSync(homeDir, { recursive: true, force: true });
    }
}
