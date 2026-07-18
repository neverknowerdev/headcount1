import { useEffect, useState } from 'react';
import axios from 'axios';
import { Loader2, Lock } from 'lucide-react';
import { Routes, Route, Navigate } from 'react-router-dom';
import { useStore } from '../store';
import { LoginPage } from '../pages/LoginPage';
import { RegisterPage } from '../pages/RegisterPage';
import { RecoverPage } from '../pages/RecoverPage';
import { unlock } from '../lib/webauthn';
import { SessionGuard } from './SessionGuard';

// AuthGate probes the session once and renders either the unauthenticated
// mini-router (login/register/recover), a re-tap "unlock" screen when the
// session is valid but the encryption vault is locked (e.g. after a server
// crash), or the app. The global 401 interceptor (main.tsx) clears the user.
export function AuthGate({ children }: { children: React.ReactNode }) {
    const { user, setUser } = useStore();
    const [checked, setChecked] = useState(false);

    useEffect(() => {
        let cancelled = false;
        axios.get('/api/auth/me')
            .then((res) => { if (!cancelled) setUser(res.data); })
            .catch(() => { if (!cancelled) setUser(null); })
            .finally(() => { if (!cancelled) setChecked(true); });
        return () => { cancelled = true; };
        // eslint-disable-next-line react-hooks/exhaustive-deps
    }, []);

    if (!checked) {
        return (
            <div className="flex min-h-screen items-center justify-center bg-gray-50">
                <Loader2 size={28} className="animate-spin text-gray-400" />
            </div>
        );
    }

    if (!user) {
        return (
            <Routes>
                <Route path="/login" element={<LoginPage />} />
                <Route path="/register" element={<RegisterPage />} />
                <Route path="/recover" element={<RecoverPage />} />
                <Route path="*" element={<Navigate to="/login" replace />} />
            </Routes>
        );
    }

    if (user.locked) {
        return <UnlockGate />;
    }

    return <SessionGuard>{children}</SessionGuard>;
}

// UnlockGate re-warms the vault with a passkey tap without logging out.
function UnlockGate() {
    const { user, setUser } = useStore();
    const [error, setError] = useState('');
    const [busy, setBusy] = useState(false);

    const doUnlock = async () => {
        setBusy(true);
        setError('');
        try {
            const data = await unlock();
            if (data.unlocked) setUser({ ...user!, locked: false });
            else setError('This device can’t decrypt your data. Use your enrolled device.');
        } catch (err: any) {
            setError(err?.response?.data?.error || err?.message || 'Unlock failed — try again.');
        } finally {
            setBusy(false);
        }
    };

    return (
        <div className="flex min-h-screen flex-col items-center justify-center gap-4 bg-gray-50 p-6 text-center">
            <Lock size={32} className="text-gray-400" />
            <div>
                <h2 className="text-lg font-semibold text-gray-900">Unlock your vault</h2>
                <p className="mt-1 max-w-sm text-sm text-gray-500">
                    You’re signed in, but your encrypted secrets are locked. Tap your passkey to
                    unlock decryption for this session.
                </p>
            </div>
            {error && <p className="max-w-sm text-sm text-red-600">{error}</p>}
            <button
                onClick={doUnlock}
                disabled={busy}
                className="rounded-md bg-indigo-600 py-2 px-4 font-semibold text-white shadow-sm hover:bg-indigo-700 disabled:opacity-50"
            >
                {busy ? 'Please wait…' : 'Unlock with passkey'}
            </button>
        </div>
    );
}
