import { useState } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import { useStore } from '../store';
import { AuthShell, AuthError, AuthField, AuthSubmit } from './AuthShell';
import { login, passkeysSupported } from '../lib/webauthn';

export function LoginPage() {
    const setUser = useStore((s) => s.setUser);
    const navigate = useNavigate();
    const [email, setEmail] = useState('');
    const [error, setError] = useState('');
    const [busy, setBusy] = useState(false);

    const submit = async (e: React.FormEvent) => {
        e.preventDefault();
        if (!passkeysSupported()) {
            setError('This browser does not support passkeys. Use a modern browser or device.');
            return;
        }
        setBusy(true);
        setError('');
        try {
            const data = await login(email);
            setUser({ ...data.user, locked: !data.unlocked });
            if (data.unlocked) {
                navigate('/', { replace: true });
            } else {
                setError('Signed in, but this device can’t decrypt your data (no PRF). Use your enrolled device.');
            }
        } catch (err: any) {
            setError(err?.response?.data?.error || err?.message || 'Passkey sign-in failed — try again.');
        } finally {
            setBusy(false);
        }
    };

    return (
        <AuthShell title="Sign in" subtitle="Sign in with your passkey — no password needed.">
            <AuthError message={error} />
            <form onSubmit={submit}>
                <AuthField label="Email" type="email" value={email} onChange={setEmail} autoFocus />
                <AuthSubmit busy={busy}>Sign in with passkey</AuthSubmit>
            </form>
            <div className="mt-4 flex justify-between text-sm">
                <Link to="/recover" className="text-indigo-600 hover:underline">Lost your passkey?</Link>
                <Link to="/register" className="text-indigo-600 hover:underline">Create an account</Link>
            </div>
        </AuthShell>
    );
}
