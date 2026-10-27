import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import React from 'react'
import { createRoot } from 'react-dom/client'
import { act } from 'react-dom/test-utils'
import { useEnsureAdminEntries } from './useEnsureAdminEntries'
import { FakeApi } from '@/hubaccount/fakeApi'

// React's act() outside a testing library.
;(
    globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }
).IS_REACT_ACT_ENVIRONMENT = true

const mockCentral = { api: new FakeApi() }
jest.mock('@/api/useApi', () => ({ useApi: () => mockCentral.api }))

const open = () => {
    const Probe = () => {
        useEnsureAdminEntries()
        return null
    }
    const client = new QueryClient({
        defaultOptions: { queries: { retry: false } },
    })
    act(() => {
        createRoot(document.createElement('div')).render(
            <QueryClientProvider client={client}>
                <Probe />
            </QueryClientProvider>
        )
    })
}

const settle = async () => {
    for (let i = 0; i < 20; i++) {
        await act(() => new Promise((r) => setTimeout(r, 5)))
    }
}

// The facility-hub-admin entries are created, protected, when someone who manages hubs
// opens the app; not when anyone else does.
describe('useEnsureAdminEntries', () => {
    beforeEach(() => {
        mockCentral.api = new FakeApi()
    })

    it('creates the missing entries when a hub manager opens the app', async () => {
        const api = mockCentral.api
        api.store['facility-hub-admin'] = {
            accounts: { group: 'G', role: 'R', managers: 'HubGrpMgrs' },
        }
        api.groups = ['HubGrpMgrs']
        open()
        await settle()
        expect(Object.keys(api.store['facility-hub-admin']).sort()).toEqual([
            'accounts',
            'central-move',
            'release',
            'retries',
        ])
        expect(api.sharing['meta-facility-hub-admin-release']).toEqual({
            object: {
                publicAccess: 'r-------',
                userAccesses: [],
                userGroupAccesses: [{ id: 'HubGrpMgrs', access: 'rw------' }],
            },
        })
    })

    it('creates them all when a superuser opens the app first', async () => {
        mockCentral.api.superuser = true
        open()
        await settle()
        expect(
            Object.keys(mockCentral.api.store['facility-hub-admin'] ?? {})
        ).toHaveLength(4)
    })

    it('creates nothing when someone else opens the app', async () => {
        open()
        await settle()
        expect(mockCentral.api.store['facility-hub-admin']).toBeUndefined()
        expect(
            mockCentral.api.log.filter((l) => !l.startsWith('GET '))
        ).toEqual([])
    })
})
