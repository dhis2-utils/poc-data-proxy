import React from 'react'
import { createRoot, Root } from 'react-dom/client'
import { act } from 'react-dom/test-utils'
import { VersionPage } from './VersionPage'
import { ReleaseChoice } from '@/hubs/stage'

// React's act() outside a testing library.
;(
    globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }
).IS_REACT_ACT_ENVIRONMENT = true

const mockState: { choice: ReleaseChoice | null } = { choice: null }
jest.mock('@/hubs/useHubs', () => ({
    useHubs: () => ({ hubs: [], isLoading: false }),
    useAdminEntry: () => ({
        value: mockState.choice,
        isLoading: false,
        error: null,
        update: { mutate: jest.fn(), isLoading: false },
    }),
}))
jest.mock('@/components/hub/ManagersOnly', () => ({
    ManagersOnly: ({ children }: { children: React.ReactNode }) => children,
}))
// No GitHub releases listed.
jest.mock('@tanstack/react-query', () => ({
    ...jest.requireActual('@tanstack/react-query'),
    useQuery: () => ({ data: [] }),
}))
jest.mock('react-router-dom', () => ({ Link: () => null }))

const versionInput = (div: HTMLElement) =>
    div.querySelector('input[name=version]') as HTMLInputElement

// Types into a React-controlled input.
const type = (input: HTMLInputElement, value: string) => {
    Object.getOwnPropertyDescriptor(
        HTMLInputElement.prototype,
        'value'
    )!.set!.call(input, value)
    input.dispatchEvent(new Event('input', { bubbles: true }))
}

describe('the version form', () => {
    let div: HTMLDivElement
    let root: Root
    const render = () =>
        act(() => {
            root.render(<VersionPage />)
        })
    beforeEach(() => {
        mockState.choice = null
        div = document.createElement('div')
        root = createRoot(div)
    })

    it('keeps what was typed when the empty choice is created meanwhile', () => {
        render()
        act(() => type(versionInput(div), '9.9.9'))
        expect(versionInput(div).value).toBe('9.9.9')
        // The app creates the missing entry, empty, when a manager opens it.
        mockState.choice = { version: '', pilotHubs: [], percent: 0 }
        render()
        expect(versionInput(div).value).toBe('9.9.9')
    })

    it('starts from a version chosen', () => {
        render()
        mockState.choice = { version: '1.2.3', pilotHubs: [], percent: 0 }
        render()
        expect(versionInput(div).value).toBe('1.2.3')
    })
})
