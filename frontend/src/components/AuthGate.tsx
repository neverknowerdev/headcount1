import { useEffect, useState } from 'react';
import axios from 'axios';
import { Loader2 } from 'lucide-react';
import { Routes, Route, Navigate } from 'react-router-dom';
import { useStore } from '../store';
import { LoginPage } from '../pages/LoginPage';
import { RegisterPage } from '../pages/RegisterPage';
import { ForgotPasswordPage } from '../pages/ForgotPasswordPage';
import { ResetPasswordPage } from '../pages/ResetPasswordPage';

// AuthGate mirrors SetupGate: it probes the session once and renders either
// the unauthenticated mini-router (login/register/reset pages) or the app.
// The global 401 interceptor (main.tsx) clears the user, flipping the gate
// back to the login screen without a reload.
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
                <Route path="/forgot-password" element={<ForgotPasswordPage />} />
                <Route path="/reset-password" element={<ResetPasswordPage />} />
                <Route path="*" element={<Navigate to="/login" replace />} />
            </Routes>
        );
    }

    return <>{children}</>;
}
