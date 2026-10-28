import { Day } from './flags'

/** Saves passed straight to central while online, and stored on the hub and sent later. */
export type Saves = { online: number; offline: number }

const dayMs = 24 * 3600e3

/**
 * Adds up a hub's saves for today and the last 30 days (today included), from the
 * daily history in its heartbeat. Days are UTC, as the hub keeps them. Hubs of older
 * versions report no saves: they count as none.
 */
export const saveTotals = (
    history: Day[] | undefined,
    now = new Date()
): { today: Saves; last30: Saves } => {
    const today = now.toISOString().slice(0, 10)
    const from = new Date(now.getTime() - 29 * dayMs).toISOString().slice(0, 10)
    const totals = {
        today: { online: 0, offline: 0 },
        last30: { online: 0, offline: 0 },
    }
    for (const d of history ?? []) {
        if (d.date < from || d.date > today) {
            continue
        }
        totals.last30.online += d.savedOnline ?? 0
        totals.last30.offline += d.savedOffline ?? 0
        if (d.date === today) {
            totals.today.online += d.savedOnline ?? 0
            totals.today.offline += d.savedOffline ?? 0
        }
    }
    return totals
}
