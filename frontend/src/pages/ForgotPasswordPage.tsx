import { useState } from 'react';
import axios from 'axios';
import { Link } from 'react-router-dom';
import { AuthShell, AuthError, AuthField, AuthSubmit } from './AuthShell';

export function ForgotPasswordPage() {
    const [email, setEmail] = useState('');
    const [sent, setSent] = useState(false);
    const [error, setError] = useState('');
    const [busy, setBusy] = useState(false);

    const submit = async (e: React.FormEvent) => {
        e.preventDefault();
        setBusy(true);
        setError('');
        try {
            await axios.post('/api/auth/reset-request', { email });
            setSent(true);
        } catch (err: any) {
            setError(err.response?.data?.error || 'Something went wrong — try again.');
        } finally {
            setBusy(false);
        }
    };

    return (
        <AuthShell title="Reset your password" subtitle="We'll email you a link that works for one hour.">
            <AuthError message={error} />
            {sent ? (
                <p className="text-sm text-gray-700">
                    If <span className="font-medium">{email}</span> has an account, a reset link is on its way.
                    Check your inbox (and spam folder).
                </p>
            ) : (
                <form onSubmit={submit}>
                    <AuthField label="Email" type="email" value={email} onChange={setEmail} autoFocus />
                    <AuthSubmit busy={busy}>Send reset link</AuthSubmit>
                </form>
            )}
            <div className="mt-4 text-center text-sm">
                <Link to="/login" className="text-indigo-600 hover:underline">Back to sign in</Link>
            </div>
        </AuthShell>
    );
}
