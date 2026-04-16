import { BrowserRouter, Routes, Route, Navigate } from 'react-router-dom';
import { Layout } from './components/Layout';
import { Dashboard } from './pages/Dashboard';
import { CompanyView } from './pages/CompanyView';
import { ProjectBoard } from './pages/ProjectBoard';
import { AgentManager } from './pages/AgentManager';
import { RunLogs } from './pages/RunLogs';
import { SkillsManager } from './pages/SkillsManager';
import { Settings } from './pages/Settings';
import { Onboarding } from './pages/Onboarding';

function App() {
  return (
    <BrowserRouter>
      <Layout>
        <Routes>
          <Route path="/onboarding" element={<Onboarding />} />
          <Route path="/" element={<Dashboard />} />
          <Route path="/tasks" element={<ProjectBoard />} />
          <Route path="/projects" element={<CompanyView />} />
          <Route path="/agents" element={<AgentManager />} />
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
