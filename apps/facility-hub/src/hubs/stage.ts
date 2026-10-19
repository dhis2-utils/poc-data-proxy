import i18n from '@dhis2/d2-i18n'

/** The country's choice, facility-hub-admin/release (internal/upgrade.Choice). */
export type ReleaseChoice = {
    version: string
    pilotHubs: string[]
    percent: number
}

/** FNV-1a, 32 bits, over the UTF-8 bytes: Go's hash/fnv New32a. */
const fnv1a = (s: string): number => {
    let h = 0x811c9dc5
    for (const b of new TextEncoder().encode(s)) {
        h ^= b
        h = Math.imul(h, 0x01000193) >>> 0
    }
    return h >>> 0
}

/** Whether a hub takes the chosen version now, exactly as the hubs decide (upgrade.InStage). */
export const inStage = (c: ReleaseChoice, hubId: string): boolean =>
    c.pilotHubs.includes(hubId) || fnv1a(hubId) % 100 < c.percent

const steps = [0, 10, 50, 100]

/** The next stage: pilot hubs only, then 10 %, 50 % and all hubs. */
export const nextStage = (c: ReleaseChoice): ReleaseChoice => ({
    ...c,
    percent: steps.find((p) => p > c.percent) ?? 100,
})

export const stageLabel = (c: ReleaseChoice): string => {
    if (c.percent >= 100) {
        return i18n.t('All hubs')
    }
    if (c.percent <= 0) {
        return c.pilotHubs.length
            ? i18n.t('Pilot hubs only')
            : i18n.t('No hubs')
    }
    return c.pilotHubs.length
        ? i18n.t('Pilot hubs and {{percent}} % of hubs', { percent: c.percent })
        : i18n.t('{{percent}} % of hubs', { percent: c.percent })
}
