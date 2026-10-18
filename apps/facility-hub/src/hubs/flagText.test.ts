import { flagSentence } from './flagText'

describe('flagSentence', () => {
    it('shows values as they are, not HTML-escaped (React escapes)', () => {
        const s = flagSentence('upgrade', {
            lastUpgrade: { result: 'failed', reason: "disk/full: can't swap" },
        })
        expect(s).toContain("disk/full: can't swap")
    })
})
