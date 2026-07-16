import { useState } from 'react';
import axios from 'axios';
import { Link, useNavigate } from 'react-router-dom';
import { useStore } from '../store';
import { AuthShell, AuthError, AuthField, AuthSubmit } from './AuthShell';

export function RegisterPage() {
    const setUser = useStore((s) => s.setUser);
    const navigate = useNavigate();
    const [email, setEmail] = useState('');
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
            const res = await axios.post('/api/auth/register', { email, password });
            setUser(res.data);
            navigate('/', { replace: true });
        } catch (err: any) {
            setError(err.response?.data?.error || 'Registration failed — try again.');
        } finally {
            setBusy(false);
        }
    };

    return (
        <AuthShell title="Create your account" subtitle="Free to set up — bring your own LLM API keys.">
            <AuthError message={error} />
            <form onSubmit={submit}>
                <AuthField label="Email" type="email" value={email} onChange={setEmail} autoFocus />
                <AuthField label="Password" type="password" value={password} onChange={setPassword} placeholder="At least 8 characters" />
                <AuthField label="Confirm password" type="password" value={confirm} onChange={setConfirm} />
                <AuthSubmit busy={busy}>Create account</AuthSubmit>
            </form>
            <div className="mt-4 text-center text-sm">
                <span className="text-gray-500">Already have an account? </span>
                <Link to="/login" className="text-indigo-600 hover:underline">Sign in</Link>
            </div>
        </AuthShell>
    );
}
