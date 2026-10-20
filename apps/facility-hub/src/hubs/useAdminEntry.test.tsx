import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import React from 'react'
import { createRoot } from 'react-dom/client'
import { act } from 'react-dom/test-utils'
import { useAdminEntry } from './useHubs'
import { FakeApi } from '@/hubaccount/fakeApi'

// React's act() outside a testing library.
;(
    globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }
).IS_REACT_ACT_ENVIRONMENT = true

const mockCentral = { api: new FakeApi() }
jest.mock('@/api/useApi', () => ({ useApi: () => mockCentral.api }))

// Renders useAdminEntry and returns a getter for its latest result.
const render = <T,>(
    namespace: 'facility-hub-admin' | 'facility-hub-config',
    key: string
) => {
    const out: { current?: ReturnType<typeof useAdminEntry<T>> } = {}
    const Probe = () => {
        out.current = useAdminEntry<T>(namespace, key)
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
    return () => out.current!
}

const until = async (ok: () => boolean) => {
    for (let i = 0; i < 100 && !ok(); i++) {
        await act(() => new Promise((r) => setTimeout(r, 5)))
    }
    expect(ok()).toBe(true)
}

// Each admin entry is written with writeAdminEntry's sharing: writable by the hub
// managers group that facility-hub-admin/accounts names, except accounts itself.
describe('useAdminEntry', () => {
    beforeEach(() => {
        mockCentral.api = new FakeApi()
        mockCentral.api.store['facility-hub-admin'] = {
            accounts: { group: 'G', role: 'R', managers: 'HubGrpMgrs' },
        }
    })

    it('reads a missing entry as null', async () => {
        const entry = render('facility-hub-config', 'tokens')
        await until(() => !entry().isLoading)
        expect(entry().value).toBeNull()
        expect(entry().error).toBeNull()
    })

    it('writes an entry shared with the hub managers, then reads it again', async () => {
        const entry = render<{ defaultMonths: number }>(
            'facility-hub-config',
            'retention'
        )
        // The managers group comes from the accounts entry, read alongside.
        await until(
            () =>
                !entry().isLoading &&
                mockCentral.api.log.includes(
                    'GET /dataStore/facility-hub-admin/accounts'
                )
        )
        await act(() => new Promise((r) => setTimeout(r, 5)))
        await act(() => entry().save.mutateAsync({ defaultMonths: 12 }))
        expect(
            mockCentral.api.entry('facility-hub-config', 'retention')
        ).toEqual({
            defaultMonths: 12,
        })
        expect(
            mockCentral.api.sharing['meta-facility-hub-config-retention']
        ).toEqual({
            object: {
                publicAccess: 'r-------',
                userAccesses: [],
                userGroupAccesses: [{ id: 'HubGrpMgrs', access: 'rw------' }],
            },
        })
        await until(() => entry().value?.defaultMonths === 12)
    })

    it('writes the accounts entry for superusers only', async () => {
        const entry = render<{ group: string; role: string }>(
            'facility-hub-admin',
            'accounts'
        )
        await until(() => !entry().isLoading)
        await act(() => entry().save.mutateAsync({ group: 'G2', role: 'R' }))
        expect(
            mockCentral.api.sharing['meta-facility-hub-admin-accounts']
        ).toEqual({
            object: {
                publicAccess: 'r-------',
                userAccesses: [],
                userGroupAccesses: [],
            },
        })
    })
})
