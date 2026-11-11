import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import React from 'react'
import { createRoot } from 'react-dom/client'
import { act } from 'react-dom/test-utils'
import { AccountsSettingsCard } from './ApprovalsPage'
import { FakeApi } from '@/hubaccount/fakeApi'
import { accountsValue } from '@/hubs/useHubs'

// React's act() outside a testing library.
;(
    globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }
).IS_REACT_ACT_ENVIRONMENT = true

// Jest 27 cannot load react-router-dom (it reads no package exports).
jest.mock('react-router-dom', () => ({ Link: () => null }))
// Nor jsdom the cryptography it imports (no TextEncoder); approvals are not shown here.
jest.mock('@/components/hub/ApproveDialog', () => ({
    ApproveDialog: () => null,
}))
jest.mock('@/hubaccount/approve', () => ({ dismiss: jest.fn() }))

// The fake central, with the groups and roles the card offers.
class Central extends FakeApi {
    async request(method: string, path: string, body?: unknown) {
        if (path.startsWith('/userGroups?')) {
            return {
                userGroups: ['G', 'Managers', 'Owners', 'Other'].map((id) => ({
                    id,
                    displayName: `Group ${id}`,
                })),
            }
        }
        if (path.startsWith('/userRoles?')) {
            return { userRoles: [{ id: 'R', displayName: 'Role R' }] }
        }
        return super.request(method, path, body)
    }
}

const mockCentral = { api: new Central() }
jest.mock('@/api/useApi', () => ({ useApi: () => mockCentral.api }))

const LOCKED =
    'Change who is in this group in the Users app, not the group itself'

const render = async () => {
    const div = document.createElement('div')
    const client = new QueryClient({
        defaultOptions: { queries: { retry: false } },
    })
    act(() => {
        createRoot(div).render(
            <QueryClientProvider client={client}>
                <AccountsSettingsCard />
            </QueryClientProvider>
        )
    })
    for (let i = 0; i < 100 && !div.textContent?.includes('Save'); i++) {
        await act(() => new Promise((r) => setTimeout(r, 5)))
    }
    return div
}

const count = (text: string, part: string) => text.split(part).length - 1

// A hub managers or hub owners group, once named, is not changed: central shares move
// keys, settings, tokens and registry entries with it, and none of that would follow.
describe('the hub managers and hub owners groups', () => {
    beforeEach(() => {
        mockCentral.api = new Central()
        mockCentral.api.superuser = true
    })

    it('show as fixed once named', async () => {
        mockCentral.api.store['facility-hub-admin'] = {
            accounts: {
                group: 'G',
                role: 'R',
                managers: 'Managers',
                owners: 'Owners',
            },
        }
        const div = await render()
        const text = div.textContent ?? ''
        expect(count(text, LOCKED)).toBe(2)
        // The two groups only: the hub accounts group and role stay open.
        expect(div.querySelectorAll('.root.disabled')).toHaveLength(2)
        expect(text).toContain('Group Managers')
        expect(text).toContain('Group Owners')
    })

    it('can be chosen while not named', async () => {
        mockCentral.api.store['facility-hub-admin'] = {
            accounts: { group: 'G', role: 'R', managers: 'Managers' },
        }
        expect(count((await render()).textContent ?? '', LOCKED)).toBe(1)
        mockCentral.api.store['facility-hub-admin'] = {
            accounts: { group: 'G', role: 'R' },
        }
        const div = await render()
        expect(count(div.textContent ?? '', LOCKED)).toBe(0)
        expect(div.querySelectorAll('.root.disabled')).toHaveLength(0)
    })

    it('stay as central has them when saved', () => {
        const latest = {
            group: 'G',
            role: 'R',
            managers: 'Managers',
            owners: 'Owners',
        }
        // A page loaded before they were named, or changed by hand.
        expect(
            accountsValue(
                { group: 'G2', role: 'R2', managers: 'Other', owners: '' },
                latest
            )
        ).toEqual({
            group: 'G2',
            role: 'R2',
            managers: 'Managers',
            owners: 'Owners',
        })
        // Named for the first time.
        expect(
            accountsValue(
                { group: 'G', role: 'R', managers: 'Managers', owners: '' },
                { group: 'G', role: 'R' }
            )
        ).toEqual({ group: 'G', role: 'R', managers: 'Managers' })
        expect(
            accountsValue(
                { group: 'G', role: 'R', managers: '', owners: '' },
                null
            )
        ).toEqual({ group: 'G', role: 'R' })
    })
})
