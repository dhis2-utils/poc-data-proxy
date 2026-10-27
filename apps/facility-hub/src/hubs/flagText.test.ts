import { flagSentence } from './flagText'

describe('flagSentence', () => {
    it('shows values as they are, not HTML-escaped (React escapes)', () => {
        const s = flagSentence('upgrade', {
            lastUpgrade: { result: 'failed', reason: "disk/full: can't swap" },
        })
        expect(s).toContain("disk/full: can't swap")
    })

    it('names the entries any user may write, and what to do', () => {
        const s = flagSentence('unprotected', {
            unprotected: [
                'facility-hub-admin/central-move',
                'facility-hub-registry/hub-abc',
            ],
        })
        expect(s).toContain(
            'facility-hub-admin/central-move, facility-hub-registry/hub-abc'
        )
        expect(s).toContain('sharing')
    })
})
