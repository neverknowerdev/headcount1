import { execSync } from 'child_process';

/**
 * Asserts that the `docker` CLI is available and the daemon is reachable. The
 * engine starts a Docker sandbox for every task run, so this is a hard
 * requirement for E2E.
 */
export function assertDockerAvailable(): void {
    let version: string;
    try {
        version = execSync('docker --version', { stdio: 'pipe', encoding: 'utf8' }).trim();
    } catch (err: any) {
        throw new Error(
            `E2E requires Docker to run agent tasks. 'docker --version' failed: ${err?.message || err}. ` +
            `Install Docker (https://docs.docker.com/get-docker/) and ensure the daemon is running.`,
        );
    }
    try {
        execSync('docker info', { stdio: 'pipe' });
    } catch (err: any) {
        throw new Error(
            `E2E requires a running Docker daemon. 'docker info' failed: ${err?.message || err}. ` +
            `Start the Docker daemon and try again.`,
        );
    }
    console.log(`[e2e] docker available: ${version}`);
}
