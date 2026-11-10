import React from 'react'
import { createRoot } from 'react-dom/client'
import { act } from 'react-dom/test-utils'
import { PageError } from './PageError'
;(
    globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }
).IS_REACT_ACT_ENVIRONMENT = true

// The router passes PageError what the page threw (App.tsx's errorElement). Jest 27
// cannot load react-router-dom's data router (it reads no package exports), so the
// hook is replaced here.
const mockThrown: { error: unknown } = { error: undefined }
jest.mock('react-router-dom', () => ({
    useRouteError: () => mockThrown.error,
    useMatches: () => [],
}))

const render = () => {
    const div = document.createElement('div')
    act(() => {
        createRoot(div).render(<PageError />)
    })
    return div.textContent
}

describe('PageError', () => {
    it('says the page could not be shown, and why', () => {
        mockThrown.error = new TypeError(
            'entry.pilotHubs.map is not a function'
        )
        const text = render()
        expect(text).toContain('This page could not be shown')
        expect(text).toContain('entry.pilotHubs.map is not a function')
    })

    it('shows a thrown value that is not an Error', () => {
        mockThrown.error = 'odd'
        expect(render()).toContain('odd')
    })
})
