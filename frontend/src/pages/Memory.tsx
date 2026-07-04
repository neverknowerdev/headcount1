import React, { useCallback, useEffect, useState } from 'react';
import axios from 'axios';
import {
  Brain, Search, Network, GitBranch, Activity as ActivityIcon, Users,
  FolderTree, Trash2, Pencil, Archive, RefreshCw,
} from 'lucide-react';
import { ReactFlow, Background, Controls, type Node, type Edge } from '@xyflow/react';
import '@xyflow/react/dist/style.css';
import { useStore } from '../store';

// ---- types mirroring /api/memory responses ----

interface MemoryStatus {
  available: boolean;
  ready: boolean;
  init_status?: string;
  install_error?: string;
  error?: string;
  palace?: { total_drawers?: number; wings?: Record<string, number>; rooms?: Record<string, number> };
}

interface Drawer {
  drawer_id: string;
  wing: string;
  room: string;
  content_preview: string;
  metadata?: { added_by?: string; filed_at?: string; source_file?: string };
}

interface SearchResult {
  text: string;
  wing: string;
  room: string;
  created_at?: string;
  similarity?: number;
}

interface Fact {
  subject: string;
  predicate: string;
  object: string;
  valid_from?: string | null;
  valid_to?: string | null;
  current?: boolean;
  direction?: string;
}

interface ActivityRow {
  id: number;
  agent_name: string;
  tool: string;
  kind: string;
  wing: string;
  room: string;
  query: string;
  result_n: number;
  created_at: string;
}

interface AgentStat {
  agent_name: string;
  reads: number;
  writes: number;
  last_activity?: string;
  diary?: { entries?: { entry?: string; content?: string; timestamp?: string; topic?: string }[] };
}

type Tab = 'explorer' | 'search' | 'graph' | 'facts' | 'activity' | 'agents';

const TABS: { id: Tab; label: string; icon: React.ElementType }[] = [
  { id: 'explorer', label: 'Explorer', icon: FolderTree },
  { id: 'search', label: 'Search', icon: Search },
  { id: 'graph', label: 'Graph', icon: Network },
  { id: 'facts', label: 'Facts', icon: GitBranch },
  { id: 'activity', label: 'Activity', icon: ActivityIcon },
  { id: 'agents', label: 'Agents', icon: Users },
];

export const Memory: React.FC = () => {
  const { selectedCompanyId } = useStore();
  const [status, setStatus] = useState<MemoryStatus | null>(null);
  const [tab, setTab] = useState<Tab>('explorer');

  const fetchStatus = useCallback(async () => {
    if (!selectedCompanyId) return;
    try {
      const res = await axios.get(`/api/memory/status?company_id=${selectedCompanyId}`);
      setStatus(res.data);
    } catch {
      setStatus(null);
    }
  }, [selectedCompanyId]);

  useEffect(() => {
    fetchStatus();
  }, [fetchStatus]);

  // Poll while the palace isn't ready yet (setup/init still running).
  useEffect(() => {
    if (status && (!status.available || !status.ready)) {
      const t = setInterval(fetchStatus, 5000);
      return () => clearInterval(t);
    }
  }, [status, fetchStatus]);

  if (!selectedCompanyId) {
    return <div className="p-8 text-gray-500">Select a company to view its memory.</div>;
  }

  return (
    <div className="h-full flex flex-col space-y-6">
      <div className="flex justify-between items-center">
        <div>
          <h1 className="text-2xl font-bold flex items-center">
            <Brain size={24} className="mr-2 text-indigo-600" /> Memory
          </h1>
          <p className="text-sm text-gray-500 mt-1">
            Long-term agent memory — verbatim storage with semantic recall, powered by MemPalace.
          </p>
        </div>
        {status?.palace && (
          <div className="flex space-x-4">
            <StatTile label="Drawers" value={status.palace.total_drawers ?? 0} />
            <StatTile label="Wings" value={Object.keys(status.palace.wings || {}).length} />
            <StatTile label="Rooms" value={Object.keys(status.palace.rooms || {}).length} />
          </div>
        )}
      </div>

      {status && !status.available && (
        <div className="bg-amber-50 border border-amber-200 rounded-lg p-6">
          <h2 className="font-semibold text-amber-800 mb-2">Memory layer not available</h2>
          <p className="text-sm text-amber-700">
            The MemPalace runtime is not installed (or setup is still running). It is installed
            automatically by the setup script; if installation failed, install it manually into the
            paperclip virtualenv: <code className="bg-amber-100 px-1 rounded">pip install mempalace</code>, then restart.
          </p>
          {status.install_error && (
            <p className="text-xs text-amber-600 mt-2">Setup reported: {status.install_error}</p>
          )}
        </div>
      )}

      {status && status.available && !status.ready && (
        <div className="bg-blue-50 border border-blue-200 rounded-lg p-6 text-sm text-blue-700">
          Memory palace is initializing ({status.init_status || 'pending'})… this page refreshes automatically.
        </div>
      )}

      {status && status.available && status.ready && (
        <>
          <div className="border-b flex space-x-1">
            {TABS.map((t) => (
              <button
                key={t.id}
                onClick={() => setTab(t.id)}
                className={`flex items-center px-4 py-2 text-sm font-medium border-b-2 -mb-px ${
                  tab === t.id
                    ? 'border-indigo-600 text-indigo-600'
                    : 'border-transparent text-gray-500 hover:text-gray-700'
                }`}
              >
                <t.icon size={15} className="mr-1.5" /> {t.label}
              </button>
            ))}
          </div>
          <div className="flex-1 overflow-y-auto">
            {tab === 'explorer' && <ExplorerTab companyId={selectedCompanyId} />}
            {tab === 'search' && <SearchTab companyId={selectedCompanyId} />}
            {tab === 'graph' && <GraphTab companyId={selectedCompanyId} />}
            {tab === 'facts' && <FactsTab companyId={selectedCompanyId} />}
            {tab === 'activity' && <ActivityTab companyId={selectedCompanyId} />}
            {tab === 'agents' && <AgentsTab companyId={selectedCompanyId} />}
          </div>
        </>
      )}
    </div>
  );
};

const StatTile: React.FC<{ label: string; value: number }> = ({ label, value }) => (
  <div className="bg-white rounded-lg border shadow-sm px-4 py-2 text-center">
    <div className="text-xl font-bold text-indigo-600">{value}</div>
    <div className="text-xs text-gray-400 uppercase tracking-wider">{label}</div>
  </div>
);

// ---- Explorer ----

const ExplorerTab: React.FC<{ companyId: number }> = ({ companyId }) => {
  const [taxonomy, setTaxonomy] = useState<Record<string, Record<string, number>>>({});
  const [selected, setSelected] = useState<{ wing: string; room?: string } | null>(null);
  const [drawers, setDrawers] = useState<Drawer[]>([]);
  const [viewing, setViewing] = useState<Drawer | null>(null);
  const [fullContent, setFullContent] = useState('');
  const [editing, setEditing] = useState(false);

  const fetchTaxonomy = useCallback(async () => {
    const res = await axios.get(`/api/memory/taxonomy?company_id=${companyId}`);
    setTaxonomy(res.data?.taxonomy || {});
  }, [companyId]);

  useEffect(() => {
    fetchTaxonomy().catch(() => setTaxonomy({}));
  }, [fetchTaxonomy]);

  const fetchDrawers = useCallback(async (wing: string, room?: string) => {
    setSelected({ wing, room });
    setViewing(null);
    const params = new URLSearchParams({ company_id: String(companyId), wing, limit: '50' });
    if (room) params.set('room', room);
    const res = await axios.get(`/api/memory/drawers?${params}`);
    setDrawers(res.data?.drawers || []);
  }, [companyId]);

  const openDrawer = async (d: Drawer) => {
    setViewing(d);
    setEditing(false);
    try {
      const res = await axios.get(`/api/memory/drawers/${encodeURIComponent(d.drawer_id)}?company_id=${companyId}`);
      setFullContent(res.data?.content || d.content_preview);
    } catch {
      setFullContent(d.content_preview);
    }
  };

  const refresh = () => selected && fetchDrawers(selected.wing, selected.room);

  const saveEdit = async () => {
    if (!viewing) return;
    await axios.put(`/api/memory/drawers/${encodeURIComponent(viewing.drawer_id)}?company_id=${companyId}`, { content: fullContent });
    setEditing(false);
    refresh();
  };

  const deleteDrawer = async () => {
    if (!viewing || !window.confirm('Delete this drawer permanently? Use "Mark superseded" if it is merely outdated.')) return;
    await axios.delete(`/api/memory/drawers/${encodeURIComponent(viewing.drawer_id)}?company_id=${companyId}`);
    setViewing(null);
    refresh();
  };

  const supersedeDrawer = async () => {
    if (!viewing) return;
    const reason = window.prompt('Why is this outdated? (optional)') || '';
    await axios.post(`/api/memory/drawers/${encodeURIComponent(viewing.drawer_id)}/supersede?company_id=${companyId}`, { reason });
    openDrawer(viewing);
    refresh();
  };

  return (
    <div className="grid grid-cols-12 gap-4 h-full">
      <div className="col-span-3 bg-white rounded-lg border shadow-sm p-4 overflow-y-auto">
        <h3 className="text-xs font-semibold text-gray-400 uppercase tracking-wider mb-2">Wings & Rooms</h3>
        {Object.keys(taxonomy).length === 0 && <p className="text-sm text-gray-400">Palace is empty — memories appear here as agents work.</p>}
        {Object.entries(taxonomy).map(([wing, rooms]) => (
          <div key={wing} className="mb-2">
            <button onClick={() => fetchDrawers(wing)} className={`text-sm font-medium w-full text-left px-1 py-0.5 rounded ${selected?.wing === wing && !selected?.room ? 'bg-indigo-50 text-indigo-600' : 'text-gray-700 hover:bg-gray-50'}`}>
              {wing}
            </button>
            {Object.entries(rooms).map(([room, count]) => (
              <button key={room} onClick={() => fetchDrawers(wing, room)} className={`text-sm w-full text-left pl-4 pr-1 py-0.5 rounded flex justify-between ${selected?.wing === wing && selected?.room === room ? 'bg-indigo-50 text-indigo-600' : 'text-gray-500 hover:bg-gray-50'}`}>
                <span>{room}</span>
                <span className="text-xs text-gray-400">{count}</span>
              </button>
            ))}
          </div>
        ))}
      </div>
      <div className="col-span-4 bg-white rounded-lg border shadow-sm p-4 overflow-y-auto">
        <h3 className="text-xs font-semibold text-gray-400 uppercase tracking-wider mb-2">Drawers</h3>
        {!selected && <p className="text-sm text-gray-400">Select a wing or room.</p>}
        {selected && drawers.length === 0 && <p className="text-sm text-gray-400">No drawers here.</p>}
        {drawers.map((d) => (
          <button key={d.drawer_id} onClick={() => openDrawer(d)} className={`block w-full text-left border rounded p-2 mb-2 text-sm ${viewing?.drawer_id === d.drawer_id ? 'border-indigo-400 bg-indigo-50' : 'hover:bg-gray-50'}`}>
            <div className="text-gray-800 line-clamp-2">{d.content_preview}</div>
            <div className="text-xs text-gray-400 mt-1">{d.room}{d.metadata?.added_by ? ` · by ${d.metadata.added_by}` : ''}</div>
          </button>
        ))}
      </div>
      <div className="col-span-5 bg-white rounded-lg border shadow-sm p-4 overflow-y-auto">
        <div className="flex justify-between items-center mb-2">
          <h3 className="text-xs font-semibold text-gray-400 uppercase tracking-wider">Content</h3>
          {viewing && (
            <div className="flex space-x-2">
              <button onClick={() => setEditing(!editing)} title="Edit" className="text-gray-400 hover:text-indigo-600"><Pencil size={15} /></button>
              <button onClick={supersedeDrawer} title="Mark superseded" className="text-gray-400 hover:text-amber-600"><Archive size={15} /></button>
              <button onClick={deleteDrawer} title="Delete" className="text-gray-400 hover:text-red-600"><Trash2 size={15} /></button>
            </div>
          )}
        </div>
        {!viewing && <p className="text-sm text-gray-400">Select a drawer.</p>}
        {viewing && !editing && <pre className="text-sm whitespace-pre-wrap text-gray-800">{fullContent}</pre>}
        {viewing && editing && (
          <div>
            <textarea className="w-full border rounded p-2 text-sm h-64" value={fullContent} onChange={(e) => setFullContent(e.target.value)} />
            <button onClick={saveEdit} className="mt-2 bg-indigo-600 text-white px-3 py-1.5 rounded text-sm hover:bg-indigo-700">Save</button>
          </div>
        )}
      </div>
    </div>
  );
};

// ---- Search ----

const SearchTab: React.FC<{ companyId: number }> = ({ companyId }) => {
  const [query, setQuery] = useState('');
  const [wing, setWing] = useState('');
  const [wings, setWings] = useState<string[]>([]);
  const [results, setResults] = useState<SearchResult[]>([]);
  const [searched, setSearched] = useState(false);

  useEffect(() => {
    axios.get(`/api/memory/taxonomy?company_id=${companyId}`)
      .then((res) => setWings(Object.keys(res.data?.taxonomy || {})))
      .catch(() => setWings([]));
  }, [companyId]);

  const search = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!query.trim()) return;
    const params = new URLSearchParams({ company_id: String(companyId), q: query });
    if (wing) params.set('wing', wing);
    const res = await axios.get(`/api/memory/search?${params}`);
    setResults(res.data?.results || []);
    setSearched(true);
  };

  return (
    <div className="space-y-4">
      <form onSubmit={search} className="flex space-x-2">
        <input className="flex-1 border rounded p-2 text-sm" placeholder="Semantic search — e.g. 'why did we drop WebSockets?'" value={query} onChange={(e) => setQuery(e.target.value)} />
        <select className="border rounded p-2 text-sm" value={wing} onChange={(e) => setWing(e.target.value)}>
          <option value="">All wings</option>
          {wings.map((w) => <option key={w} value={w}>{w}</option>)}
        </select>
        <button type="submit" className="bg-indigo-600 text-white px-4 py-2 rounded flex items-center hover:bg-indigo-700 shadow-sm">
          <Search size={16} className="mr-2" /> Search
        </button>
      </form>
      {searched && results.length === 0 && <p className="text-sm text-gray-400">No results.</p>}
      {results.map((r, i) => (
        <div key={i} className="bg-white rounded-lg border shadow-sm p-4">
          <div className="text-sm text-gray-800 whitespace-pre-wrap">{r.text}</div>
          <div className="text-xs text-gray-400 mt-2">
            {r.wing} / {r.room}
            {r.created_at ? ` · ${r.created_at.slice(0, 10)}` : ''}
            {typeof r.similarity === 'number' ? ` · similarity ${(r.similarity * 100).toFixed(0)}%` : ''}
          </div>
        </div>
      ))}
    </div>
  );
};

// ---- Graph ----

const GraphTab: React.FC<{ companyId: number }> = ({ companyId }) => {
  const [nodes, setNodes] = useState<Node[]>([]);
  const [edges, setEdges] = useState<Edge[]>([]);

  useEffect(() => {
    axios.get(`/api/memory/graph?company_id=${companyId}`).then((res) => {
      const rawNodes: { id: string; label: string; kind: string; drawer_count: number }[] = res.data?.nodes || [];
      const rawEdges: { from: string; to: string; kind: string; label?: string }[] = res.data?.edges || [];

      // Simple layout: wings in a column, their rooms fanned to the right.
      const wings = rawNodes.filter((n) => n.kind === 'wing');
      const rooms = rawNodes.filter((n) => n.kind === 'room');
      const laidOut: Node[] = [];
      wings.forEach((wingNode, wi) => {
        laidOut.push({
          id: wingNode.id,
          position: { x: 50, y: wi * 220 + 40 },
          data: { label: `${wingNode.label} (${wingNode.drawer_count})` },
          style: { background: '#eef2ff', border: '1px solid #6366f1', borderRadius: 8, fontWeight: 600 },
        });
        const myRooms = rooms.filter((room) => room.id.startsWith(wingNode.id + '/'));
        myRooms.forEach((room, ri) => {
          laidOut.push({
            id: room.id,
            position: { x: 340 + (ri % 3) * 200, y: wi * 220 + Math.floor(ri / 3) * 70 },
            data: { label: `${room.label} (${room.drawer_count})` },
            style: { borderRadius: 8 },
          });
        });
      });
      setNodes(laidOut);
      setEdges(rawEdges.map((edge, i) => ({
        id: `edge-${i}`,
        source: edge.from,
        target: edge.to,
        label: edge.label,
        animated: edge.kind === 'tunnel',
        style: edge.kind === 'tunnel' ? { stroke: '#f59e0b' } : edge.kind === 'hallway' ? { stroke: '#10b981' } : undefined,
      })));
    }).catch(() => { setNodes([]); setEdges([]); });
  }, [companyId]);

  if (nodes.length === 0) {
    return <p className="text-sm text-gray-400">The palace graph is empty — wings and rooms appear as memories are filed.</p>;
  }
  return (
    <div className="bg-white rounded-lg border shadow-sm" style={{ height: '600px' }}>
      <ReactFlow nodes={nodes} edges={edges} fitView proOptions={{ hideAttribution: true }}>
        <Background />
        <Controls />
      </ReactFlow>
    </div>
  );
};

// ---- Facts ----

const FactsTab: React.FC<{ companyId: number }> = ({ companyId }) => {
  const [entity, setEntity] = useState('');
  const [asOf, setAsOf] = useState('');
  const [facts, setFacts] = useState<Fact[]>([]);
  const [loaded, setLoaded] = useState(false);

  const load = useCallback(async () => {
    const params = new URLSearchParams({ company_id: String(companyId) });
    if (entity) params.set('entity', entity);
    if (asOf) params.set('as_of', asOf);
    const url = entity ? `/api/memory/facts?${params}` : `/api/memory/facts/timeline?${params}`;
    const res = await axios.get(url);
    setFacts(res.data?.facts || res.data?.timeline || []);
    setLoaded(true);
  }, [companyId, entity, asOf]);

  useEffect(() => { load().catch(() => setFacts([])); }, [companyId]); // eslint-disable-line react-hooks/exhaustive-deps

  const invalidate = async (f: Fact) => {
    if (!window.confirm(`Invalidate fact "${f.subject} ${f.predicate} ${f.object}"? Its validity window closes; history is preserved.`)) return;
    await axios.post(`/api/memory/facts/invalidate?company_id=${companyId}`, { subject: f.subject, predicate: f.predicate, object: f.object });
    load();
  };

  return (
    <div className="space-y-4">
      <form onSubmit={(e) => { e.preventDefault(); load(); }} className="flex space-x-2">
        <input className="border rounded p-2 text-sm flex-1" placeholder="Entity (e.g. task-dec-1) — empty for full timeline" value={entity} onChange={(e) => setEntity(e.target.value)} />
        <input type="date" className="border rounded p-2 text-sm" value={asOf} onChange={(e) => setAsOf(e.target.value)} />
        <button type="submit" className="bg-indigo-600 text-white px-4 py-2 rounded hover:bg-indigo-700 shadow-sm text-sm">Query</button>
      </form>
      <div className="bg-white rounded-lg border shadow-sm overflow-hidden">
        <table className="w-full text-sm">
          <thead className="bg-gray-50 text-left text-xs text-gray-500 uppercase tracking-wider">
            <tr>
              <th className="px-4 py-2">Subject</th>
              <th className="px-4 py-2">Predicate</th>
              <th className="px-4 py-2">Object</th>
              <th className="px-4 py-2">Valid</th>
              <th className="px-4 py-2"></th>
            </tr>
          </thead>
          <tbody>
            {facts.length === 0 && loaded && (
              <tr><td colSpan={5} className="px-4 py-6 text-center text-gray-400">No facts recorded yet.</td></tr>
            )}
            {facts.map((f, i) => (
              <tr key={i} className="border-t">
                <td className="px-4 py-2">{f.subject}</td>
                <td className="px-4 py-2 text-gray-500">{f.predicate}</td>
                <td className="px-4 py-2">{f.object}</td>
                <td className="px-4 py-2 text-xs">
                  {f.current === false || f.valid_to ? (
                    <span className="inline-flex items-center px-2 py-0.5 rounded text-xs font-medium bg-gray-100 text-gray-500">ended {f.valid_to ? String(f.valid_to).slice(0, 10) : ''}</span>
                  ) : (
                    <span className="inline-flex items-center px-2 py-0.5 rounded text-xs font-medium bg-green-100 text-green-700">current</span>
                  )}
                </td>
                <td className="px-4 py-2 text-right">
                  {(f.current !== false && !f.valid_to) && (
                    <button onClick={() => invalidate(f)} className="text-xs text-amber-600 hover:text-amber-800">invalidate</button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
};

// ---- Activity ----

const ActivityTab: React.FC<{ companyId: number }> = ({ companyId }) => {
  const [rows, setRows] = useState<ActivityRow[]>([]);
  const [total, setTotal] = useState(0);
  const [kind, setKind] = useState('');

  const load = useCallback(async () => {
    const params = new URLSearchParams({ company_id: String(companyId), limit: '50' });
    if (kind) params.set('kind', kind);
    const res = await axios.get(`/api/memory/activity?${params}`);
    setRows(res.data?.items || []);
    setTotal(res.data?.total || 0);
  }, [companyId, kind]);

  useEffect(() => { load().catch(() => setRows([])); }, [load]);

  const kindPill = (k: string) => {
    const cls = k === 'write' ? 'bg-amber-100 text-amber-700' : k === 'maintenance' ? 'bg-purple-100 text-purple-700' : 'bg-green-100 text-green-700';
    return <span className={`inline-flex items-center px-2 py-0.5 rounded text-xs font-medium ${cls}`}>{k}</span>;
  };

  return (
    <div className="space-y-4">
      <div className="flex justify-between items-center">
        <div className="text-sm text-gray-500">{total} operations recorded</div>
        <div className="flex space-x-2 items-center">
          <select className="border rounded p-1.5 text-sm" value={kind} onChange={(e) => setKind(e.target.value)}>
            <option value="">All kinds</option>
            <option value="read">reads</option>
            <option value="write">writes</option>
            <option value="maintenance">maintenance</option>
          </select>
          <button onClick={load} className="text-gray-400 hover:text-indigo-600" title="Refresh"><RefreshCw size={16} /></button>
        </div>
      </div>
      <div className="bg-white rounded-lg border shadow-sm divide-y">
        {rows.length === 0 && <p className="p-6 text-sm text-gray-400 text-center">No memory activity yet.</p>}
        {rows.map((row) => (
          <div key={row.id} className="px-4 py-2.5 flex items-center justify-between text-sm">
            <div className="flex items-center space-x-3 min-w-0">
              {kindPill(row.kind)}
              <span className="font-medium text-gray-700">{row.tool}</span>
              <span className="text-gray-400 truncate">{row.query}</span>
            </div>
            <div className="text-xs text-gray-400 flex-shrink-0 ml-4">
              {row.agent_name || 'engine'} · {row.wing}{row.result_n ? ` · ${row.result_n} results` : ''} · {new Date(row.created_at).toLocaleString()}
            </div>
          </div>
        ))}
      </div>
    </div>
  );
};

// ---- Agents ----

const AgentsTab: React.FC<{ companyId: number }> = ({ companyId }) => {
  const [agents, setAgents] = useState<AgentStat[]>([]);
  const [loaded, setLoaded] = useState(false);

  useEffect(() => {
    axios.get(`/api/memory/agents?company_id=${companyId}`)
      .then((res) => { setAgents(res.data || []); setLoaded(true); })
      .catch(() => { setAgents([]); setLoaded(true); });
  }, [companyId]);

  return (
    <div className="grid grid-cols-2 gap-4">
      {loaded && agents.length === 0 && <p className="text-sm text-gray-400 col-span-2">No agent memory usage yet.</p>}
      {agents.map((a) => (
        <div key={a.agent_name} className="bg-white rounded-lg border shadow-sm p-5">
          <div className="flex justify-between items-center mb-2">
            <h3 className="font-semibold text-gray-800">{a.agent_name}</h3>
            <div className="text-xs text-gray-400">
              {a.reads} reads · {a.writes} writes
            </div>
          </div>
          {a.diary?.entries && a.diary.entries.length > 0 ? (
            <div>
              <h4 className="text-xs font-semibold text-gray-400 uppercase tracking-wider mb-1">Recent diary</h4>
              {a.diary.entries.slice(0, 3).map((e, i) => (
                <div key={i} className="text-sm text-gray-600 border-l-2 border-indigo-100 pl-2 mb-2 whitespace-pre-wrap line-clamp-4">
                  {e.entry || e.content || ''}
                </div>
              ))}
            </div>
          ) : (
            <p className="text-sm text-gray-400">No diary entries.</p>
          )}
        </div>
      ))}
    </div>
  );
};
