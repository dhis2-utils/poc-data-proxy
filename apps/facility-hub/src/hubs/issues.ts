import i18n from '@dhis2/d2-i18n'

/**
 * Who closes a refused change, by error code (docs/design/
 * 2026-11-04-refused-changes.md, "Refusals by who can fix them"): the author by editing
 * again; a supervisor or admin on central; or nobody, because central has deleted the
 * target. A change blocked by a refused parent (E5000, "blocked by") closes with it.
 */
export type Closer = 'author' | 'central' | 'nobody' | 'parent'

const codes: Record<Closer, string[]> = {
    author: [
        'E1302',
        'E1007',
        'E1125',
        'E1303',
        'E1064',
        'E1020',
        'E1021',
        'E1015',
        'E1016',
        'E1039',
    ],
    central: [
        'E1000',
        'E1102',
        'E1091',
        'E1095',
        'E1099',
        'E1104',
        'E1010',
        'E1013',
        'E1069',
        'E1029',
        'E1041',
        'E1057',
        'E1047',
        'E1083',
        'E1300',
        'E1301',
        'E1305',
        'E1306',
        'NO_LOGIN',
        'E2017',
        'E2022',
        'E2025',
        'E2031',
        'E1100',
    ],
    nobody: ['E1082', 'E1113', 'E1114', 'E4017'],
    parent: ['E5000'],
}

const byCode = new Map(
    (Object.keys(codes) as Closer[]).flatMap((c) =>
        codes[c].map((code) => [code, c] as const)
    )
)

/** Who closes a refusal with this code; undefined for a code not in the table. */
export const closerOf = (code: string): Closer | undefined => byCode.get(code)

/**
 * Who closes a refused change, from its error codes and message: each closer once, in
 * the order of the codes. A change blocked by its parent has no code, only the message.
 */
export const closersOf = (errorCodes: string[], message?: string): Closer[] => {
    const out = errorCodes
        .map(closerOf)
        .filter((c): c is Closer => c !== undefined)
    if (message?.startsWith('blocked by ')) {
        out.push('parent')
    }
    return [...new Set(out)]
}

/** The short text the hub's page shows for each closer. */
export const closerText = (c: Closer): string =>
    ({
        author: i18n.t('Fixed by the author'),
        central: i18n.t('Fixed on central'),
        nobody: i18n.t('Fixed by nobody'),
        parent: i18n.t('Closes with the record it belongs to'),
    })[c]

/**
 * A link that opens a refused record in Capture on central, or undefined for kinds
 * Capture does not open (aggregate values, relationships). Capture's routes
 * (capture-app, src/components/App/AppPages.component.tsx) fill in the programme and
 * person from the enrollment or event. An event whose trackedEntity is "" is an event
 * programme's, which opens on another page; an event reported without the field (by a
 * hub older than this field) gets no link, because the wrong page does not open it.
 * /dhis-web-capture/index.html redirects to /apps/capture on 2.42 and keeps the hash.
 */
export const captureLink = (
    baseUrl: string,
    issue: { kind?: string; objectUid?: string; trackedEntity?: string }
): string | undefined => {
    const uid = issue.objectUid
    if (!uid) {
        return undefined
    }
    const app = `${baseUrl.replace(/\/+$/, '')}/dhis-web-capture/index.html#/`
    const q = (k: string, v: string) => `${k}=${encodeURIComponent(v)}`
    switch (issue.kind) {
        case 'trackedEntity':
            return `${app}enrollment?${q('teiId', uid)}`
        case 'enrollment':
            return `${app}enrollment?${q('enrollmentId', uid)}`
        case 'event':
            if (issue.trackedEntity === undefined) {
                return undefined
            }
            return issue.trackedEntity
                ? `${app}enrollmentEventEdit?${q('eventId', uid)}`
                : `${app}viewEvent?${q('viewEventId', uid)}`
        default:
            return undefined
    }
}
