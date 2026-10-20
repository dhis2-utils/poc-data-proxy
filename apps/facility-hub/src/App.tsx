import './locales'
import { CssReset, CssVariables } from '@dhis2/ui'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import React from 'react'
import { createHashRouter, Outlet, RouterProvider } from 'react-router-dom'
import { Layout } from '@/components/layout/Layout'
import { PageWrapper } from '@/components/layout/PageWrapper'
import { ApprovalsPage } from '@/pages/ApprovalsPage'
import { HubPage } from '@/pages/HubPage'
import { HubsPage } from '@/pages/HubsPage'
import { MovesPage } from '@/pages/MovesPage'
import { SettingsPage } from '@/pages/SettingsPage'
import { VersionPage } from '@/pages/VersionPage'
import { SyncUrlWithGlobalShell } from '@/utils/SyncUrlWithGlobalShell'

const queryClient = new QueryClient()

const router = createHashRouter([
    {
        element: <SyncUrlWithGlobalShell />,
        children: [
            {
                element: <Layout />,
                children: [
                    {
                        element: (
                            <PageWrapper>
                                <Outlet />
                            </PageWrapper>
                        ),
                        children: [
                            { path: '/', element: <HubsPage /> },
                            { path: '/hubs/:id', element: <HubPage /> },
                            { path: '/approvals', element: <ApprovalsPage /> },
                            { path: '/moves', element: <MovesPage /> },
                            { path: '/version', element: <VersionPage /> },
                            { path: '/settings', element: <SettingsPage /> },
                        ],
                    },
                ],
            },
        ],
    },
])

const App = () => (
    <QueryClientProvider client={queryClient}>
        <CssReset />
        <CssVariables theme spacers colors elevations />
        <RouterProvider router={router} />
    </QueryClientProvider>
)

export default App
