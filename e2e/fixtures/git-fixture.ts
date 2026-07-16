import { spawnSync } from 'child_process';
import * as fs from 'fs';
import * as os from 'os';
import * as path from 'path';

/**
 * Creates a local bare git repository with a dummy initial commit and returns
 * its `file://` URL. The bare repo lives in a temp directory under
 * `<tmp>/headcount-e2e/<id>/repo.git` so each test run gets a clean slate.
 *
 * The dummy commit is required so that `git ls-remote` and `git clone` succeed
 * when the orchestrator validates the remote and clones it.
 */
export function setupBareRepo(): string {
    const baseDir = path.join(os.tmpdir(), 'headcount-e2e');
    fs.mkdirSync(baseDir, { recursive: true });

    const id = `${Date.now()}-${Math.random().toString(36).slice(2, 8)}`;
    const workDir = path.join(baseDir, `${id}-work`);
    const bareDir = path.join(baseDir, `${id}.git`);

    fs.mkdirSync(workDir, { recursive: true });
    run('git', ['init', '--initial-branch=main', workDir]);

    // Configure a local committer so commits succeed
    run('git', ['-C', workDir, 'config', 'user.email', 'e2e@headcount.local']);
    run('git', ['-C', workDir, 'config', 'user.name', 'headcount e2e']);

    // Create an initial commit so the repo isn't empty
    fs.writeFileSync(path.join(workDir, 'README.md'), '# headcount e2e repo\n');
    run('git', ['-C', workDir, 'add', 'README.md']);
    run('git', ['-C', workDir, 'commit', '-m', 'initial commit']);

    // Convert the working repo into a bare one at bareDir
    run('git', ['clone', '--bare', workDir, bareDir]);

    return `file://${bareDir}`;
}

function run(cmd: string, args: string[]): string {
    const result = spawnSync(cmd, args, { stdio: 'pipe', encoding: 'utf8' });
    if (result.status !== 0) {
        throw new Error(
            `git command failed: ${cmd} ${args.join(' ')}\n` +
            `exit: ${result.status}\n` +
            `stderr: ${result.stderr}\n` +
            `stdout: ${result.stdout}`,
        );
    }
    return result.stdout;
}
