import { useEffect, useRef, useState } from 'react';
import axios from 'axios';
import { Loader2, AlertTriangle, ChevronDown, ChevronRight } from 'lucide-react';

interface SetupFailure {
  name: string;
  reason: string;
  detail?: string;
}

type SetupStatus =
  | { phase: 'checking' }
  | { phase: 'pending'; step?: string }
  | { phase: 'ok'; warning?: string; warnings?: SetupFailure[] }
  | { phase: 'error'; message: string; warning?: string; failures?: SetupFailure[]; warnings?: SetupFailure[] };

const POLL_INTERVAL_MS = 2000;

function FailureItem({ failure, tone }: { failure: SetupFailure; tone: 'red' | 'amber' }) {
  const [expanded, setExpanded] = useState(false);
  const colors = tone === 'red'
    ? { border: 'border-red-900/50', text: 'text-red-300', hover: 'hover:bg-red-950/40', pre: 'bg-slate-950 text-red-300 border-red-900/40' }
    : { border: 'border-amber-900/50', text: 'text-amber-300', hover: 'hover:bg-amber-950/40', pre: 'bg-slate-950 text-amber-300 border-amber-900/40' };

  return (
    <div className={`rounded border ${colors.border}`}>
      <button
        onClick={() => setExpanded(!expanded)}
        disabled={!failure.detail}
        className={`w-full flex items-start gap-2 px-3 py-2 text-left text-sm ${failure.detail ? colors.hover : 'cursor-default'}`}
      >
        {failure.detail ? (
          expanded ? <ChevronDown size={14} className="mt-0.5 shrink-0" /> : <ChevronRight size={14} className="mt-0.5 shrink-0" />
        ) : (
          <span className="w-3.5 shrink-0" />
        )}
        <span>
          <span className={`font-medium ${colors.text}`}>{failure.name}</span>
          <span className="text-slate-400"> — {failure.reason}</span>
        </span>
      </button>
      {expanded && failure.detail && (
        <pre className={`mx-3 mb-2 max-h-48 overflow-auto whitespace-pre-wrap rounded border p-2 text-xs ${colors.pre}`}>
          {failure.detail}
        </pre>
      )}
    </div>
  );
}

export function SetupGate({ children }: { children: React.ReactNode }) {
  const [status, setStatus] = useState<SetupStatus>({ phase: 'checking' });
  const [showRawLog, setShowRawLog] = useState(false);
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(() => {
    let cancelled = false;

    const poll = async () => {
      try {
        const res = await axios.get('/api/setup-status');
        if (cancelled) return;
        const data = res.data as {
          pending?: boolean; ok?: boolean; error?: string; warning?: string; step?: string;
          failures?: SetupFailure[]; warnings?: SetupFailure[];
        };
        if (data.pending) {
          setStatus({ phase: 'pending', step: data.step || undefined });
          timerRef.current = setTimeout(poll, POLL_INTERVAL_MS);
        } else if (data.ok) {
          setStatus({ phase: 'ok', warning: data.warning || undefined, warnings: data.warnings || undefined });
        } else {
          setStatus({
            phase: 'error',
            message: data.error || 'Setup failed for an unknown reason.',
            warning: data.warning || undefined,
            failures: data.failures || undefined,
            warnings: data.warnings || undefined,
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
        <div className="bg-amber-950/80 px-4 py-2 text-xs text-amber-300">
          <div className="flex items-center gap-2">
            <AlertTriangle size={14} className="shrink-0" />
            <span>Setup warning: {status.warning}</span>
          </div>
          {status.warnings && status.warnings.length > 0 && (
            <div className="mt-2 space-y-1.5">
              {status.warnings.map((f, i) => (
                <FailureItem key={i} failure={f} tone="amber" />
              ))}
            </div>
          )}
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
            Headcount couldn't finish installing its required dependencies. Fix the issue below and restart the
            server — the app will unlock automatically once setup succeeds.
          </p>
          {status.failures && status.failures.length > 0 ? (
            <div className="space-y-2">
              {status.failures.map((f, i) => (
                <FailureItem key={i} failure={f} tone="red" />
              ))}
            </div>
          ) : (
            <pre className="max-h-64 overflow-auto whitespace-pre-wrap rounded bg-slate-950 p-3 text-xs text-red-300">
              {status.message}
            </pre>
          )}
          {status.failures && status.failures.length > 0 && (
            <div className="mt-3">
              <button
                onClick={() => setShowRawLog(!showRawLog)}
                className="flex items-center gap-1 text-xs text-slate-500 hover:text-slate-300"
              >
                {showRawLog ? <ChevronDown size={12} /> : <ChevronRight size={12} />}
                Show full setup log
              </button>
              {showRawLog && (
                <pre className="mt-2 max-h-64 overflow-auto whitespace-pre-wrap rounded bg-slate-950 p-3 text-xs text-slate-400">
                  {status.message}
                </pre>
              )}
            </div>
          )}
        </div>
      </div>
    );
  }

  return (
    <div className="flex min-h-screen items-center justify-center bg-slate-950">
      <div className="flex flex-col items-center gap-3 text-slate-300">
        <Loader2 size={28} className="animate-spin" />
        <p className="text-sm">Setting up dependencies...</p>
        {status.phase === 'pending' && status.step && (
          <p className="text-xs text-slate-500">{status.step}</p>
        )}
      </div>
    </div>
  );
}
