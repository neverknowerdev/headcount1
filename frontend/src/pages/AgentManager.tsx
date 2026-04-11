import React, { useState, useEffect } from 'react';
import axios from 'axios';

export const AgentManager: React.FC = () => {
  const [agents, setAgents] = useState<any[]>([]);
  const [companies, setCompanies] = useState<any[]>([]);

  const [companyId, setCompanyId] = useState('');
  const [name, setName] = useState('');
  const [systemPrompt, setSystemPrompt] = useState('');
  const [model, setModel] = useState('gpt-4');

  useEffect(() => {
    fetchCompanies();
  }, []);

  useEffect(() => {
    if (companyId) {
      fetchAgents();
    }
  }, [companyId]);

  const fetchCompanies = async () => {
    const res = await axios.get('/api/companies');
    setCompanies(res.data || []);
    if (res.data?.length > 0) setCompanyId(res.data[0].id.toString());
  };

  const fetchAgents = async () => {
    const res = await axios.get(`/api/agents?company_id=${companyId}`);
    setAgents(res.data || []);
  };

  const createAgent = async (e: React.FormEvent) => {
    e.preventDefault();
    await axios.post('/api/agents', {
      company_id: parseInt(companyId),
      name,
      system_prompt: systemPrompt,
      model
    });
    setName('');
    setSystemPrompt('');
    fetchAgents();
  };

  return (
    <div>
      <h1 className="text-2xl font-bold mb-6">Agent Management</h1>

      <div className="mb-8">
        <label className="block mb-2 text-sm font-medium">Select Company</label>
        <select
          value={companyId}
          onChange={(e) => setCompanyId(e.target.value)}
          className="border p-2 rounded w-64"
        >
          {companies.map(c => (
            <option key={c.id} value={c.id}>{c.name}</option>
          ))}
        </select>
      </div>

      <div className="grid grid-cols-1 lg:grid-cols-2 gap-8">
        <div className="bg-white p-6 rounded-lg border shadow-sm">
          <h2 className="text-lg font-semibold mb-4">Create New Agent</h2>
          <form onSubmit={createAgent} className="space-y-4">
            <div>
              <label className="block text-sm font-medium text-gray-700">Name</label>
              <input type="text" value={name} onChange={e => setName(e.target.value)} required className="mt-1 block w-full border rounded-md shadow-sm p-2" />
            </div>
            <div>
              <label className="block text-sm font-medium text-gray-700">System Prompt</label>
              <textarea value={systemPrompt} onChange={e => setSystemPrompt(e.target.value)} required rows={4} className="mt-1 block w-full border rounded-md shadow-sm p-2" />
            </div>
            <div>
              <label className="block text-sm font-medium text-gray-700">Model</label>
              <input type="text" value={model} onChange={e => setModel(e.target.value)} required className="mt-1 block w-full border rounded-md shadow-sm p-2" />
            </div>
            <button type="submit" className="w-full bg-indigo-600 text-white px-4 py-2 rounded">Create Agent</button>
          </form>
        </div>

        <div className="space-y-4">
          <h2 className="text-lg font-semibold">Existing Agents</h2>
          {agents.map(agent => (
            <div key={agent.id} className="bg-white p-4 rounded-lg border shadow-sm">
              <h3 className="font-bold text-indigo-600">{agent.name}</h3>
              <p className="text-sm text-gray-500 mt-1">Model: {agent.model?.String || agent.model}</p>
              <div className="mt-2 text-sm text-gray-700 bg-gray-50 p-2 rounded overflow-x-auto max-h-32">
                {agent.system_prompt}
              </div>
            </div>
          ))}
        </div>
      </div>
    </div>
  );
};
