import { useEffect, useState } from 'react';
import axios from 'axios';
import { Lock, ShieldAlert, X } from 'lucide-react';
import { useStore } from '../store';
import { reauth } from '../lib/webauthn';

// SessionGuard closes the gap between "the login silently expired" and "an agent
// suddenly can't decrypt secrets". A login has a hard ceiling
// (user.session_expires_at); a configurable safety gap before it
// (user.reauth_at) opens a window in which we nudge the user to re-authenticate
// with their passkey. Re-auth resets the ceiling and re-warms the keyring, so
// nothing ever stops mid-run.
//
//   before reauth_at            → nothing
//   reauth_at ≤ now < ceiling   → dismissible banner (undismissable in last 24h)
//   now ≥ ceiling               → full-screen hard gate (no silent logout)
export function SessionGuard({ children }: { children: React.ReactNode }) {
    const { user } = useStore();
    useNow(30_000); // re-render on a timer so the window/gate transitions on their own

    const now = Date.now();
    const expMs = user?.session_expires_at ? Date.parse(user.session_expires_at) : NaN;
    const reauthMs = user?.reauth_at ? Date.parse(user.reauth_at) : NaN;

    if (!Number.isNaN(expMs) && now >= expMs) {
        return <ReauthGate />;
    }
    const inWindow = !Number.isNaN(reauthMs) && now >= reauthMs;
    return (
        <>
            {inWindow && !Number.isNaN(expMs) && <SessionBanner expiresAt={expMs} />}
            {children}
        </>
    );
}

// useNow re-renders the calling component every `intervalMs` so time-based UI
// (countdowns, threshold crossings) updates without any server polling.
function useNow(intervalMs: number) {
    const [, setTick] = useState(0);
    useEffect(() => {
        const id = setInterval(() => setTick((t) => t + 1), intervalMs);
        return () => clearInterval(id);
    }, [intervalMs]);
}

// refreshMe re-probes /auth/me to pick up the reset ceiling after a re-auth.
async function refreshMe(setUser: (u: any) => void) {
    try {
        const res = await axios.get('/api/auth/me');
        setUser(res.data);
    } catch {
        setUser(null);
    }
}

// humanizeUntil renders a coarse, friendly "time left" (e.g. "3 days", "5
// hours", "8 minutes", "less than a minute").
function humanizeUntil(ms: number): string {
    const s = Math.max(0, Math.floor(ms / 1000));
    const days = Math.floor(s / 86400);
    if (days >= 1) return days === 1 ? '1 day' : `${days} days`;
    const hours = Math.floor(s / 3600);
    if (hours >= 1) return hours === 1 ? '1 hour' : `${hours} hours`;
    const mins = Math.floor(s / 60);
    if (mins >= 1) return mins === 1 ? '1 minute' : `${mins} minutes`;
    return 'less than a minute';
}

const ONE_DAY_MS = 24 * 60 * 60 * 1000;

// SessionBanner is the soft nudge shown during the re-auth window. Dismissible,
// but it returns on reload and becomes undismissable + red in the final 24h.
function SessionBanner({ expiresAt }: { expiresAt: number }) {
    const { setUser } = useStore();
    const [dismissed, setDismissed] = useState(false);
    const [busy, setBusy] = useState(false);
    const [error, setError] = useState('');

    const remaining = expiresAt - Date.now();
    const urgent = remaining <= ONE_DAY_MS;

    if (dismissed && !urgent) return null;

    const doReauth = async () => {
        setBusy(true);
        setError('');
        try {
            await reauth();
            await refreshMe(setUser);
        } catch (err: any) {
            setError(err?.response?.data?.error || err?.message || 'Re-authentication failed — try again.');
        } finally {
            setBusy(false);
        }
    };

    const tone = urgent
        ? 'border-red-300 bg-red-50 text-red-800'
        : 'border-amber-300 bg-amber-50 text-amber-900';

    return (
        <div className={`flex items-center gap-3 border-b px-4 py-2.5 text-sm ${tone}`}>
            <ShieldAlert size={18} className="shrink-0" />
            <div className="min-w-0 flex-1">
                <span className="font-medium">
                    Your session expires in {humanizeUntil(remaining)}.
                </span>{' '}
                <span className="opacity-90">
                    Re-authenticate with your passkey to keep your agents running — otherwise they’ll
                    stop when the session ends.
                </span>
                {error && <div className="mt-1 text-red-700">{error}</div>}
            </div>
            <button
                onClick={doReauth}
                disabled={busy}
                className="shrink-0 rounded-md bg-indigo-600 px-3 py-1.5 font-semibold text-white shadow-sm hover:bg-indigo-700 disabled:opacity-50"
            >
                {busy ? 'Please wait…' : 'Re-authenticate'}
            </button>
            {!urgent && (
                <button
                    onClick={() => setDismissed(true)}
                    className="shrink-0 rounded p-1 opacity-70 hover:opacity-100"
                    aria-label="Dismiss"
                >
                    <X size={16} />
                </button>
            )}
        </div>
    );
}

// ReauthGate is the hard gate once the ceiling is reached: instead of a silent
// forced logout, block the app and require a passkey re-auth. For an active
// user the access cookie is still valid, so re-auth succeeds and resets the
// ceiling; if it too has lapsed, fall back to a full sign-in.
function ReauthGate() {
    const { setUser } = useStore();
    const [busy, setBusy] = useState(false);
    const [error, setError] = useState('');

    const doReauth = async () => {
        setBusy(true);
        setError('');
        try {
            await reauth();
            await refreshMe(setUser);
        } catch (err: any) {
            if (err?.response?.status === 401) {
                // Access session also lapsed — nothing left to re-auth against.
                setUser(null);
                return;
            }
            setError(err?.response?.data?.error || err?.message || 'Re-authentication failed — try again.');
        } finally {
            setBusy(false);
        }
    };

    return (
        <div className="flex min-h-screen flex-col items-center justify-center gap-4 bg-gray-50 p-6 text-center">
            <Lock size={32} className="text-gray-400" />
            <div>
                <h2 className="text-lg font-semibold text-gray-900">Re-authenticate to continue</h2>
                <p className="mt-1 max-w-sm text-sm text-gray-500">
                    Your session has reached its limit. Tap your passkey to renew it — this keeps your
                    encrypted secrets available and your agents running, no data lost.
                </p>
            </div>
            {error && <p className="max-w-sm text-sm text-red-600">{error}</p>}
            <div className="flex gap-3">
                <button
                    onClick={doReauth}
                    disabled={busy}
                    className="rounded-md bg-indigo-600 py-2 px-4 font-semibold text-white shadow-sm hover:bg-indigo-700 disabled:opacity-50"
                >
                    {busy ? 'Please wait…' : 'Re-authenticate with passkey'}
                </button>
                <button
                    onClick={() => setUser(null)}
                    disabled={busy}
                    className="rounded-md border border-gray-300 py-2 px-4 font-semibold text-gray-700 hover:bg-gray-100 disabled:opacity-50"
                >
                    Sign in again
                </button>
            </div>
        </div>
    );
}
