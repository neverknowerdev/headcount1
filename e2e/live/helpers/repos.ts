import { spawnSync } from 'child_process';
import * as fs from 'fs';
import * as os from 'os';
import * as path from 'path';
import { loadE2EEnv } from '../../helpers/env';

/**
 * Creates a bare git repository holding `files` in one initial commit and
 * returns its file:// URL. Each call is a fresh repository.
 */
export function seedRepo(files: Record<string, string>): string {
    const base = fs.mkdtempSync(path.join(os.tmpdir(), 'headcount1-live-repo-'));
    const work = path.join(base, 'work');
    const bare = path.join(base, 'repo.git');
    git(base, 'init', '--initial-branch=main', work);
    git(work, 'config', 'user.email', 'e2e@headcount1.local');
    git(work, 'config', 'user.name', 'headcount1 e2e');
    for (const [rel, content] of Object.entries({ 'README.md': '# headcount1 live e2e repo\n', ...files })) {
        const file = path.join(work, rel);
        fs.mkdirSync(path.dirname(file), { recursive: true });
        fs.writeFileSync(file, content);
    }
    git(work, 'add', '-A');
    git(work, 'commit', '-m', 'initial commit');
    git(base, 'clone', '--bare', work, bare);
    return `file://${bare}`;
}

/** The task tree's shared git worktree. */
export function worktreeOf(shortName: string, taskId: number): string {
    return path.join(loadE2EEnv().E2E_HEADCOUNT1_HOME, '.headcount1', 'workspace', shortName, `task-${taskId}`);
}

export function git(dir: string, ...args: string[]): string {
    const result = spawnSync('git', ['-C', dir, ...args], { encoding: 'utf8' });
    if (result.status !== 0) throw new Error(`git ${args.join(' ')} failed in ${dir}: ${result.stderr}`);
    return result.stdout.trim();
}

export interface CommandResult {
    status: number | null;
    output: string;
}

/**
 * Copies the worktree (without .git) to a scratch directory, adds the hidden
 * acceptance files the agents never saw, and runs `command` there. The
 * worktree itself is never touched.
 */
export function runAcceptance(worktree: string, hidden: Record<string, string>, command: string[], timeoutMs = 120_000): CommandResult {
    const scratch = fs.mkdtempSync(path.join(os.tmpdir(), 'headcount1-live-accept-'));
    fs.cpSync(worktree, scratch, { recursive: true, filter: (src) => path.basename(src) !== '.git' && path.basename(src) !== 'node_modules' });
    for (const [rel, content] of Object.entries(hidden)) {
        const file = path.join(scratch, rel);
        fs.mkdirSync(path.dirname(file), { recursive: true });
        fs.writeFileSync(file, content);
    }
    const result = spawnSync(command[0], command.slice(1), { cwd: scratch, encoding: 'utf8', timeout: timeoutMs });
    return { status: result.status, output: `$ ${command.join(' ')}\n${result.stdout}${result.stderr}` };
}

/** Files changed on the task branch since the initial commit. */
export function changedFiles(worktree: string): string[] {
    const root = git(worktree, 'rev-list', '--max-parents=0', 'HEAD').split('\n')[0];
    return git(worktree, 'diff', '--name-only', root, 'HEAD').split('\n').filter(Boolean);
}
