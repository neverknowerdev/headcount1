import { BrowserRouter, Routes, Route, Navigate } from 'react-router-dom';
import { Layout } from './components/Layout';
import { Dashboard } from './pages/Dashboard';
import { CompanyView } from './pages/CompanyView';
import { ProjectBoard } from './pages/ProjectBoard';
import { AgentManager } from './pages/AgentManager';
import { AgentDetails } from './pages/AgentDetails';
import { RunLogs } from './pages/RunLogs';
import { RunLogDetails } from './pages/RunLogDetails';
import { ProvidersManager } from './pages/ProvidersManager';
import { SprintsManager } from './pages/SprintsManager';
import { SkillsManager } from './pages/SkillsManager';
import { MCPServers } from './pages/MCPServers';
import { Settings } from './pages/Settings';
import { Backup } from './pages/Backup';
import { Memory } from './pages/Memory';
import { AddCompany } from './pages/AddCompany';
import { ProjectSettings } from './pages/ProjectSettings';

function App() {
  return (
    <BrowserRouter>
      <Layout>
        <Routes>
          <Route path="/add-company" element={<AddCompany />} />
          <Route path="/companies/:shortName" element={<Dashboard />} />
          <Route path="/companies/:shortName/tasks" element={<ProjectBoard />} />
          <Route path="/companies/:shortName/sprints" element={<SprintsManager />} />
          <Route path="/companies/:shortName/projects" element={<CompanyView />} />
          <Route path="/companies/:shortName/projects/:id" element={<ProjectSettings />} />
          <Route path="/companies/:shortName/agents" element={<AgentManager />} />
          <Route path="/companies/:shortName/agents/:id" element={<AgentDetails />} />
          <Route path="/companies/:shortName/providers" element={<ProvidersManager />} />
          <Route path="/companies/:shortName/skills" element={<SkillsManager />} />
          <Route path="/companies/:shortName/mcp-servers" element={<MCPServers />} />
          <Route path="/companies/:shortName/runs" element={<RunLogs />} />
          <Route path="/companies/:shortName/run-logs/:id" element={<RunLogDetails />} />
          <Route path="/companies/:shortName/settings" element={<Settings />} />
          <Route path="/companies/:shortName/backup" element={<Backup />} />
          <Route path="/companies/:shortName/memory" element={<Memory />} />
          <Route path="/" element={<Dashboard />} />
          <Route path="*" element={<Navigate to="/" replace />} />
        </Routes>
      </Layout>
    </BrowserRouter>
  );
}

export default App;
