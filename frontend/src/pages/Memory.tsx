import React, { useEffect, useState, useCallback } from 'react';
import axios from 'axios';
import {
  Brain,
  ChevronRight,
  ChevronDown,
  BookOpen,
  Archive,
  Loader2,
  AlertCircle,
  ArrowLeft,
  Calendar,
  Tag,
} from 'lucide-react';

// ---- types ------------------------------------------------------------------

interface Taxonomy {
  [wing: string]: { [room: string]: number };
}

interface DrawerMeta {
  filed_at?: string;
  date?: string;
  agent?: string;
  topic?: string;
  type?: string;
  hall?: string;
  [key: string]: unknown;
}

interface DrawerSummary {
  drawer_id: string;
  wing: string;
  room: string;
  content_preview: string;
  metadata: DrawerMeta;
}

interface DrawerDetail {
  drawer_id: string;
  wing: string;
  room: string;
  content: string;
  metadata: DrawerMeta;
}

interface DrawerListResponse {
  drawers: DrawerSummary[];
  total: number;
  count: number;
  offset: number;
  limit: number;
}

// ---- helpers ----------------------------------------------------------------

const wingLabel = (wing: string) => {
  const stripped = wing.startsWith('wing_') ? wing.slice(5) : wing;
  return stripped.charAt(0).toUpperCase() + stripped.slice(1);
};

const roomLabel = (room: string) =>
  room.charAt(0).toUpperCase() + room.slice(1).replace(/-/g, ' ');

const formatDate = (iso?: string) => {
  if (!iso) return '';
  try {
    return new Date(iso).toLocaleString();
  } catch {
    return iso;
  }
};

// ---- sub-components ---------------------------------------------------------

const EmptyState: React.FC<{ message: string; sub?: string }> = ({ message, sub }) => (
  <div className="flex flex-col items-center justify-center h-full text-gray-400 gap-3">
    <Brain className="w-12 h-12 opacity-30" />
    <p className="text-lg font-medium text-gray-500">{message}</p>
    {sub && <p className="text-sm text-center max-w-xs">{sub}</p>}
  </div>
);

interface DrawerCardProps {
  drawer: DrawerSummary;
  onClick: () => void;
}

const DrawerCard: React.FC<DrawerCardProps> = ({ drawer, onClick }) => (
  <button
    onClick={onClick}
    className="w-full text-left p-4 bg-white border border-gray-200 rounded-lg hover:border-indigo-300 hover:shadow-sm transition-all group"
  >
    <p className="text-sm text-gray-800 font-mono leading-relaxed line-clamp-3 group-hover:text-indigo-900">
      {drawer.content_preview || <span className="italic text-gray-400">empty</span>}
    </p>
    <div className="mt-2 flex flex-wrap gap-2 text-xs text-gray-400">
      {drawer.metadata?.filed_at && (
        <span className="flex items-center gap-1">
          <Calendar className="w-3 h-3" />
          {formatDate(drawer.metadata.filed_at)}
        </span>
      )}
      {drawer.metadata?.topic && (
        <span className="flex items-center gap-1">
          <Tag className="w-3 h-3" />
          {drawer.metadata.topic}
        </span>
      )}
    </div>
  </button>
);

interface DrawerViewProps {
  drawer: DrawerDetail;
  onBack: () => void;
}

const DrawerView: React.FC<DrawerViewProps> = ({ drawer, onBack }) => (
  <div className="flex flex-col h-full">
    <div className="flex items-center gap-3 mb-4">
      <button
        onClick={onBack}
        className="flex items-center gap-1 text-sm text-indigo-600 hover:text-indigo-800"
      >
        <ArrowLeft className="w-4 h-4" /> Back
      </button>
      <span className="text-gray-300">|</span>
      <span className="text-sm text-gray-500">
        {wingLabel(drawer.wing)} / {roomLabel(drawer.room)}
      </span>
    </div>

    <div className="bg-white border border-gray-200 rounded-lg p-5 flex-1 overflow-auto">
      <pre className="text-sm text-gray-800 font-mono whitespace-pre-wrap leading-relaxed">
        {drawer.content}
      </pre>
    </div>

    {Object.keys(drawer.metadata).length > 0 && (
      <div className="mt-4 bg-gray-50 border border-gray-200 rounded-lg p-4">
        <p className="text-xs font-semibold text-gray-500 uppercase tracking-wide mb-2">Metadata</p>
        <dl className="grid grid-cols-2 gap-x-4 gap-y-1 text-xs">
          {Object.entries(drawer.metadata).map(([k, v]) => (
            <React.Fragment key={k}>
              <dt className="text-gray-400">{k}</dt>
              <dd className="text-gray-700 font-mono truncate">{String(v)}</dd>
            </React.Fragment>
          ))}
        </dl>
      </div>
    )}
  </div>
);

// ---- main component ---------------------------------------------------------

export const Memory: React.FC = () => {
  const [available, setAvailable] = useState<boolean | null>(null);
  const [taxonomy, setTaxonomy] = useState<Taxonomy>({});
  const [expandedWings, setExpandedWings] = useState<Set<string>>(new Set());
  const [selectedWing, setSelectedWing] = useState<string | null>(null);
  const [selectedRoom, setSelectedRoom] = useState<string | null>(null);
  const [drawers, setDrawers] = useState<DrawerSummary[]>([]);
  const [selectedDrawer, setSelectedDrawer] = useState<DrawerDetail | null>(null);
  const [loading, setLoading] = useState(true);
  const [drawersLoading, setDrawersLoading] = useState(false);
  const [drawerLoading, setDrawerLoading] = useState(false);

  // Load taxonomy on mount
  useEffect(() => {
    axios
      .get<{ available: boolean; data: { taxonomy: Taxonomy } | null }>('/api/memory/taxonomy')
      .then((res) => {
        setAvailable(res.data.available);
        if (res.data.available && res.data.data?.taxonomy) {
          const tax = res.data.data.taxonomy;
          setTaxonomy(tax);
          // Auto-expand the first wing
          const wings = Object.keys(tax);
          if (wings.length > 0) {
            setExpandedWings(new Set([wings[0]]));
          }
        }
      })
      .catch(() => setAvailable(false))
      .finally(() => setLoading(false));
  }, []);

  // Load drawers when room is selected
  const selectRoom = useCallback(async (wing: string, room: string) => {
    setSelectedWing(wing);
    setSelectedRoom(room);
    setSelectedDrawer(null);
    setDrawers([]);
    setDrawersLoading(true);
    try {
      const res = await axios.get<DrawerListResponse>('/api/memory/drawers', {
        params: { wing, room, limit: 50 },
      });
      setDrawers(res.data.drawers ?? []);
    } finally {
      setDrawersLoading(false);
    }
  }, []);

  // Load full drawer content
  const selectDrawer = useCallback(async (id: string) => {
    setDrawerLoading(true);
    try {
      const res = await axios.get<DrawerDetail>('/api/memory/drawer', { params: { id } });
      setSelectedDrawer(res.data);
    } finally {
      setDrawerLoading(false);
    }
  }, []);

  const toggleWing = (wing: string) => {
    setExpandedWings((prev) => {
      const next = new Set(prev);
      if (next.has(wing)) next.delete(wing);
      else next.add(wing);
      return next;
    });
  };

  // ---- render ----------------------------------------------------------------

  if (loading) {
    return (
      <div className="flex items-center justify-center h-64">
        <Loader2 className="w-6 h-6 animate-spin text-indigo-500" />
      </div>
    );
  }

  if (available === false) {
    return (
      <div className="max-w-2xl mx-auto mt-16 p-8 bg-white rounded-lg border border-amber-200 text-center space-y-4">
        <AlertCircle className="w-10 h-10 text-amber-500 mx-auto" />
        <h2 className="text-xl font-semibold text-gray-800">MemPalace Not Available</h2>
        <p className="text-gray-500 text-sm">
          The <code className="bg-gray-100 px-1 rounded">mempalace-mcp</code> binary was not
          found. Install it with:
        </p>
        <pre className="bg-gray-50 border border-gray-200 rounded p-3 text-sm font-mono text-left">
          uv tool install mempalace
        </pre>
        <p className="text-gray-400 text-xs">
          After installation, agents will automatically start storing memories here.
        </p>
      </div>
    );
  }

  const wings = Object.keys(taxonomy).sort();

  return (
    <div className="flex h-[calc(100vh-4rem)] overflow-hidden">
      {/* ── Left panel: Wing / Room tree ── */}
      <div className="w-64 flex-shrink-0 bg-white border-r overflow-y-auto">
        <div className="p-4 border-b">
          <div className="flex items-center gap-2 text-indigo-600">
            <Brain className="w-5 h-5" />
            <span className="font-semibold text-sm">Palace</span>
          </div>
          <p className="text-xs text-gray-400 mt-1">
            {wings.length} wing{wings.length !== 1 ? 's' : ''}
          </p>
        </div>

        {wings.length === 0 ? (
          <p className="text-xs text-gray-400 p-4">No memories stored yet.</p>
        ) : (
          <nav className="py-2">
            {wings.map((wing) => {
              const rooms = taxonomy[wing];
              const isExpanded = expandedWings.has(wing);
              return (
                <div key={wing}>
                  {/* Wing row */}
                  <button
                    onClick={() => toggleWing(wing)}
                    className="w-full flex items-center gap-2 px-4 py-2 text-sm font-medium text-gray-700 hover:bg-gray-50"
                  >
                    {isExpanded ? (
                      <ChevronDown className="w-4 h-4 text-gray-400 flex-shrink-0" />
                    ) : (
                      <ChevronRight className="w-4 h-4 text-gray-400 flex-shrink-0" />
                    )}
                    <Archive className="w-4 h-4 text-indigo-400 flex-shrink-0" />
                    <span className="truncate">{wingLabel(wing)}</span>
                  </button>

                  {/* Room rows */}
                  {isExpanded &&
                    Object.keys(rooms)
                      .sort()
                      .map((room) => {
                        const isActive = selectedWing === wing && selectedRoom === room;
                        return (
                          <button
                            key={room}
                            onClick={() => selectRoom(wing, room)}
                            className={`w-full flex items-center gap-2 pl-10 pr-4 py-1.5 text-sm ${
                              isActive
                                ? 'bg-indigo-50 text-indigo-700 font-medium'
                                : 'text-gray-600 hover:bg-gray-50'
                            }`}
                          >
                            <BookOpen className="w-3.5 h-3.5 flex-shrink-0 opacity-60" />
                            <span className="truncate flex-1 text-left">{roomLabel(room)}</span>
                            <span className="text-xs text-gray-400">{rooms[room]}</span>
                          </button>
                        );
                      })}
                </div>
              );
            })}
          </nav>
        )}
      </div>

      {/* ── Right panel: Drawer list or detail ── */}
      <div className="flex-1 overflow-hidden flex flex-col">
        {!selectedRoom ? (
          <EmptyState
            message="Select a room"
            sub="Click on any room in the left panel to view its drawers."
          />
        ) : selectedDrawer ? (
          <div className="flex-1 p-6 overflow-auto">
            {drawerLoading ? (
              <div className="flex items-center justify-center h-32">
                <Loader2 className="w-5 h-5 animate-spin text-indigo-400" />
              </div>
            ) : (
              <DrawerView drawer={selectedDrawer} onBack={() => setSelectedDrawer(null)} />
            )}
          </div>
        ) : (
          <div className="flex-1 flex flex-col overflow-hidden">
            <div className="px-6 py-4 border-b bg-white flex items-center gap-2">
              <Archive className="w-4 h-4 text-indigo-400" />
              <span className="font-medium text-gray-800">
                {wingLabel(selectedWing!)} / {roomLabel(selectedRoom)}
              </span>
              <span className="text-sm text-gray-400 ml-auto">
                {drawers.length} drawer{drawers.length !== 1 ? 's' : ''}
              </span>
            </div>

            <div className="flex-1 overflow-y-auto p-6">
              {drawersLoading ? (
                <div className="flex items-center justify-center h-32">
                  <Loader2 className="w-5 h-5 animate-spin text-indigo-400" />
                </div>
              ) : drawers.length === 0 ? (
                <EmptyState message="No drawers" sub="This room has no stored memories." />
              ) : (
                <div className="space-y-3">
                  {drawers.map((d) => (
                    <DrawerCard key={d.drawer_id} drawer={d} onClick={() => selectDrawer(d.drawer_id)} />
                  ))}
                </div>
              )}
            </div>
          </div>
        )}
      </div>
    </div>
  );
};
