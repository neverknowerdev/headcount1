// Shared layout + form primitives for the unauthenticated pages
// (login / register / forgot / reset), styled after the AddCompany wizard.
export function AuthShell({ title, subtitle, children }: {
    title: string;
    subtitle?: string;
    children: React.ReactNode;
}) {
    return (
        <div className="flex min-h-screen items-center justify-center bg-gray-50 p-6">
            <div className="w-full max-w-md">
                <div className="mb-6 text-center">
                    <h1 className="text-2xl font-bold text-gray-900">headcount1</h1>
                    <p className="mt-1 text-sm text-gray-500">AI agent orchestration</p>
                </div>
                <div className="rounded-lg bg-white p-8 shadow">
                    <h2 className="mb-1 text-xl font-semibold text-gray-900">{title}</h2>
                    {subtitle && <p className="mb-4 text-sm text-gray-500">{subtitle}</p>}
                    {children}
                </div>
            </div>
        </div>
    );
}

export function AuthError({ message }: { message: string }) {
    if (!message) return null;
    return (
        <div className="mb-4 rounded border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-700">
            {message}
        </div>
    );
}

export function AuthField({ label, type, value, onChange, autoFocus, placeholder }: {
    label: string;
    type: string;
    value: string;
    onChange: (v: string) => void;
    autoFocus?: boolean;
    placeholder?: string;
}) {
    return (
        <div className="mb-4">
            <label className="mb-1 block text-sm font-medium text-gray-700">{label}</label>
            <input
                type={type}
                required
                autoFocus={autoFocus}
                value={value}
                placeholder={placeholder}
                onChange={(e) => onChange(e.target.value)}
                className="w-full rounded border p-2 focus:border-indigo-500 focus:outline-none"
            />
        </div>
    );
}

export function AuthSubmit({ busy, children }: { busy: boolean; children: React.ReactNode }) {
    return (
        <button
            type="submit"
            disabled={busy}
            className="w-full rounded-md bg-indigo-600 py-2 px-4 font-semibold text-white shadow-sm transition-colors hover:bg-indigo-700 disabled:opacity-50"
        >
            {busy ? 'Please wait…' : children}
        </button>
    );
}
