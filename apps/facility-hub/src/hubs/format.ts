import i18n from '@dhis2/d2-i18n'

/** "3 hours ago", "2 days ago", or "never". */
export const ago = (
    iso: string | undefined | null,
    now = new Date()
): string => {
    const t = iso ? Date.parse(iso) : NaN
    if (Number.isNaN(t)) {
        return i18n.t('never')
    }
    const minutes = Math.max(0, Math.round((now.getTime() - t) / 60e3))
    if (minutes < 60) {
        return minutes === 1
            ? i18n.t('1 minute ago')
            : i18n.t('{{- n}} minutes ago', { n: minutes })
    }
    const hours = Math.round(minutes / 60)
    if (hours < 48) {
        return hours === 1
            ? i18n.t('1 hour ago')
            : i18n.t('{{- n}} hours ago', { n: hours })
    }
    return i18n.t('{{- n}} days ago', { n: Math.round(hours / 24) })
}

/** "12.3 GB". */
export const gb = (bytes: number | undefined | null): string =>
    bytes == null ? '' : `${(bytes / 1e9).toFixed(1)} GB`

/** A date and time in the user's locale. */
export const when = (iso: string | undefined | null): string => {
    const t = iso ? Date.parse(iso) : NaN
    return Number.isNaN(t) ? '' : new Date(t).toLocaleString()
}
