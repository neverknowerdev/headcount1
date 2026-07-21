import { useState } from 'react';
import axios from 'axios';
import { Link, useNavigate, useSearchParams } from 'react-router-dom';
import { useStore } from '../store';
import { AuthShell, AuthError, AuthField, AuthSubmit } from './AuthShell';
import { register, passkeysSupported } from '../lib/webauthn';

// Passkey recovery: lost your passkey → request an email link → confirming it
// wipes your stored secrets (they can't be decrypted without the old passkey)
// but keeps your account, then you enroll a fresh passkey and re-enter keys.
export function RecoverPage() {
    const setUser = useStore((s) => s.setUser);
    const navigate = useNavigate();
    const [params] = useSearchParams();
    const token = params.get('token') || '';
    const [email, setEmail] = useState('');
    const [sent, setSent] = useState(false);
    const [error, setError] = useState('');
    const [busy, setBusy] = useState(false);

    const request = async (e: React.FormEvent) => {
        e.preventDefault();
        setBusy(true);
        setError('');
        try {
            await axios.post('/api/auth/recover/request', { email });
            setSent(true);
        } catch (err: any) {
            setError(err?.response?.data?.error || 'Something went wrong — try again.');
        } finally {
            setBusy(false);
        }
    };

    const confirm = async (e: React.FormEvent) => {
        e.preventDefault();
        if (!passkeysSupported()) {
            setError('This browser does not support passkeys.');
            return;
        }
        setBusy(true);
        setError('');
        try {
            // Verify the link + crypto-shred the old secrets, then re-enroll.
            const res = await axios.post('/api/auth/recover/confirm', { token });
            const recoveredEmail = res.data.email as string;
            const data = await register(recoveredEmail);
            setUser({ ...data.user, locked: !data.unlocked });
            navigate('/', { replace: true });
        } catch (err: any) {
            setError(err?.response?.data?.error || err?.message || 'Recovery failed — request a new link.');
        } finally {
            setBusy(false);
        }
    };

    if (token) {
        return (
            <AuthShell title="Reset your passkey" subtitle="Enroll a new passkey to regain access.">
                <AuthError message={error} />
                <div className="mb-4 rounded border border-amber-200 bg-amber-50 px-3 py-2 text-sm text-amber-800">
                    Your stored secrets (LLM API keys, MCP tokens) will be <strong>cleared</strong> —
                    they can’t be decrypted without your old passkey. Your account, teams, and
                    projects are kept; you’ll just re-enter your keys.
                </div>
                <form onSubmit={confirm}>
                    <AuthSubmit busy={busy}>Create a new passkey</AuthSubmit>
                </form>
                <div className="mt-4 text-center text-sm">
                    <Link to="/login" className="text-indigo-600 hover:underline">Back to sign in</Link>
                </div>
            </AuthShell>
        );
    }

    return (
        <AuthShell title="Lost your passkey?" subtitle="We’ll email you a recovery link.">
            <AuthError message={error} />
            {sent ? (
                <p className="text-sm text-gray-600">
                    If an account exists for <strong>{email}</strong>, a recovery link is on its way.
                    Open it to enroll a new passkey.
                </p>
            ) : (
                <form onSubmit={request}>
                    <AuthField label="Email" type="email" value={email} onChange={setEmail} autoFocus />
                    <AuthSubmit busy={busy}>Send recovery link</AuthSubmit>
                </form>
            )}
            <div className="mt-4 text-center text-sm">
                <Link to="/login" className="text-indigo-600 hover:underline">Back to sign in</Link>
            </div>
        </AuthShell>
    );
}
