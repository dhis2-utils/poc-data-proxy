/**
 * @jest-environment node
 */
import { captureLink, closerOf, closersOf, closerText } from './issues'

describe('closerOf', () => {
    it.each([
        ['E1064', 'author'],
        ['E1020', 'author'],
        ['E1039', 'author'],
        ['E1000', 'central'],
        ['E1047', 'central'],
        ['NO_LOGIN', 'central'],
        ['E2017', 'central'],
        ['E1082', 'nobody'],
        ['E1114', 'nobody'],
        ['E4017', 'nobody'],
        ['E5000', 'parent'],
    ])('%s is closed by %s', (code, closer) => {
        expect(closerOf(code)).toBe(closer)
    })

    it('knows no code outside the table', () => {
        expect(closerOf('E9999')).toBeUndefined()
        expect(closerOf('')).toBeUndefined()
    })
})

describe('closersOf', () => {
    it('names each closer once, in the order of the codes', () => {
        expect(closersOf(['E1082', 'E1000', 'E1083'])).toEqual([
            'nobody',
            'central',
        ])
    })

    it('says a change blocked by its parent closes with it', () => {
        expect(closersOf([], 'blocked by DupTe000079')).toEqual(['parent'])
    })

    it('says nothing for codes it does not know', () => {
        expect(closersOf(['E9999'], 'rejected by central: E9999')).toEqual([])
    })
})

describe('closerText', () => {
    it("uses the plan's three texts", () => {
        expect(closerText('author')).toBe('Fixed by the author')
        expect(closerText('central')).toBe('Fixed on central')
        expect(closerText('nobody')).toBe('Fixed by nobody')
    })
})

describe('captureLink', () => {
    const base = 'https://central.example/dhis'
    const app = `${base}/dhis-web-capture/index.html#/`

    it('opens a person, an enrollment and a tracker event', () => {
        expect(
            captureLink(`${base}/`, {
                kind: 'trackedEntity',
                objectUid: 'DupTe000079',
            })
        ).toBe(`${app}enrollment?teiId=DupTe000079`)
        expect(
            captureLink(base, { kind: 'enrollment', objectUid: 'DupEn000079' })
        ).toBe(`${app}enrollment?enrollmentId=DupEn000079`)
        expect(
            captureLink(base, {
                kind: 'event',
                objectUid: 'zP9a7knGhRf',
                trackedEntity: 'yFcOhsM1Yoa',
            })
        ).toBe(`${app}enrollmentEventEdit?eventId=zP9a7knGhRf`)
    })

    it("opens an event programme's event on its own page", () => {
        expect(
            captureLink(base, {
                kind: 'event',
                objectUid: 'ohAH6BXIMad',
                trackedEntity: '',
            })
        ).toBe(`${app}viewEvent?viewEventId=ohAH6BXIMad`)
    })

    it('gives no link where Capture cannot open the record', () => {
        expect(
            captureLink(base, { kind: 'event', objectUid: 'ohAH6BXIMad' })
        ).toBeUndefined()
        expect(
            captureLink(base, { kind: 'aggregate', objectUid: 'x' })
        ).toBeUndefined()
        expect(
            captureLink(base, {
                kind: 'relationship',
                objectUid: 'Rel00000001',
            })
        ).toBeUndefined()
        expect(captureLink(base, { kind: 'enrollment' })).toBeUndefined()
    })
})
