import { useState } from 'react';
import { Lock } from 'lucide-react';

// SecretLabel renders a field label with the lock marker and the hover
// "Encrypted" explainer. Use it for secret inputs that can't adopt the full
// SecretField — file dropzones, custom-typed inputs (e.g. an MCP URL that
// carries a token) — so every secret-collecting field carries the same
// affordance.
export function SecretLabel({ children }: { children: React.ReactNode }) {
    const [open, setOpen] = useState(false);
    return (
        <div className="mb-1 flex items-center gap-1.5">
            <Lock size={13} className="text-emerald-600" />
            <label className="text-sm font-medium text-gray-700">{children}</label>
            <span
                className="relative cursor-help text-xs text-emerald-700 underline decoration-dotted"
                onMouseEnter={() => setOpen(true)}
                onMouseLeave={() => setOpen(false)}
            >
                Encrypted
                {open && (
                    <span className="absolute left-0 top-5 z-10 w-64 rounded border border-gray-200 bg-white p-2 text-left text-xs font-normal text-gray-600 shadow-lg">
                        Encrypted at rest with your personal key and never stored raw.
                        Only unlocked while you’re signed in — the server can’t read it
                        when you’re logged out.
                    </span>
                )}
            </span>
        </div>
    );
}

// SecretField is a labeled input for values that are encrypted at rest and
// never stored in raw form (LLM API keys, MCP tokens). It shows a lock marker
// and an inline explainer so the user understands the value is protected.
export function SecretField({
    label,
    value,
    onChange,
    placeholder,
    type = 'password',
    hint,
    className = '',
}: {
    label?: string;
    value: string;
    onChange: (v: string) => void;
    placeholder?: string;
    type?: string;
    hint?: string;
    className?: string;
}) {
    return (
        <div className={className}>
            {label && <SecretLabel>{label}</SecretLabel>}
            <input
                type={type}
                value={value}
                placeholder={placeholder}
                onChange={(e) => onChange(e.target.value)}
                className="w-full rounded border p-2 focus:border-indigo-500 focus:outline-none"
            />
            {hint && <p className="mt-1 text-xs text-gray-500">{hint}</p>}
        </div>
    );
}
