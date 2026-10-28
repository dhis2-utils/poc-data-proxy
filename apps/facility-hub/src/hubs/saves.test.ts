/**
 * @jest-environment node
 */
import { Day } from './flags'
import { saveTotals } from './saves'

const day = (
    date: string,
    savedOnline?: number,
    savedOffline?: number
): Day => ({
    date,
    syncs: 1,
    outages: 0,
    offlineMinutes: 0,
    longestOfflineMinutes: 0,
    pendingPeak: 0,
    savedOnline,
    savedOffline,
})

describe('saveTotals', () => {
    const now = new Date('2026-10-28T12:00:00Z')
    it('adds up today and the last 30 days, today included', () => {
        const history = [
            day('2026-09-28', 100, 100), // 31 days ago: left out
            day('2026-09-29', 5, 1),
            day('2026-10-27', 7),
            day('2026-10-28', 2, 3),
        ]
        expect(saveTotals(history, now)).toEqual({
            today: { online: 2, offline: 3 },
            last30: { online: 14, offline: 4 },
        })
    })
    it('counts nothing for days without saves or hubs that do not report them', () => {
        expect(saveTotals([day('2026-10-28')], now)).toEqual({
            today: { online: 0, offline: 0 },
            last30: { online: 0, offline: 0 },
        })
        expect(saveTotals(undefined, now)).toEqual({
            today: { online: 0, offline: 0 },
            last30: { online: 0, offline: 0 },
        })
    })
})
