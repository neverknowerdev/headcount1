import { spawnSync } from 'child_process';
import * as fs from 'fs';
import * as os from 'os';
import * as path from 'path';

/**
 * Creates a local bare git repository with a dummy initial commit and returns
 * its `file://` URL. The bare repo lives in a temp directory under
 * `<tmp>/headcount1-e2e/<id>/repo.git` so each test run gets a clean slate.
 *
 * The dummy commit is required so that `git ls-remote` and `git clone` succeed
 * when the orchestrator validates the remote and clones it.
 */
export function setupBareRepo(): string {
    const baseDir = path.join(os.tmpdir(), 'headcount1-e2e');
    fs.mkdirSync(baseDir, { recursive: true });

    const id = `${Date.now()}-${Math.random().toString(36).slice(2, 8)}`;
    const workDir = path.join(baseDir, `${id}-work`);
    const bareDir = path.join(baseDir, `${id}.git`);

    fs.mkdirSync(workDir, { recursive: true });
    run('git', ['init', '--initial-branch=main', workDir]);

    // Configure a local committer so commits succeed
    run('git', ['-C', workDir, 'config', 'user.email', 'e2e@headcount1.local']);
    run('git', ['-C', workDir, 'config', 'user.name', 'headcount1 e2e']);

    // Create an initial commit so the repo isn't empty. The .md files double
    // as documentation for the Hindsight memory ingestion e2e tests: every
    // .md file becomes one memory document ("doc:<relpath>").
    fs.writeFileSync(path.join(workDir, 'README.md'), '# paperclip e2e repo\n');
    fs.mkdirSync(path.join(workDir, 'docs'), { recursive: true });
    fs.writeFileSync(
        path.join(workDir, 'docs', 'gm-coin.md'),
        '# GM Coin\n\nGM Coin is a community token. The GM Coin product rewards users for daily greetings.\n',
    );
    fs.writeFileSync(
        path.join(workDir, 'docs', 'icp-backend.md'),
        '# ICP backend\n\nThe backend for GM Coin runs on the Internet Computer (ICP). ' +
        'Canisters written in Motoko store balances and process GM Coin transactions.\n',
    );
    run('git', ['-C', workDir, 'add', '.']);
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
