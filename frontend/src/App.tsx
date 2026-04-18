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
          <Route path="/" element={<Dashboard />} />
          <Route path="/tasks" element={<ProjectBoard />} />
          <Route path="/sprints" element={<SprintsManager />} />
          <Route path="/projects" element={<CompanyView />} />
          <Route path="/agents" element={<AgentManager />} />
          <Route path="/agents/:id" element={<AgentDetails />} />
          <Route path="/providers" element={<ProvidersManager />} />
          <Route path="/skills" element={<SkillsManager />} />
          <Route path="/runs" element={<RunLogs />} />
          <Route path="/settings" element={<Settings />} />
          <Route path="*" element={<Navigate to="/" replace />} />
        </Routes>
      </Layout>
    </BrowserRouter>
  );
}

export default App;
