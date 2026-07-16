import { useState } from 'react';
import axios from 'axios';
import { Link, useNavigate, useSearchParams } from 'react-router-dom';
import { AuthShell, AuthError, AuthField, AuthSubmit } from './AuthShell';

export function ResetPasswordPage() {
    const [params] = useSearchParams();
    const token = params.get('token') || '';
    const navigate = useNavigate();
    const [password, setPassword] = useState('');
    const [confirm, setConfirm] = useState('');
    const [error, setError] = useState('');
    const [busy, setBusy] = useState(false);

    const submit = async (e: React.FormEvent) => {
        e.preventDefault();
        if (password !== confirm) {
            setError('Passwords do not match.');
            return;
        }
        setBusy(true);
        setError('');
        try {
            await axios.post('/api/auth/reset-confirm', { token, new_password: password });
            navigate('/login', { replace: true });
        } catch (err: any) {
            setError(err.response?.data?.error || 'Reset failed — the link may have expired.');
        } finally {
            setBusy(false);
        }
    };

    if (!token) {
        return (
            <AuthShell title="Invalid reset link">
                <p className="text-sm text-gray-700">This link is missing its token.</p>
                <div className="mt-4 text-center text-sm">
                    <Link to="/forgot-password" className="text-indigo-600 hover:underline">Request a new link</Link>
                </div>
            </AuthShell>
        );
    }

    return (
        <AuthShell title="Choose a new password" subtitle="Your saved API keys and data are unaffected.">
            <AuthError message={error} />
            <form onSubmit={submit}>
                <AuthField label="New password" type="password" value={password} onChange={setPassword} autoFocus placeholder="At least 8 characters" />
                <AuthField label="Confirm new password" type="password" value={confirm} onChange={setConfirm} />
                <AuthSubmit busy={busy}>Set new password</AuthSubmit>
            </form>
        </AuthShell>
    );
}
