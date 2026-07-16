import { useState } from 'react';
import axios from 'axios';
import { Link, useNavigate } from 'react-router-dom';
import { useStore } from '../store';
import { AuthShell, AuthError, AuthField, AuthSubmit } from './AuthShell';

export function LoginPage() {
    const setUser = useStore((s) => s.setUser);
    const navigate = useNavigate();
    const [email, setEmail] = useState('');
    const [password, setPassword] = useState('');
    const [error, setError] = useState('');
    const [busy, setBusy] = useState(false);

    const submit = async (e: React.FormEvent) => {
        e.preventDefault();
        setBusy(true);
        setError('');
        try {
            const res = await axios.post('/api/auth/login', { email, password });
            setUser(res.data);
            navigate('/', { replace: true });
        } catch (err: any) {
            setError(err.response?.data?.error || 'Login failed — try again.');
        } finally {
            setBusy(false);
        }
    };

    return (
        <AuthShell title="Sign in" subtitle="Welcome back — log in to see your companies.">
            <AuthError message={error} />
            <form onSubmit={submit}>
                <AuthField label="Email" type="email" value={email} onChange={setEmail} autoFocus />
                <AuthField label="Password" type="password" value={password} onChange={setPassword} />
                <AuthSubmit busy={busy}>Sign in</AuthSubmit>
            </form>
            <div className="mt-4 flex justify-between text-sm">
                <Link to="/forgot-password" className="text-indigo-600 hover:underline">Forgot password?</Link>
                <Link to="/register" className="text-indigo-600 hover:underline">Create an account</Link>
            </div>
        </AuthShell>
    );
}
