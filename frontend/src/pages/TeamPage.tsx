import { useCallback, useEffect, useState } from 'react';
import axios from 'axios';
import { Users, Copy, Trash2, Check } from 'lucide-react';
import { useStore } from '../store';

interface TeamMember {
    user_id: number;
    email: string;
    role: string;
    joined_at: string;
}

interface TeamInvite {
    id: number;
    email: string;
    role: string;
    expires_at: string;
}

interface TeamInfo {
    id: number;
    name: string;
    role: string;
    members: TeamMember[];
    invites?: TeamInvite[];
}

function RoleBadge({ role }: { role: string }) {
    const isOwner = role === 'owner';
    return (
        <span className={`rounded-full px-2 py-0.5 text-xs font-medium ${
            isOwner ? 'bg-indigo-100 text-indigo-700' : 'bg-gray-100 text-gray-600'
        }`}>
            {role}
        </span>
    );
}

export function TeamPage() {
    const currentUser = useStore((s) => s.user);
    const [team, setTeam] = useState<TeamInfo | null>(null);
    const [error, setError] = useState('');
    const [inviteEmail, setInviteEmail] = useState('');
    const [inviteBusy, setInviteBusy] = useState(false);
    const [lastInviteURL, setLastInviteURL] = useState('');
    const [copied, setCopied] = useState(false);

    const load = useCallback(async () => {
        try {
            const res = await axios.get('/api/team');
            setTeam(res.data);
        } catch (err: any) {
            setError(err.response?.data?.error || 'Failed to load team');
        }
    }, []);

    useEffect(() => { load(); }, [load]);

    const sendInvite = async (e: React.FormEvent) => {
        e.preventDefault();
        setInviteBusy(true);
        setError('');
        setLastInviteURL('');
        try {
            const res = await axios.post('/api/team/invites', { email: inviteEmail });
            setInviteEmail('');
            setLastInviteURL(res.data.invite_url || '');
            await load();
        } catch (err: any) {
            setError(err.response?.data?.error || 'Failed to send invite');
        } finally {
            setInviteBusy(false);
        }
    };

    const revokeInvite = async (id: number) => {
        try {
            await axios.delete(`/api/team/invites/${id}`);
            await load();
        } catch (err: any) {
            setError(err.response?.data?.error || 'Failed to revoke invite');
        }
    };

    const copyInviteURL = async () => {
        try {
            await navigator.clipboard.writeText(lastInviteURL);
            setCopied(true);
            setTimeout(() => setCopied(false), 1500);
        } catch { /* clipboard unavailable — the URL is still visible to select */ }
    };

    if (!team) {
        return <div className="p-8 text-sm text-gray-500">{error || 'Loading team…'}</div>;
    }

    const isOwner = team.role === 'owner';

    return (
        <div className="mx-auto max-w-3xl p-8">
            <div className="mb-6 flex items-center gap-3">
                <Users className="h-6 w-6 text-indigo-600" />
                <div>
                    <h1 className="text-2xl font-bold text-gray-900">{team.name}</h1>
                    <p className="text-sm text-gray-500">
                        {team.members.length} member{team.members.length === 1 ? '' : 's'} · you are <RoleBadge role={team.role} />
                    </p>
                </div>
            </div>

            {error && (
                <div className="mb-4 rounded border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-700">{error}</div>
            )}

            <div className="mb-8 rounded-lg bg-white shadow">
                <div className="border-b px-6 py-3 text-sm font-semibold text-gray-700">Members</div>
                <ul>
                    {team.members.map((m) => (
                        <li key={m.user_id} className="flex items-center justify-between border-b px-6 py-3 last:border-b-0" data-testid="team-member">
                            <span className="text-sm text-gray-900">
                                {m.email}
                                {currentUser?.id === m.user_id && <span className="ml-2 text-xs text-gray-400">(you)</span>}
                            </span>
                            <RoleBadge role={m.role} />
                        </li>
                    ))}
                </ul>
            </div>

            {isOwner && (
                <div className="rounded-lg bg-white shadow">
                    <div className="border-b px-6 py-3 text-sm font-semibold text-gray-700">Invite a member</div>
                    <div className="px-6 py-4">
                        <p className="mb-3 text-sm text-gray-500">
                            Only the team owner can invite. The invitee gets an email link that is valid for 7 days;
                            registering through it joins them to this team as a member.
                        </p>
                        <form onSubmit={sendInvite} className="flex gap-2">
                            <input
                                type="email"
                                required
                                value={inviteEmail}
                                onChange={(e) => setInviteEmail(e.target.value)}
                                placeholder="teammate@company.com"
                                className="flex-1 rounded border p-2 text-sm focus:border-indigo-500 focus:outline-none"
                            />
                            <button
                                type="submit"
                                disabled={inviteBusy}
                                className="rounded-md bg-indigo-600 px-4 py-2 text-sm font-semibold text-white hover:bg-indigo-700 disabled:opacity-50"
                            >
                                {inviteBusy ? 'Sending…' : 'Invite'}
                            </button>
                        </form>
                        {lastInviteURL && (
                            <div className="mt-3 flex items-center gap-2 rounded border border-green-200 bg-green-50 px-3 py-2 text-xs text-green-800">
                                <span className="truncate" title={lastInviteURL}>Invite sent — link: {lastInviteURL}</span>
                                <button onClick={copyInviteURL} title="Copy invite link" className="shrink-0 text-green-700 hover:text-green-900">
                                    {copied ? <Check size={14} /> : <Copy size={14} />}
                                </button>
                            </div>
                        )}
                    </div>

                    {team.invites && team.invites.length > 0 && (
                        <>
                            <div className="border-t border-b px-6 py-3 text-sm font-semibold text-gray-700">Pending invites</div>
                            <ul>
                                {team.invites.map((inv) => (
                                    <li key={inv.id} className="flex items-center justify-between border-b px-6 py-3 last:border-b-0" data-testid="pending-invite">
                                        <span className="text-sm text-gray-900">{inv.email}</span>
                                        <span className="flex items-center gap-3">
                                            <span className="text-xs text-gray-400">
                                                expires {new Date(inv.expires_at).toLocaleDateString()}
                                            </span>
                                            <button
                                                onClick={() => revokeInvite(inv.id)}
                                                title="Revoke invite"
                                                className="text-red-500 hover:text-red-700"
                                            >
                                                <Trash2 size={16} />
                                            </button>
                                        </span>
                                    </li>
                                ))}
                            </ul>
                        </>
                    )}
                </div>
            )}
        </div>
    );
}
