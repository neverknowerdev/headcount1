import { BrowserRouter, Routes, Route, Navigate } from 'react-router-dom';
import { Layout } from './components/Layout';
import { Dashboard } from './pages/Dashboard';
import { CompanyView } from './pages/CompanyView';
import { ProjectBoard } from './pages/ProjectBoard';
import { AgentManager } from './pages/AgentManager';
import { AgentDetails } from './pages/AgentDetails';
import { RunLogs } from './pages/RunLogs';
import { ProvidersManager } from './pages/ProvidersManager';
import { SprintsManager } from './pages/SprintsManager';
import { SkillsManager } from './pages/SkillsManager';
import { Settings } from './pages/Settings';
import { AddCompany } from './pages/AddCompany';

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
          <Route path="/companies/:shortName/agents" element={<AgentManager />} />
          <Route path="/companies/:shortName/agents/:id" element={<AgentDetails />} />
          <Route path="/companies/:shortName/providers" element={<ProvidersManager />} />
          <Route path="/companies/:shortName/skills" element={<SkillsManager />} />
          <Route path="/companies/:shortName/runs" element={<RunLogs />} />
          <Route path="/companies/:shortName/settings" element={<Settings />} />
          <Route path="/" element={<Dashboard />} />
          <Route path="*" element={<Navigate to="/" replace />} />
        </Routes>
      </Layout>
    </BrowserRouter>
  );
}

export default App;
