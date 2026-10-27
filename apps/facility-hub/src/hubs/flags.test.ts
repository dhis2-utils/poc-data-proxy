/**
 * @jest-environment node
 */
import { canManage, flags, Heartbeat, uid, visible } from './flags'

const now = new Date('2026-10-18T12:00:00Z')
const ago = (hours: number) =>
    new Date(now.getTime() - hours * 3600e3).toISOString()
const healthy: Heartbeat = {
    time: ago(1),
    lastPush: ago(1),
    pendingOutbox: 0,
    stuckOutbox: 0,
    issues: 0,
    diskFreeBytes: 50e9,
    usb: { present: true, writable: true },
    hubTokenExpires: ago(-24 * 30),
    lastUpgrade: { result: 'ok' },
    needsUpgrade: false,
    unprotected: [],
}
const kinds = (hb?: Heartbeat) => flags(hb, now).map((f) => f.kind)

describe('flags', () => {
    it('raises none for a healthy hub', () => {
        expect(kinds(healthy)).toEqual([])
    })

    it.each<[string, Partial<Heartbeat>]>([
        ['silent', { time: ago(49) }],
        ['pending', { pendingOutbox: 3, lastPush: ago(25) }],
        ['pending', { pendingOutbox: 3, lastPush: '' }],
        ['pending', { stuckOutbox: 1 }],
        ['refused', { issues: 2 }],
        ['disk', { diskFreeBytes: 1.5e9 }],
        ['usb', { usb: { present: false, writable: false } }],
        ['usb', { usb: { present: true, writable: false } }],
        ['usb', { usb: { present: true, writable: true, error: 'I/O error' } }],
        ['token', { hubTokenExpires: ago(1) }],
        [
            'upgrade',
            { lastUpgrade: { result: 'failed', reason: 'health check' } },
        ],
        ['needsUpgrade', { needsUpgrade: true }],
        ['unprotected', { unprotected: ['facility-hub-admin/central-move'] }],
    ])('raises %s', (kind, change) => {
        expect(kinds({ ...healthy, ...change })).toEqual([kind])
    })

    it('does not raise pending for changes pushed recently', () => {
        expect(
            kinds({ ...healthy, pendingOutbox: 3, lastPush: ago(2) })
        ).toEqual([])
    })

    it('does not raise usb when the hub has no stick configured', () => {
        expect(kinds({ ...healthy, usb: null })).toEqual([])
    })

    it('says a hub never reported', () => {
        expect(kinds(undefined)).toEqual(['never'])
    })

    it('copes with a heartbeat missing fields (older hubs)', () => {
        expect(kinds({ time: ago(1) })).toEqual([])
    })
})

describe('visible', () => {
    const hub = '/Root/Region/District/Facility'
    it("shows a hub below one of the user's org units", () => {
        expect(
            visible(hub, {
                paths: ['/Root/Other', '/Root/Region/District'],
                superuser: false,
            })
        ).toBe(true)
        expect(visible(hub, { paths: [hub], superuser: false })).toBe(true)
    })

    it('hides one elsewhere, or under a name that only starts the same', () => {
        expect(visible(hub, { paths: ['/Root/Other'], superuser: false })).toBe(
            false
        )
        expect(
            visible(hub, { paths: ['/Root/Region/Dis'], superuser: false })
        ).toBe(false)
        expect(visible(hub, { paths: [], superuser: false })).toBe(false)
    })

    it('shows every hub to a superuser, also one whose facility is unknown', () => {
        expect(visible('', { paths: [], superuser: true })).toBe(true)
    })
})

describe('uid', () => {
    it('accepts a DHIS2 identifier only', () => {
        expect(uid('DiszpKrYNg8')).toBe(true)
        for (const bad of [
            '',
            'x',
            'DiszpKrYNg8,x',
            'Diszp#rYNg8',
            '1iszpKrYNg8',
            'DiszpKrYNg80',
        ]) {
            expect(uid(bad)).toBe(false)
        }
    })
})

describe('canManage', () => {
    it('lets superusers and hub managers approve and move hubs', () => {
        expect(canManage({ superuser: true, groups: [] }, undefined)).toBe(true)
        expect(
            canManage(
                { superuser: false, groups: ['G1', 'Mgr'] },
                { managers: 'Mgr' }
            )
        ).toBe(true)
        expect(
            canManage({ superuser: false, groups: ['G1'] }, { managers: 'Mgr' })
        ).toBe(false)
        expect(
            canManage({ superuser: false, groups: ['G1'] }, { managers: '' })
        ).toBe(false)
        expect(canManage({ superuser: false, groups: [] }, null)).toBe(false)
    })
})
