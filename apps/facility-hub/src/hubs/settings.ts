// The country's settings for every hub (facility-hub-config, spec 2026-10-20, section
// 4), as the Settings page edits them. A missing key means the hubs' defaults.

/** facility-hub-config/retention: months kept; 0 keeps everything (access.Retention). */
export type Retention = {
    defaultMonths?: number
    programs?: Record<string, number>
    trackedEntityTypes?: Record<string, number>
    [field: string]: unknown
}

/**
 * facility-hub-config/localReads: programmes whose data each hub answers from its own
 * copy while central is reachable (access.LocalReads). Saves still go to central.
 */
export type LocalReads = {
    programs?: string[]
    [field: string]: unknown
}

/** The value to write: the chosen programmes, other fields kept. */
export const localReadsValue = (
    programs: string[],
    stored?: LocalReads | null
): LocalReads => ({ ...stored, programs: [...programs].sort() })

/**
 * facility-hub-config/metadataReload: a time the hubs have not seen makes each of
 * them fetch metadata and settings from central again (core/metadata.go).
 */
export type MetadataReload = { at?: string; [field: string]: unknown }

export const metadataReloadValue = (
    now: Date,
    stored?: MetadataReload | null
): MetadataReload => ({ ...stored, at: now.toISOString() })

/** facility-hub-config/aggregate: periods kept, the current one included (0: the default). */
export type AggregateRetention = {
    retentionPeriods?: number
    dataSets?: Record<string, number>
    [field: string]: unknown
}

/** facility-hub-config/tokens: the lifetime of new and renewed tokens. */
export type TokenSettings = { expiryDays?: number; [field: string]: unknown }

/** facility-hub-config/reserved: reserved-value pool sizes (core.ReservedPool). */
export type ReservedPool = {
    default?: number
    attributes?: Record<string, number>
    [field: string]: unknown
}

export const defaults = { months: 0, periods: 12, expiryDays: 90, pool: 500 }

export type Limits = { min: number; max: number }

export const limits = {
    months: { min: 0, max: 1200 },
    periods: { min: 1, max: 1000 },
    expiryDays: { min: 7, max: 365 },
    pool: { min: 1, max: 100000 },
    poolRow: { min: 0, max: 100000 }, // 0 turns an attribute's offline pool off
}

/** The whole number typed, or null when it is not one within the limits. */
export const wholeNumber = (s: string, { min, max }: Limits): number | null => {
    const t = s.trim()
    if (!/^\d+$/.test(t)) {
        return null
    }
    const n = Number(t)
    return n >= min && n <= max ? n : null
}

/** A number by object (programme, person type or data set), as typed. */
export type Row = { id: string; value: string }

const rowsOf = (
    m: Record<string, number> | undefined,
    keep = (n: number) => n >= 0
) =>
    Object.entries(m ?? {})
        .filter(([, n]) => typeof n === 'number' && keep(n))
        .map(([id, n]) => ({ id, value: String(n) }))

const mapOf = (rows: Row[], l: Limits): Record<string, number> | null => {
    const out: Record<string, number> = {}
    for (const r of rows) {
        const n = wholeNumber(r.value, l)
        if (n === null) {
            return null
        }
        out[r.id] = n
    }
    return out
}

export type RetentionForm = {
    defaultMonths: string
    programs: Row[]
    trackedEntityTypes: Row[]
}

export const retentionForm = (r?: Retention | null): RetentionForm => ({
    defaultMonths: String(r?.defaultMonths ?? defaults.months),
    programs: rowsOf(r?.programs),
    trackedEntityTypes: rowsOf(r?.trackedEntityTypes),
})

/** The value to write, or null while something typed is not valid. */
export const retentionValue = (
    f: RetentionForm,
    stored?: Retention | null
): Retention | null => {
    const defaultMonths = wholeNumber(f.defaultMonths, limits.months)
    const programs = mapOf(f.programs, limits.months)
    const trackedEntityTypes = mapOf(f.trackedEntityTypes, limits.months)
    if (defaultMonths === null || !programs || !trackedEntityTypes) {
        return null
    }
    return { ...stored, defaultMonths, programs, trackedEntityTypes }
}

export type AggregateForm = { retentionPeriods: string; dataSets: Row[] }

export const aggregateForm = (
    a?: AggregateRetention | null
): AggregateForm => ({
    retentionPeriods: String(a?.retentionPeriods || defaults.periods),
    // The hubs read 0 as "the default" (aggregate.Retention.For).
    dataSets: rowsOf(a?.dataSets, (n) => n > 0),
})

export const aggregateValue = (
    f: AggregateForm,
    stored?: AggregateRetention | null
): AggregateRetention | null => {
    const retentionPeriods = wholeNumber(f.retentionPeriods, limits.periods)
    const dataSets = mapOf(f.dataSets, limits.periods)
    if (retentionPeriods === null || !dataSets) {
        return null
    }
    return { ...stored, retentionPeriods, dataSets }
}

export const tokensForm = (t?: TokenSettings | null) =>
    String(t?.expiryDays ?? defaults.expiryDays)

export const tokensValue = (
    s: string,
    stored?: TokenSettings | null
): TokenSettings | null => {
    const expiryDays = wholeNumber(s, limits.expiryDays)
    return expiryDays === null ? null : { ...stored, expiryDays }
}

export type PoolForm = { defaultPool: string; attributes: Row[] }

export const poolForm = (r?: ReservedPool | null): PoolForm => ({
    defaultPool: String(r?.default ?? defaults.pool),
    // n >= 0, so a stored 0 round-trips as '0' (the attribute's pool is off).
    attributes: rowsOf(r?.attributes),
})

export const poolValue = (
    f: PoolForm,
    stored?: ReservedPool | null
): ReservedPool | null => {
    const def = wholeNumber(f.defaultPool, limits.pool)
    const attributes = mapOf(
        f.attributes.filter((r) => r.value.trim() !== ''),
        limits.poolRow
    )
    if (def === null || !attributes) {
        return null
    }
    return { ...stored, default: def, attributes }
}
