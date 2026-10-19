import { inStage, ReleaseChoice } from './stage'
import { HubRow } from './useHubs'

export type Progress = {
    /** Hubs the current stage includes (retired hubs left out). */
    inStage: number
    /** Of those, hubs that report the chosen version. */
    onVersion: number
    /** Of those, hubs whose last upgrade to the chosen version failed. */
    failed: { id: string; reason: string }[]
    /** The rest: in the stage, not on the version, not failed. */
    waiting: number
}

/** How far the chosen version has got, from the hubs' heartbeats. */
export const progress = (hubs: HubRow[], choice: ReleaseChoice): Progress => {
    const p: Progress = { inStage: 0, onVersion: 0, failed: [], waiting: 0 }
    for (const h of hubs) {
        if (h.retired || !inStage(choice, h.id)) {
            continue
        }
        p.inStage++
        const hb = h.heartbeat
        const up = hb?.lastUpgrade
        if (hb?.version === choice.version) {
            p.onVersion++
        } else if (up?.result === 'failed' && up.to === choice.version) {
            p.failed.push({ id: h.id, reason: up.reason ?? '' })
        } else {
            p.waiting++
        }
    }
    return p
}
