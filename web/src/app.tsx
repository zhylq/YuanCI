import { createBrowserRouter, RouterProvider } from 'react-router-dom'
import { Layout } from './layout'
import { HomePage, PipelinePage } from './pages/account'
import { AuthSettingsPage } from './pages/auth-settings'
import { LoginPage } from './components/auth-boundary'
import { ProjectDetailPage, ProjectsPage, RunDetailPage } from './pages/projects'
import { RepositorySettingsPage } from './pages/repository-settings'

const router = createBrowserRouter([
  { path: '/', element: <Layout />, children: [
    { index: true, element: <HomePage /> },
    { path: 'pipelines/new', element: <PipelinePage /> },
    { path: 'setup', element: <AuthSettingsPage setup /> },
    { path: 'settings/auth', element: <AuthSettingsPage /> },
    { path: 'settings/repositories', element: <RepositorySettingsPage /> },
    { path: 'login', element: <LoginPage /> },
    { path: 'projects', element: <ProjectsPage /> },
    { path: 'projects/:projectID', element: <ProjectDetailPage /> },
    { path: 'projects/:projectID/runs/:runID', element: <RunDetailPage /> },
  ] },
])

export function App() { return <RouterProvider router={router} /> }
