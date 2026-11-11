import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import React from 'react'
import { createRoot } from 'react-dom/client'
import { act } from 'react-dom/test-utils'
import { RefusalsPage } from './RefusalsPage'
import { resend } from '@/hubs/refusals'
import { HubRow } from '@/hubs/useHubs'

// React's act() outside a testing library.
;(
    globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }
).IS_REACT_ACT_ENVIRONMENT = true

const hub = (id: string, count: number): HubRow =>
    ({
        id,
        registry: {},
        orgUnit: { id, path: `/Country/${id}` },
        heartbeat: {
            refused: [{ kind: 'event', program: 'P1', code: 'E1', count }],
            refusedGroups: 1,
        },
        flags: [],
        retired: false,
        waiting: false,
    }) as unknown as HubRow

const mockHubs = [hub('HubA', 3), hub('HubB', 4)]
jest.mock('@/hubs/useHubs', () => ({
    useHubs: () => ({ hubs: mockHubs, isLoading: false, error: null }),
    useAdminEntry: () => ({ value: null }),
}))
jest.mock('@/hooks/useMe', () => ({
    useMe: () => ({
        me: { username: 'manager', superuser: false, paths: ['/Country'] },
    }),
}))
jest.mock('@/hooks/useNamed', () => ({ useNamed: () => ({ list: [] }) }))
jest.mock('@/api/useApi', () => ({ useApi: () => ({}) }))
jest.mock('@/components/hub/ManagersOnly', () => ({
    ManagersOnly: ({ children }: { children: React.ReactNode }) => children,
}))
jest.mock('@/hubs/refusals', () => ({
    ...jest.requireActual('@/hubs/refusals'),
    readRetries: async () => [],
    resend: jest.fn(async () => []),
}))

const buttons = (label: string) =>
    [...document.body.querySelectorAll('button')].filter(
        (b) => b.textContent === label
    )

const click = (b: HTMLElement) =>
    act(async () => {
        b.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })

describe('group Resend', () => {
    beforeEach(async () => {
        ;(resend as jest.Mock).mockClear()
        document.body.innerHTML = ''
        const div = document.createElement('div')
        document.body.appendChild(div)
        await act(async () => {
            createRoot(div).render(
                <QueryClientProvider client={new QueryClient()}>
                    <RefusalsPage />
                </QueryClientProvider>
            )
        })
        // Lets the list of earlier requests load.
        await act(async () => {
            await new Promise((r) => setTimeout(r, 0))
        })
    })

    it('asks first, with its reach, and writes the instruction on confirm', async () => {
        await click(buttons('Resend')[0])
        expect(resend).not.toHaveBeenCalled()
        expect(document.body.textContent).toContain('Resend refused saves')
        expect(document.body.textContent).toContain(
            'this reaches 7 saves on 2 hubs'
        )
        expect(document.body.textContent).toContain('Nothing is thrown away')
        const resends = buttons('Resend')
        await click(resends[resends.length - 1])
        expect(resend).toHaveBeenCalledTimes(1)
        expect((resend as jest.Mock).mock.calls[0][1]).toMatchObject({
            kind: 'event',
            program: 'P1',
            code: 'E1',
        })
        expect((resend as jest.Mock).mock.calls[0][2].orgUnits).toEqual([
            'Country',
        ])
    })

    it('writes nothing on cancel', async () => {
        await click(buttons('Resend')[0])
        await click(buttons('Cancel')[0])
        expect(resend).not.toHaveBeenCalled()
        expect(document.body.textContent).not.toContain('Resend refused saves')
    })
})
