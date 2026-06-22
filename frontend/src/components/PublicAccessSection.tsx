import React, { useState, useEffect, useCallback } from 'react';
import axios from 'axios';

interface TunnelStatus {
    provider: string;
    enabled: boolean;
    url: string;
    status: 'stopped' | 'starting' | 'running' | 'error';
    error?: string;
}

const TAILSCALE_INSTRUCTIONS = `# 1. Install Tailscale
curl -fsSL https://tailscale.com/install.sh | sh

# 2. Connect to your tailnet (log in)
sudo tailscale up

# 3. Enable HTTPS certificates (required for Funnel)
sudo tailscale cert

# Tailscale Funnel will be enabled automatically when you click "Enable Tunnel" below.`;

const CLOUDFLARE_INSTRUCTIONS = `# Ubuntu / Debian
wget -q https://pkg.cloudflare.com/cloudflare-main.gpg -O /usr/share/keyrings/cloudflare-main.gpg
echo "deb [signed-by=/usr/share/keyrings/cloudflare-main.gpg] https://pkg.cloudflare.com/cloudflared $(lsb_release -cs) main" \\
  | sudo tee /etc/apt/sources.list.d/cloudflared.list
sudo apt-get update && sudo apt-get install cloudflared

# macOS
brew install cloudflared

# Manual (Linux x86-64)
curl -L https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-amd64 \\
  -o /usr/local/bin/cloudflared && chmod +x /usr/local/bin/cloudflared

# No account needed — click "Enable Tunnel" below and a temporary URL will be generated.`;

interface ServerInfo {
    ips: string[];
    port: string;
}

export const PublicAccessSection: React.FC = () => {
    const [tunnelStatus, setTunnelStatus] = useState<TunnelStatus>({
        provider: '',
        enabled: false,
        url: '',
        status: 'stopped',
    });
    const [serverInfo, setServerInfo] = useState<ServerInfo>({ ips: [], port: '8080' });
    const [selectedProvider, setSelectedProvider] = useState('');
    const [loading, setLoading] = useState(false);
    const [showInstructions, setShowInstructions] = useState<string | null>(null);
    const [copied, setCopied] = useState(false);

    const fetchStatus = useCallback(async () => {
        try {
            const res = await axios.get<TunnelStatus>('/api/tunnel/status');
            setTunnelStatus(res.data);
            if (res.data.provider && !selectedProvider) {
                setSelectedProvider(res.data.provider);
            }
        } catch (e) {
            console.error(e);
        }
    }, [selectedProvider]);

    useEffect(() => {
        fetchStatus();
        axios.get<ServerInfo>('/api/server-info').then(res => setServerInfo(res.data)).catch(() => {});
    }, []); // eslint-disable-line react-hooks/exhaustive-deps

    useEffect(() => {
        if (tunnelStatus.status === 'starting') {
            const interval = setInterval(fetchStatus, 2000);
            return () => clearInterval(interval);
        }
    }, [tunnelStatus.status, fetchStatus]);

    const handleStart = async () => {
        if (!selectedProvider) return;
        setLoading(true);
        try {
            await axios.post('/api/tunnel/start', { provider: selectedProvider });
            fetchStatus();
        } catch (e) {
            console.error(e);
        } finally {
            setLoading(false);
        }
    };

    const handleStop = async () => {
        setLoading(true);
        try {
            await axios.post('/api/tunnel/stop');
            setTunnelStatus(prev => ({ ...prev, status: 'stopped', enabled: false, url: '' }));
        } catch (e) {
            console.error(e);
        } finally {
            setLoading(false);
        }
    };

    const copyURL = () => {
        if (tunnelStatus.url) {
            navigator.clipboard.writeText(tunnelStatus.url);
            setCopied(true);
            setTimeout(() => setCopied(false), 2000);
        }
    };

    const { status, url, error } = tunnelStatus;
    const isRunning = status === 'running';
    const isStarting = status === 'starting';
    const hasError = status === 'error';

    return (
        <div className="bg-white p-6 rounded-lg shadow-sm border mt-8">
            <h2 className="text-lg font-medium text-gray-900 border-b pb-2 mb-4">Public Access</h2>
            <p className="text-sm text-gray-600 mb-5">
                Expose this Paperclip2 instance via a secure HTTPS tunnel so you and your team can
                access it from anywhere. Choose a provider, install the required binary if needed,
                then click <strong>Enable Tunnel</strong>.
            </p>

            {/* Running state — show URL */}
            {isRunning && url && (
                <div className="bg-green-50 border border-green-200 rounded-md p-4 mb-5">
                    <div className="flex items-center gap-2 mb-2">
                        <span className="inline-block w-2 h-2 bg-green-500 rounded-full" />
                        <span className="text-sm font-medium text-green-800">
                            Tunnel active via {tunnelStatus.provider === 'tailscale' ? 'Tailscale' : 'Cloudflare'}
                        </span>
                    </div>
                    <div className="flex items-center gap-2 flex-wrap">
                        <a
                            href={url}
                            target="_blank"
                            rel="noopener noreferrer"
                            className="text-green-700 font-mono text-sm hover:underline break-all"
                        >
                            {url}
                        </a>
                        <button
                            onClick={copyURL}
                            className="text-xs text-green-700 border border-green-400 rounded px-2 py-0.5 hover:bg-green-100 whitespace-nowrap"
                        >
                            {copied ? 'Copied!' : 'Copy URL'}
                        </button>
                    </div>
                    {tunnelStatus.provider === 'cloudflare' && (
                        <p className="text-xs text-green-600 mt-2">
                            Note: this URL is temporary and will change if the tunnel is restarted.
                        </p>
                    )}
                </div>
            )}

            {/* Starting state — spinner */}
            {isStarting && (
                <div className="bg-blue-50 border border-blue-200 rounded-md p-4 mb-5 flex items-center gap-3">
                    <svg className="animate-spin h-4 w-4 text-blue-600 shrink-0" xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24">
                        <circle className="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" strokeWidth="4" />
                        <path className="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z" />
                    </svg>
                    <span className="text-sm text-blue-800">Starting tunnel, please wait…</span>
                </div>
            )}

            {/* Error state */}
            {hasError && error && (
                <div className="bg-red-50 border border-red-200 rounded-md p-4 mb-5">
                    <p className="text-sm font-medium text-red-800 mb-1">Tunnel failed to start</p>
                    <p className="text-sm text-red-700 font-mono break-all">{error}</p>
                </div>
            )}

            {/* Provider selection — shown when not running */}
            {!isRunning && !isStarting && (
                <div className="grid grid-cols-1 sm:grid-cols-2 gap-4 mb-5">
                    {/* Tailscale card */}
                    <div
                        role="button"
                        tabIndex={0}
                        onClick={() => setSelectedProvider('tailscale')}
                        onKeyDown={e => e.key === 'Enter' && setSelectedProvider('tailscale')}
                        className={`border-2 rounded-lg p-4 cursor-pointer transition-colors focus:outline-none focus:ring-2 focus:ring-indigo-400 ${
                            selectedProvider === 'tailscale'
                                ? 'border-indigo-500 bg-indigo-50'
                                : 'border-gray-200 hover:border-gray-300'
                        }`}
                    >
                        <div className="flex items-start justify-between mb-1">
                            <span className="font-medium text-gray-900">Tailscale</span>
                            <span className="text-xs bg-green-100 text-green-700 rounded px-1.5 py-0.5 ml-2 whitespace-nowrap">
                                Most secure
                            </span>
                        </div>
                        <p className="text-xs text-gray-600 mb-3">
                            VPN-based access limited to your tailnet members. Stable URL, works
                            across restarts. Best for personal or team use.
                        </p>
                        <button
                            type="button"
                            onClick={e => {
                                e.stopPropagation();
                                setShowInstructions(showInstructions === 'tailscale' ? null : 'tailscale');
                            }}
                            className="text-xs text-indigo-600 hover:underline"
                        >
                            {showInstructions === 'tailscale' ? 'Hide' : 'Show'} install instructions
                        </button>
                        {showInstructions === 'tailscale' && (
                            <pre className="mt-3 text-xs bg-gray-900 text-gray-100 rounded p-3 overflow-x-auto whitespace-pre-wrap">
                                {TAILSCALE_INSTRUCTIONS}
                            </pre>
                        )}
                    </div>

                    {/* Cloudflare card */}
                    <div
                        role="button"
                        tabIndex={0}
                        onClick={() => setSelectedProvider('cloudflare')}
                        onKeyDown={e => e.key === 'Enter' && setSelectedProvider('cloudflare')}
                        className={`border-2 rounded-lg p-4 cursor-pointer transition-colors focus:outline-none focus:ring-2 focus:ring-indigo-400 ${
                            selectedProvider === 'cloudflare'
                                ? 'border-indigo-500 bg-indigo-50'
                                : 'border-gray-200 hover:border-gray-300'
                        }`}
                    >
                        <div className="flex items-start justify-between mb-1">
                            <span className="font-medium text-gray-900">Cloudflare Tunnel</span>
                            <span className="text-xs bg-orange-100 text-orange-700 rounded px-1.5 py-0.5 ml-2 whitespace-nowrap">
                                Public URL
                            </span>
                        </div>
                        <p className="text-xs text-gray-600 mb-3">
                            Quick HTTPS tunnel via Cloudflare's global network. No account required.
                            URL is temporary and changes on restart.
                        </p>
                        <button
                            type="button"
                            onClick={e => {
                                e.stopPropagation();
                                setShowInstructions(showInstructions === 'cloudflare' ? null : 'cloudflare');
                            }}
                            className="text-xs text-indigo-600 hover:underline"
                        >
                            {showInstructions === 'cloudflare' ? 'Hide' : 'Show'} install instructions
                        </button>
                        {showInstructions === 'cloudflare' && (
                            <pre className="mt-3 text-xs bg-gray-900 text-gray-100 rounded p-3 overflow-x-auto whitespace-pre-wrap">
                                {CLOUDFLARE_INSTRUCTIONS}
                            </pre>
                        )}
                    </div>
                </div>
            )}

            {/* Action buttons */}
            <div className="flex gap-3 flex-wrap mb-6">
                {!isRunning && !isStarting && (
                    <button
                        type="button"
                        onClick={handleStart}
                        disabled={!selectedProvider || loading}
                        className="bg-indigo-600 text-white px-4 py-2 rounded-md shadow-sm hover:bg-indigo-700 disabled:bg-indigo-300 disabled:cursor-not-allowed text-sm"
                    >
                        {loading ? 'Starting…' : 'Enable Tunnel'}
                    </button>
                )}
                {(isRunning || isStarting) && (
                    <button
                        type="button"
                        onClick={handleStop}
                        disabled={loading}
                        className="bg-red-600 text-white px-4 py-2 rounded-md shadow-sm hover:bg-red-700 disabled:bg-red-300 disabled:cursor-not-allowed text-sm"
                    >
                        {loading ? 'Stopping…' : 'Disable Tunnel'}
                    </button>
                )}
                {hasError && (
                    <button
                        type="button"
                        onClick={handleStart}
                        disabled={!selectedProvider || loading}
                        className="bg-indigo-600 text-white px-4 py-2 rounded-md shadow-sm hover:bg-indigo-700 disabled:bg-indigo-300 disabled:cursor-not-allowed text-sm"
                    >
                        {loading ? 'Retrying…' : 'Retry'}
                    </button>
                )}
            </div>

            {/* Local network access — always visible as fallback */}
            {serverInfo.ips.length > 0 && (
                <div className="border-t pt-4">
                    <p className="text-xs font-medium text-gray-500 uppercase tracking-wide mb-2">
                        Direct access (local network)
                    </p>
                    <p className="text-xs text-gray-500 mb-2">
                        If the tunnel stops working, you can reach this instance directly on your local network:
                    </p>
                    <div className="flex flex-wrap gap-2">
                        {serverInfo.ips.map(ip => (
                            <a
                                key={ip}
                                href={`http://${ip}:${serverInfo.port}`}
                                target="_blank"
                                rel="noopener noreferrer"
                                className="font-mono text-xs bg-gray-100 hover:bg-gray-200 text-gray-700 rounded px-2 py-1"
                            >
                                http://{ip}:{serverInfo.port}
                            </a>
                        ))}
                    </div>
                </div>
            )}
        </div>
    );
};
