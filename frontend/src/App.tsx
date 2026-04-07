import { BrowserRouter, Routes, Route } from 'react-router-dom';
import { Layout } from './components/Layout';
import { Dashboard } from './pages/Dashboard';
import { CompanyView } from './pages/CompanyView';
import { ProjectBoard } from './pages/ProjectBoard';
import { AgentManager } from './pages/AgentManager';
import { RunLogs } from './pages/RunLogs';

function App() {
  return (
    <BrowserRouter>
      <Layout>
        <Routes>
          <Route path="/" element={<Dashboard />} />
          <Route path="/company/:companyId" element={<CompanyView />} />
          <Route path="/project/:projectId" element={<ProjectBoard />} />
          <Route path="/agents" element={<AgentManager />} />
          <Route path="/runs" element={<RunLogs />} />
        </Routes>
      </Layout>
    </BrowserRouter>
  );
}

export default App;
