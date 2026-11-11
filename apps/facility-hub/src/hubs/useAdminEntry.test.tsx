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
    key: string,
    afterWrite?: (written: T, latest: T | null) => Promise<void>
) => {
    const out: { current?: ReturnType<typeof useAdminEntry<T>> } = {}
    const Probe = () => {
        out.current = useAdminEntry<T>(namespace, key, afterWrite)
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
        ).toMatchObject({
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

// A write based on what the page showed must not undo a change made since (review R-30).
describe('useAdminEntry update', () => {
    beforeEach(() => {
        mockCentral.api = new FakeApi()
        mockCentral.api.store['facility-hub-admin'] = {
            accounts: { group: 'G', role: 'R', managers: 'HubGrpMgrs' },
            release: { version: '0.3.0', pilotHubs: ['a'], percent: 10 },
        }
    })

    it('changes the entry as central has it now, not as it was read', async () => {
        type C = { version: string; pilotHubs: string[]; percent: number }
        const entry = render<C>('facility-hub-admin', 'release')
        await until(() => entry().value?.percent === 10)
        // Another manager stops the rollout.
        mockCentral.api.store['facility-hub-admin'].release = {
            version: '0.3.0',
            pilotHubs: [],
            percent: 0,
        }
        const seen: (C | null)[] = []
        await act(() =>
            entry().update.mutateAsync((latest) => {
                seen.push(latest)
                return { ...latest!, version: '0.3.1' }
            })
        )
        expect(seen).toEqual([{ version: '0.3.0', pilotHubs: [], percent: 0 }])
        expect(
            mockCentral.api.entry('facility-hub-admin', 'release')
        ).toMatchObject({
            version: '0.3.1',
            pilotHubs: [],
            percent: 0,
        })
        await until(() => entry().value?.version === '0.3.1')
    })

    // The Settings page shares the hub accounts group after saving: a person who leaves
    // once the page shows the save must not cut that off (the live app test did).
    it('shows the entry as saved only after afterWrite is done', async () => {
        type C = { percent: number }
        let done = () => {}
        const calls: [C, C | null][] = []
        const entry = render<C>('facility-hub-admin', 'release', (w, l) => {
            calls.push([w, l])
            return new Promise<void>((r) => (done = r))
        })
        await until(() => entry().value?.percent === 10)
        let settled = false
        act(() => {
            entry()
                .update.mutateAsync((latest) => ({ ...latest!, percent: 50 }))
                .then(() => (settled = true))
        })
        await until(() => calls.length === 1)
        expect(calls[0][0].percent).toBe(50)
        expect(calls[0][1]?.percent).toBe(10)
        // Written, but not shown as saved while afterWrite runs.
        await act(() => new Promise((r) => setTimeout(r, 20)))
        expect(entry().value?.percent).toBe(10)
        expect(settled).toBe(false)
        done()
        await until(() => settled && entry().value?.percent === 50)
    })

    it('writes nothing when the change refuses', async () => {
        const entry = render<{ percent: number }>(
            'facility-hub-admin',
            'release'
        )
        await until(() => !entry().isLoading)
        await act(() =>
            entry()
                .update.mutateAsync(() => {
                    throw new Error('changed meanwhile')
                })
                .catch(() => undefined)
        )
        await until(() => !!entry().update.error)
        expect(mockCentral.api.log.filter((l) => !l.startsWith('GET'))).toEqual(
            []
        )
    })
})
