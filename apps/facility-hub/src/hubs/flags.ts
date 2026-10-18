// What a hub reports in its heartbeat (facility-hub-status/<hub ID>, written by
// internal/sync Status). Every field may be missing: hubs of older versions send fewer.
export type Heartbeat = {
    hubId?: string
    facility?: string
    version?: string
    serverVersion?: string
    time?: string
    lastPush?: string
    lastPull?: string
    lastSync?: string
    lastError?: string
    pending?: number
    pendingOutbox?: number
    stuckOutbox?: number
    issues?: number
    diskFreeBytes?: number
    usb?: {
        present: boolean
        writable: boolean
        lastCopy?: string
        freeBytes?: number
        error?: string
    } | null
    hubTokenOwner?: string
    hubTokenExpires?: string | null
    lastUpgrade?: {
        result: string
        from?: string
        to?: string
        reason?: string
        at?: string
    } | null
    needsUpgrade?: boolean
    refusedGroups?: number
    history?: Day[]
}

export type Day = {
    date: string
    syncs: number
    outages: number
    offlineMinutes: number
    longestOfflineMinutes: number
    pendingPeak: number
    diskFreeMinBytes?: number
}

export type FlagKind =
    | 'never'
    | 'silent'
    | 'pending'
    | 'refused'
    | 'disk'
    | 'usb'
    | 'token'
    | 'upgrade'
    | 'needsUpgrade'

export type Flag = { kind: FlagKind }

const hour = 3600e3
const silentAfter = 48 * hour
const pendingAfter = 24 * hour
const lowDisk = 2e9

const older = (iso: string | undefined | null, now: Date, ms: number) => {
    const t = iso ? Date.parse(iso) : NaN
    return Number.isNaN(t) || now.getTime() - t > ms
}

/**
 * The flags of a hub, from its heartbeat and the time (docs/superpowers/specs/
 * 2026-10-18-facility-hub-app-design.md, "Flags"). The thresholds are fixed.
 */
export const flags = (hb: Heartbeat | undefined, now: Date): Flag[] => {
    if (!hb) {
        return [{ kind: 'never' }]
    }
    const out: Flag[] = []
    const add = (kind: FlagKind, when: boolean) => when && out.push({ kind })
    add('silent', older(hb.time, now, silentAfter))
    add(
        'pending',
        ((hb.pendingOutbox ?? 0) > 0 &&
            older(hb.lastPush, now, pendingAfter)) ||
            (hb.stuckOutbox ?? 0) > 0
    )
    add('refused', (hb.issues ?? 0) > 0)
    add('disk', hb.diskFreeBytes != null && hb.diskFreeBytes < lowDisk)
    add(
        'usb',
        hb.usb != null &&
            (!hb.usb.present || !hb.usb.writable || !!hb.usb.error)
    )
    add(
        'token',
        !!hb.hubTokenExpires && Date.parse(hb.hubTokenExpires) < now.getTime()
    )
    add('upgrade', hb.lastUpgrade?.result === 'failed')
    add('needsUpgrade', hb.needsUpgrade === true)
    return out
}

/**
 * Whether a user sees a hub: its facility (by path) is at or below one of the user's
 * org units. Superusers see every hub. This is filtering in the app, not access
 * control: the namespaces hold no patient data.
 */
export const visible = (
    facilityPath: string,
    user: { paths: string[]; superuser: boolean }
): boolean =>
    user.superuser ||
    user.paths.some(
        (p) =>
            p !== '' && (facilityPath === p || facilityPath.startsWith(`${p}/`))
    )

/**
 * Whether s is a DHIS2 identifier. Hub owners write the registry: a facility that is
 * not one is shown as unknown, not sent to central (where it would fail the whole
 * lookup).
 */
export const uid = (s: string) => /^[A-Za-z][A-Za-z0-9]{10}$/.test(s)
