import { ChildProcess } from 'child_process';

export interface TerminateOptions {
    timeoutMs?: number;
    group?: boolean;
}

function sendSignal(child: ChildProcess, signal: NodeJS.Signals, group: boolean): void {
    if (!child.pid) return;
    try {
        if (group) process.kill(-child.pid, signal);
        else child.kill(signal);
    } catch {
        // The process may have exited between the state check and signal.
    }
}

export function waitForExit(child: ChildProcess, timeoutMs = 5_000): Promise<void> {
    if (child.exitCode !== null || child.signalCode !== null) return Promise.resolve();
    return new Promise((resolve, reject) => {
        let settled = false;
        const finish = (err?: Error) => {
            if (settled) return;
            settled = true;
            clearTimeout(timer);
            child.removeListener('exit', onExit);
            child.removeListener('error', onError);
            err ? reject(err) : resolve();
        };
        const onExit = () => finish();
        const onError = (err: Error) => finish(err);
        const timer = setTimeout(() => finish(new Error(`process did not exit within ${timeoutMs}ms`)), timeoutMs);
        child.once('exit', onExit);
        child.once('error', onError);
    });
}

export async function terminateProcess(child: ChildProcess | null, options: TerminateOptions = {}): Promise<void> {
    if (!child || child.exitCode !== null || child.signalCode !== null) return;
    const timeoutMs = options.timeoutMs ?? 5_000;
    const group = options.group ?? false;
    sendSignal(child, 'SIGTERM', group);
    try {
        await waitForExit(child, timeoutMs);
        return;
    } catch {
        sendSignal(child, 'SIGKILL', group);
        try { await waitForExit(child, 2_000); } catch { /* bounded best effort */ }
    }
}

export async function terminatePid(pid: number, group = true, timeoutMs = 5_000): Promise<void> {
    try {
        if (group) process.kill(-pid, 'SIGTERM');
        else process.kill(pid, 'SIGTERM');
    } catch {
        return;
    }
    const deadline = Date.now() + timeoutMs;
    while (Date.now() < deadline) {
        try {
            process.kill(group ? -pid : pid, 0);
        } catch {
            return;
        }
        await new Promise((resolve) => setTimeout(resolve, 100));
    }
    try {
        if (group) process.kill(-pid, 'SIGKILL');
        else process.kill(pid, 'SIGKILL');
    } catch { /* already exited */ }
}
