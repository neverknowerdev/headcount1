import React, { useState, useEffect } from 'react';
import axios from 'axios';
import { Link } from 'react-router-dom';

export const Dashboard: React.FC = () => {
  const [companies, setCompanies] = useState<any[]>([]);
  const [newCompanyName, setNewCompanyName] = useState('');

  useEffect(() => {
    fetchCompanies();
  }, []);

  const fetchCompanies = async () => {
    try {
      const res = await axios.get('/api/companies');
      setCompanies(res.data || []);
    } catch (e) {
      console.error(e);
    }
  };

  const createCompany = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!newCompanyName) return;
    try {
      await axios.post('/api/companies', { name: newCompanyName });
      setNewCompanyName('');
      fetchCompanies();
    } catch (e) {
      console.error(e);
    }
  };

  return (
    <div>
      <h1 className="text-2xl font-bold mb-6">Companies</h1>

      <form onSubmit={createCompany} className="mb-8 flex gap-4">
        <input
          type="text"
          value={newCompanyName}
          onChange={(e) => setNewCompanyName(e.target.value)}
          placeholder="New Company Name"
          className="border p-2 rounded"
        />
        <button type="submit" className="bg-indigo-600 text-white px-4 py-2 rounded">
          Create
        </button>
      </form>

      <div className="grid grid-cols-1 md:grid-cols-3 gap-6">
        {companies.map(c => (
          <Link key={c.id} to={`/company/${c.id}`} className="block p-6 bg-white rounded-lg border shadow-sm hover:shadow-md transition">
            <h3 className="text-lg font-semibold">{c.name}</h3>
            <p className="text-sm text-gray-500 mt-2">ID: {c.id}</p>
          </Link>
        ))}
      </div>
    </div>
  );
};
