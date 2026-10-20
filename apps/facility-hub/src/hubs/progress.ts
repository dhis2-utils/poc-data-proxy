import { inStage, ReleaseChoice } from './stage'
import { HubRow } from './useHubs'
import { compareVersions } from './versions'

export type Progress = {
    /** Hubs the current stage includes (retired hubs left out). */
    inStage: number
    /** Of those, hubs that report the chosen version or a newer one (hubs never go back). */
    onVersion: number
    /** Of those, hubs whose upgrade to the chosen version failed or was ignored (a bad
     * release): neither is tried again. */
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
        if (hb?.version && compareVersions(hb.version, choice.version) >= 0) {
            p.onVersion++
        } else if (
            (up?.result === 'failed' || up?.result === 'ignored') &&
            up.to === choice.version
        ) {
            p.failed.push({ id: h.id, reason: up.reason ?? '' })
        } else {
            p.waiting++
        }
    }
    return p
}
