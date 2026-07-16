import * as fs from 'fs';
import * as os from 'os';
import * as path from 'path';

/**
 * Creates a temporary home directory for E2E tests to avoid
 * interfering with the user's local headcount1 setup.
 *
 * Returns the path to the temporary home directory.
 */
export function createE2EHome(): string {
    const e2eHome = path.join(os.tmpdir(), `headcount1-e2e-home-${Date.now()}`);
    fs.mkdirSync(e2eHome, { recursive: true });

    // Create necessary subdirectories
    const dirs = [
        path.join(e2eHome, '.headcount1', 'data'),
        path.join(e2eHome, '.headcount1', 'companies'),
    ];
    for (const dir of dirs) {
        fs.mkdirSync(dir, { recursive: true });
    }

    return e2eHome;
}

/**
 * Returns the headcount1 base path within the given home directory.
 */
export function getHeadcount1Base(homeDir: string): string {
    return path.join(homeDir, '.headcount1');
}

/**
 * Cleans up the E2E home directory.
 */
export function cleanupE2EHome(homeDir: string): void {
    if (fs.existsSync(homeDir)) {
        fs.rmSync(homeDir, { recursive: true, force: true });
    }
}
