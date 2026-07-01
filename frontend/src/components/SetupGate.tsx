import { useEffect, useRef, useState } from 'react';
import axios from 'axios';
import { Loader2, AlertTriangle } from 'lucide-react';

type SetupStatus =
  | { phase: 'checking' }
  | { phase: 'pending' }
  | { phase: 'ok'; warning?: string }
  | { phase: 'error'; message: string; warning?: string };

const POLL_INTERVAL_MS = 2000;

export function SetupGate({ children }: { children: React.ReactNode }) {
  const [status, setStatus] = useState<SetupStatus>({ phase: 'checking' });
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(() => {
    let cancelled = false;

    const poll = async () => {
      try {
        const res = await axios.get('/api/setup-status');
        if (cancelled) return;
        const data = res.data as { pending?: boolean; ok?: boolean; error?: string; warning?: string };
        if (data.pending) {
          setStatus({ phase: 'pending' });
          timerRef.current = setTimeout(poll, POLL_INTERVAL_MS);
        } else if (data.ok) {
          setStatus({ phase: 'ok', warning: data.warning || undefined });
        } else {
          setStatus({
            phase: 'error',
            message: data.error || 'Setup failed for an unknown reason.',
            warning: data.warning || undefined,
          });
        }
      } catch {
        if (cancelled) return;
        setStatus({ phase: 'pending' });
        timerRef.current = setTimeout(poll, POLL_INTERVAL_MS);
      }
    };

    poll();

    return () => {
      cancelled = true;
      if (timerRef.current) clearTimeout(timerRef.current);
    };
  }, []);

  if (status.phase === 'ok') {
    if (!status.warning) {
      return <>{children}</>;
    }
    return (
      <>
        <div className="flex items-center gap-2 bg-amber-950/80 px-4 py-2 text-xs text-amber-300">
          <AlertTriangle size={14} className="shrink-0" />
          <span>Setup warning: {status.warning}</span>
        </div>
        {children}
      </>
    );
  }

  if (status.phase === 'error') {
    return (
      <div className="flex min-h-screen items-center justify-center bg-slate-950 p-6">
        <div className="max-w-lg rounded-lg border border-red-900/50 bg-slate-900 p-6 text-slate-200">
          <div className="mb-3 flex items-center gap-2 text-red-400">
            <AlertTriangle size={20} />
            <h1 className="text-lg font-semibold">Setup failed</h1>
          </div>
          <p className="mb-3 text-sm text-slate-400">
            Paperclip couldn't finish installing its required dependencies. Fix the issue below and restart the
            server — the app will unlock automatically once setup succeeds.
          </p>
          <pre className="max-h-64 overflow-auto whitespace-pre-wrap rounded bg-slate-950 p-3 text-xs text-red-300">
            {status.message}
          </pre>
        </div>
      </div>
    );
  }

  return (
    <div className="flex min-h-screen items-center justify-center bg-slate-950">
      <div className="flex flex-col items-center gap-3 text-slate-300">
        <Loader2 size={28} className="animate-spin" />
        <p className="text-sm">Setting up dependencies...</p>
      </div>
    </div>
  );
}
