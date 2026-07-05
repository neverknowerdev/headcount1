import React, { useCallback, useEffect, useState } from 'react';
import axios from 'axios';
import {
  Brain, Search, Network, GitBranch, Activity as ActivityIcon, Users,
  FolderTree, Trash2, Pencil, Archive, RefreshCw, X, Info,
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

interface ActivityDetail extends ActivityRow {
  args?: string;
  response?: string;
  task_id?: number | null;
  run_id?: number | null;
}

interface AgentStat {
  agent_name: string;
  reads: number;
  writes: number;
  last_activity?: string;
  diary?: { entries?: { entry?: string; content?: string; timestamp?: string; topic?: string }[] };
}

interface GraphNode {
  id: string;
  label: string;
  kind: string; // "wing" | "room"
  wing?: string;
  room?: string;
  drawer_count: number;
}
interface GraphEdge {
  from: string;
  to: string;
  kind: string; // "contains" | "tunnel" | "hallway"
  label?: string;
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

// ---- shared: relationships (tunnels/hallways) fetch, used by Explorer + Graph ----

function useRelationships(companyId: number) {
  const [edges, setEdges] = useState<GraphEdge[]>([]);
  useEffect(() => {
    axios.get(`/api/memory/graph?company_id=${companyId}`)
      .then((res) => setEdges((res.data?.edges || []).filter((e: GraphEdge) => e.kind !== 'contains')))
      .catch(() => setEdges([]));
  }, [companyId]);
  return edges;
}

const RelationshipsPanel: React.FC<{ edges: GraphEdge[] }> = ({ edges }) => (
  <div>
    <h3 className="text-xs font-semibold text-gray-400 uppercase tracking-wider mb-2 flex items-center">
      Relationships <span className="ml-1.5 text-gray-300">· which room/wing depends on which</span>
    </h3>
    {edges.length === 0 && <p className="text-sm text-gray-400">No cross-wing tunnels or hallways yet.</p>}
    {edges.map((e, i) => (
      <div key={i} className="text-sm flex items-center py-1 border-b last:border-b-0">
        <span className="text-gray-700 truncate">{e.from}</span>
        <span className={`mx-2 text-xs px-1.5 py-0.5 rounded font-medium flex-shrink-0 ${e.kind === 'tunnel' ? 'bg-amber-100 text-amber-700' : 'bg-emerald-100 text-emerald-700'}`}>
          {e.kind === 'tunnel' ? '⇢ tunnel' : '↝ hallway'}
        </span>
        <span className="text-gray-700 truncate">{e.to}</span>
        {e.label && <span className="ml-2 text-xs text-gray-400 truncate">({e.label})</span>}
      </div>
    ))}
  </div>
);

// ---- shared: drawer list + content viewer, used by Explorer AND Graph (combined UI) ----

const DrawerBrowser: React.FC<{ companyId: number; wing: string; room?: string; title?: string }> = ({ companyId, wing, room, title }) => {
  const [drawers, setDrawers] = useState<Drawer[]>([]);
  const [viewing, setViewing] = useState<Drawer | null>(null);
  const [fullContent, setFullContent] = useState('');
  const [editing, setEditing] = useState(false);
  const [loading, setLoading] = useState(false);

  const fetchDrawers = useCallback(async () => {
    setViewing(null);
    setLoading(true);
    try {
      const params = new URLSearchParams({ company_id: String(companyId), wing, limit: '50' });
      if (room) params.set('room', room);
      const res = await axios.get(`/api/memory/drawers?${params}`);
      setDrawers(res.data?.drawers || []);
    } catch {
      setDrawers([]);
    } finally {
      setLoading(false);
    }
  }, [companyId, wing, room]);

  useEffect(() => { fetchDrawers(); }, [fetchDrawers]);

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

  const saveEdit = async () => {
    if (!viewing) return;
    await axios.put(`/api/memory/drawers/${encodeURIComponent(viewing.drawer_id)}?company_id=${companyId}`, { content: fullContent });
    setEditing(false);
    fetchDrawers();
  };

  const deleteDrawer = async () => {
    if (!viewing || !window.confirm('Delete this drawer permanently? Use "Mark superseded" if it is merely outdated.')) return;
    await axios.delete(`/api/memory/drawers/${encodeURIComponent(viewing.drawer_id)}?company_id=${companyId}`);
    setViewing(null);
    fetchDrawers();
  };

  const supersedeDrawer = async () => {
    if (!viewing) return;
    const reason = window.prompt('Why is this outdated? (optional)') || '';
    await axios.post(`/api/memory/drawers/${encodeURIComponent(viewing.drawer_id)}/supersede?company_id=${companyId}`, { reason });
    openDrawer(viewing);
    fetchDrawers();
  };

  return (
    <div className="grid grid-cols-12 gap-4 h-full">
      <div className="col-span-5 bg-white rounded-lg border shadow-sm p-4 overflow-y-auto">
        <h3 className="text-xs font-semibold text-gray-400 uppercase tracking-wider mb-2">
          {title || `Drawers — ${wing}${room ? ` / ${room}` : ''}`}
        </h3>
        {loading && <p className="text-sm text-gray-400">Loading…</p>}
        {!loading && drawers.length === 0 && <p className="text-sm text-gray-400">No drawers here yet.</p>}
        {drawers.map((d) => (
          <button key={d.drawer_id} onClick={() => openDrawer(d)} className={`block w-full text-left border rounded p-2 mb-2 text-sm ${viewing?.drawer_id === d.drawer_id ? 'border-indigo-400 bg-indigo-50' : 'hover:bg-gray-50'}`}>
            <div className="text-gray-800 line-clamp-2">{d.content_preview}</div>
            <div className="text-xs text-gray-400 mt-1">{d.room}{d.metadata?.added_by ? ` · by ${d.metadata.added_by}` : ''}</div>
          </button>
        ))}
      </div>
      <div className="col-span-7 bg-white rounded-lg border shadow-sm p-4 overflow-y-auto">
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

// ---- Explorer ----

const ExplorerTab: React.FC<{ companyId: number }> = ({ companyId }) => {
  const [taxonomy, setTaxonomy] = useState<Record<string, Record<string, number>>>({});
  const [selected, setSelected] = useState<{ wing: string; room?: string } | null>(null);
  const relationships = useRelationships(companyId);

  const fetchTaxonomy = useCallback(async () => {
    const res = await axios.get(`/api/memory/taxonomy?company_id=${companyId}`);
    setTaxonomy(res.data?.taxonomy || {});
  }, [companyId]);

  useEffect(() => {
    fetchTaxonomy().catch(() => setTaxonomy({}));
  }, [fetchTaxonomy]);

  return (
    <div className="grid grid-cols-12 gap-4 h-full">
      <div className="col-span-3 flex flex-col gap-4 overflow-y-auto">
        <div className="bg-white rounded-lg border shadow-sm p-4">
          <h3 className="text-xs font-semibold text-gray-400 uppercase tracking-wider mb-2">
            Wings & Rooms <span className="text-gray-300">· includes empty ones</span>
          </h3>
          {Object.keys(taxonomy).length === 0 && <p className="text-sm text-gray-400">Palace is empty — no wings provisioned yet.</p>}
          {Object.entries(taxonomy).map(([wing, rooms]) => (
            <div key={wing} className="mb-2">
              <button onClick={() => setSelected({ wing })} className={`text-sm font-medium w-full text-left px-1 py-0.5 rounded ${selected?.wing === wing && !selected?.room ? 'bg-indigo-50 text-indigo-600' : 'text-gray-700 hover:bg-gray-50'}`}>
                {wing}
              </button>
              {Object.entries(rooms).map(([room, count]) => (
                <button key={room} onClick={() => setSelected({ wing, room })} className={`text-sm w-full text-left pl-4 pr-1 py-0.5 rounded flex justify-between ${selected?.wing === wing && selected?.room === room ? 'bg-indigo-50 text-indigo-600' : count === 0 ? 'text-gray-300 hover:bg-gray-50' : 'text-gray-500 hover:bg-gray-50'}`}>
                  <span>{room}</span>
                  <span className="text-xs text-gray-400">{count}</span>
                </button>
              ))}
            </div>
          ))}
        </div>
        <div className="bg-white rounded-lg border shadow-sm p-4">
          <RelationshipsPanel edges={relationships} />
        </div>
      </div>
      <div className="col-span-9">
        {!selected && (
          <div className="bg-white rounded-lg border shadow-sm p-8 text-center text-sm text-gray-400">
            Select a wing or room on the left.
          </div>
        )}
        {selected && <DrawerBrowser companyId={companyId} wing={selected.wing} room={selected.room} />}
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

// ---- Graph (interactive: click a node to browse its drawers, combined with Explorer-style content view) ----

const GraphTab: React.FC<{ companyId: number }> = ({ companyId }) => {
  const [nodes, setNodes] = useState<Node[]>([]);
  const [edges, setEdges] = useState<Edge[]>([]);
  const [rawNodes, setRawNodes] = useState<GraphNode[]>([]);
  const [rawEdges, setRawEdges] = useState<GraphEdge[]>([]);
  const [selected, setSelected] = useState<GraphNode | null>(null);

  const load = useCallback(() => {
    axios.get(`/api/memory/graph?company_id=${companyId}`).then((res) => {
      const gNodes: GraphNode[] = res.data?.nodes || [];
      const gEdges: GraphEdge[] = res.data?.edges || [];
      setRawNodes(gNodes);
      setRawEdges(gEdges);

      // Simple layout: wings in a column, their rooms fanned to the right.
      const wings = gNodes.filter((n) => n.kind === 'wing');
      const rooms = gNodes.filter((n) => n.kind === 'room');
      const laidOut: Node[] = [];
      wings.forEach((wingNode, wi) => {
        laidOut.push({
          id: wingNode.id,
          position: { x: 50, y: wi * 220 + 40 },
          data: { label: <NodeLabel label={wingNode.label} count={wingNode.drawer_count} bold /> },
          style: { background: '#eef2ff', border: '1px solid #6366f1', borderRadius: 8, padding: 4 },
        });
        const myRooms = rooms.filter((room) => room.id.startsWith(wingNode.id + '/'));
        myRooms.forEach((room, ri) => {
          laidOut.push({
            id: room.id,
            position: { x: 340 + (ri % 3) * 200, y: wi * 220 + Math.floor(ri / 3) * 70 },
            data: { label: <NodeLabel label={room.label} count={room.drawer_count} empty={room.drawer_count === 0} /> },
            style: { borderRadius: 8, padding: 4, opacity: room.drawer_count === 0 ? 0.55 : 1 },
          });
        });
      });
      setNodes(laidOut);
      setEdges(gEdges.map((edge, i) => ({
        id: `edge-${i}`,
        source: edge.from,
        target: edge.to,
        label: edge.label,
        animated: edge.kind === 'tunnel',
        style: edge.kind === 'tunnel' ? { stroke: '#f59e0b' } : edge.kind === 'hallway' ? { stroke: '#10b981' } : undefined,
      })));
    }).catch(() => { setNodes([]); setEdges([]); setRawNodes([]); setRawEdges([]); });
  }, [companyId]);

  useEffect(() => { load(); }, [load]);

  const onNodeClick = useCallback((_: unknown, node: Node) => {
    const match = rawNodes.find((n) => n.id === node.id);
    if (match) setSelected(match);
  }, [rawNodes]);

  if (nodes.length === 0) {
    return <p className="text-sm text-gray-400">The palace graph is empty — wings and rooms appear as memories are filed.</p>;
  }

  const relationshipEdges = rawEdges.filter((e) => e.kind !== 'contains');

  return (
    <div className="grid grid-cols-12 gap-4 h-full" style={{ minHeight: 600 }}>
      <div className="col-span-7 flex flex-col gap-4">
        <div className="bg-white rounded-lg border shadow-sm flex-1" style={{ height: '440px' }}>
          <ReactFlow nodes={nodes} edges={edges} onNodeClick={onNodeClick} fitView proOptions={{ hideAttribution: true }}>
            <Background />
            <Controls />
          </ReactFlow>
        </div>
        <div className="bg-white rounded-lg border shadow-sm p-4">
          <RelationshipsPanel edges={relationshipEdges} />
        </div>
      </div>
      <div className="col-span-5">
        {!selected && (
          <div className="bg-white rounded-lg border shadow-sm p-8 text-center text-sm text-gray-400 h-full flex items-center justify-center">
            Click a wing or room node to browse its drawers.
          </div>
        )}
        {selected && (
          <DrawerBrowserPanel companyId={companyId} node={selected} onClose={() => setSelected(null)} />
        )}
      </div>
    </div>
  );
};

const NodeLabel: React.FC<{ label: string; count: number; bold?: boolean; empty?: boolean }> = ({ label, count, bold, empty }) => (
  <div className="flex items-center justify-between gap-2 text-sm" style={{ fontWeight: bold ? 600 : 400 }}>
    <span className={empty ? 'text-gray-400' : ''}>{label}</span>
    <span className={`text-xs px-1.5 py-0.5 rounded-full font-semibold ${empty ? 'bg-gray-100 text-gray-400' : 'bg-indigo-100 text-indigo-700'}`}>{count}</span>
  </div>
);

// Single-column variant of DrawerBrowser used in Graph's side panel — same
// data/actions, laid out list-above-content since the panel is narrower.
const DrawerBrowserPanel: React.FC<{ companyId: number; node: GraphNode; onClose: () => void }> = ({ companyId, node, onClose }) => {
  const wing = node.wing!;
  const room = node.kind === 'room' ? node.room : undefined;
  return (
    <div className="bg-white rounded-lg border shadow-sm h-full flex flex-col overflow-hidden">
      <div className="flex justify-between items-center px-4 py-2 border-b">
        <div className="text-sm font-medium text-gray-700">
          {node.label} <span className="text-xs text-gray-400">({node.drawer_count} drawers)</span>
        </div>
        <button onClick={onClose} className="text-gray-400 hover:text-gray-600"><X size={16} /></button>
      </div>
      <div className="flex-1 overflow-y-auto p-2">
        <DrawerBrowser companyId={companyId} wing={wing} room={room} title="Drawers" />
      </div>
    </div>
  );
};

// ---- Facts ----

const FactsTab: React.FC<{ companyId: number }> = ({ companyId }) => {
  const [entity, setEntity] = useState('');
  const [asOf, setAsOf] = useState('');
  const [facts, setFacts] = useState<Fact[]>([]);
  const [loaded, setLoaded] = useState(false);
  const [expanded, setExpanded] = useState<number | null>(null);

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
      <div className="bg-blue-50 border border-blue-200 rounded-lg p-3 flex items-start text-sm text-blue-700">
        <Info size={16} className="mr-2 mt-0.5 flex-shrink-0" />
        <div>
          Facts are the palace's <strong>knowledge graph</strong> — structured subject/predicate/object triples
          (e.g. "task-dec-59 — approach — implemented"), separate from the verbatim memory drawers in Explorer/Search.
          <strong> The "Object" column IS the fact's content</strong> — it's the value being asserted. Click a row
          if the object text is truncated to see it in full. A fact stays "current" until something invalidates it;
          invalidating closes its validity window but never deletes it — see the full history via a date in "as of".
        </div>
      </div>
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
              <th className="px-4 py-2">Object (fact content)</th>
              <th className="px-4 py-2">Valid</th>
              <th className="px-4 py-2"></th>
            </tr>
          </thead>
          <tbody>
            {facts.length === 0 && loaded && (
              <tr><td colSpan={5} className="px-4 py-6 text-center text-gray-400">No facts recorded yet.</td></tr>
            )}
            {facts.map((f, i) => (
              <React.Fragment key={i}>
                <tr className="border-t cursor-pointer hover:bg-gray-50" onClick={() => setExpanded(expanded === i ? null : i)}>
                  <td className="px-4 py-2">{f.subject}</td>
                  <td className="px-4 py-2 text-gray-500">{f.predicate}</td>
                  <td className="px-4 py-2 max-w-md truncate">{f.object}</td>
                  <td className="px-4 py-2 text-xs">
                    {f.current === false || f.valid_to ? (
                      <span className="inline-flex items-center px-2 py-0.5 rounded text-xs font-medium bg-gray-100 text-gray-500">ended {f.valid_to ? String(f.valid_to).slice(0, 10) : ''}</span>
                    ) : (
                      <span className="inline-flex items-center px-2 py-0.5 rounded text-xs font-medium bg-green-100 text-green-700">current</span>
                    )}
                  </td>
                  <td className="px-4 py-2 text-right">
                    {(f.current !== false && !f.valid_to) && (
                      <button onClick={(e) => { e.stopPropagation(); invalidate(f); }} className="text-xs text-amber-600 hover:text-amber-800">invalidate</button>
                    )}
                  </td>
                </tr>
                {expanded === i && (
                  <tr className="bg-gray-50 border-t">
                    <td colSpan={5} className="px-4 py-3">
                      <div className="text-xs text-gray-400 uppercase tracking-wider mb-1">Full object content</div>
                      <pre className="text-sm whitespace-pre-wrap text-gray-800">{f.object}</pre>
                    </td>
                  </tr>
                )}
              </React.Fragment>
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
  const [detail, setDetail] = useState<ActivityDetail | null>(null);
  const [detailLoading, setDetailLoading] = useState(false);

  const load = useCallback(async () => {
    const params = new URLSearchParams({ company_id: String(companyId), limit: '50' });
    if (kind) params.set('kind', kind);
    const res = await axios.get(`/api/memory/activity?${params}`);
    setRows(res.data?.items || []);
    setTotal(res.data?.total || 0);
  }, [companyId, kind]);

  useEffect(() => { load().catch(() => setRows([])); }, [load]);

  const openDetail = async (row: ActivityRow) => {
    setDetailLoading(true);
    setDetail(null);
    try {
      const res = await axios.get(`/api/memory/activity/${row.id}?company_id=${companyId}`);
      setDetail(res.data);
    } catch {
      setDetail({ ...row, response: '(failed to load full log)' });
    } finally {
      setDetailLoading(false);
    }
  };

  const kindPill = (k: string) => {
    const cls = k === 'write' ? 'bg-amber-100 text-amber-700' : k === 'maintenance' ? 'bg-purple-100 text-purple-700' : 'bg-green-100 text-green-700';
    return <span className={`inline-flex items-center px-2 py-0.5 rounded text-xs font-medium ${cls}`}>{k}</span>;
  };

  return (
    <div className="space-y-4">
      <div className="flex justify-between items-center">
        <div className="text-sm text-gray-500">{total} operations recorded · click a row for the full log</div>
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
          <button key={row.id} onClick={() => openDetail(row)} className="w-full px-4 py-2.5 flex items-center justify-between text-sm hover:bg-gray-50 text-left">
            <div className="flex items-center space-x-3 min-w-0">
              {kindPill(row.kind)}
              <span className="font-medium text-gray-700">{row.tool}</span>
              <span className="text-gray-400 truncate">{row.query}</span>
            </div>
            <div className="text-xs text-gray-400 flex-shrink-0 ml-4">
              {row.agent_name || 'engine'} · {row.wing}{row.result_n ? ` · ${row.result_n} results` : ''} · {new Date(row.created_at).toLocaleString()}
            </div>
          </button>
        ))}
      </div>

      {(detail || detailLoading) && (
        <div className="fixed inset-0 bg-black/30 flex items-center justify-center z-50 p-6" onClick={() => setDetail(null)}>
          <div className="bg-white rounded-lg shadow-xl max-w-2xl w-full max-h-[80vh] overflow-y-auto" onClick={(e) => e.stopPropagation()}>
            <div className="flex justify-between items-center px-5 py-3 border-b">
              <h3 className="font-semibold text-gray-800">{detail ? detail.tool : 'Loading…'}</h3>
              <button onClick={() => setDetail(null)} className="text-gray-400 hover:text-gray-600"><X size={18} /></button>
            </div>
            {detailLoading && <div className="p-6 text-sm text-gray-400">Loading full log…</div>}
            {detail && !detailLoading && (
              <div className="p-5 space-y-4 text-sm">
                <div className="grid grid-cols-2 gap-2 text-xs text-gray-500">
                  <div><span className="text-gray-400">Agent:</span> {detail.agent_name || 'engine'}</div>
                  <div><span className="text-gray-400">Kind:</span> {detail.kind}</div>
                  <div><span className="text-gray-400">Wing/Room:</span> {detail.wing}{detail.room ? ` / ${detail.room}` : ''}</div>
                  <div><span className="text-gray-400">When:</span> {new Date(detail.created_at).toLocaleString()}</div>
                  {detail.task_id ? <div><span className="text-gray-400">Task:</span> #{detail.task_id}</div> : null}
                  {detail.run_id ? <div><span className="text-gray-400">Run:</span> #{detail.run_id}</div> : null}
                </div>
                <div>
                  <h4 className="text-xs font-semibold text-gray-400 uppercase tracking-wider mb-1">Command (args)</h4>
                  <pre className="text-xs bg-gray-50 border rounded p-3 whitespace-pre-wrap overflow-x-auto">
                    {detail.args ? formatMaybeJSON(detail.args) : '(no structured args captured for this activity — see query below)'}
                  </pre>
                </div>
                <div>
                  <h4 className="text-xs font-semibold text-gray-400 uppercase tracking-wider mb-1">Query / content preview</h4>
                  <pre className="text-xs bg-gray-50 border rounded p-3 whitespace-pre-wrap overflow-x-auto">{detail.query || '(none)'}</pre>
                </div>
                <div>
                  <h4 className="text-xs font-semibold text-gray-400 uppercase tracking-wider mb-1">MemPalace response</h4>
                  <pre className="text-xs bg-gray-50 border rounded p-3 whitespace-pre-wrap overflow-x-auto">
                    {detail.response ? formatMaybeJSON(detail.response) : '(no response captured for this activity)'}
                  </pre>
                </div>
              </div>
            )}
          </div>
        </div>
      )}
    </div>
  );
};

function formatMaybeJSON(s: string): string {
  try {
    return JSON.stringify(JSON.parse(s), null, 2);
  } catch {
    return s;
  }
}

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
