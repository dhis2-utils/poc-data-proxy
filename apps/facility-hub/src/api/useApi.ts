import { useConfig } from '@dhis2/app-runtime'
import i18n from '@dhis2/d2-i18n'
import { useMemo } from 'react'
import { Api, ApiError, getOrNull } from '@/hubaccount/api'

// Once the session has expired, central answers with its login page: a redirect to it,
// or with X-Requested-With (which 2.42 honours) a 401.
const sessionExpired = () =>
    new ApiError(
        401,
        i18n.t('Your session has expired. Reload the page and log in again.')
    )

/** Central's API at baseUrl, with the browser's session. */
export const apiFor = (baseUrl: string): Api => ({
    // The Api interface's shape, shared with the approval code and its tests.
    // eslint-disable-next-line max-params
    async request(method, path, body, contentType = 'application/json') {
        const res = await fetch(`${baseUrl}/api${path}`, {
            method,
            credentials: 'include',
            headers: {
                Accept: 'application/json',
                'X-Requested-With': 'XMLHttpRequest',
                ...(body !== undefined && {
                    'Content-Type': contentType,
                }),
            },
            body: body !== undefined ? JSON.stringify(body) : undefined,
        })
        const html = (res.headers.get('Content-Type') ?? '').includes(
            'text/html'
        )
        if (
            res.status === 401 ||
            (res.ok && (html || (res.redirected && /login/i.test(res.url))))
        ) {
            throw sessionExpired()
        }
        const text = await res.text()
        let json: unknown = undefined
        try {
            json = text ? JSON.parse(text) : undefined
        } catch {
            json = undefined
        }
        if (!res.ok) {
            const message =
                (json as { message?: string } | undefined)?.message ??
                res.statusText
            throw new ApiError(res.status, `${res.status} ${message}`)
        }
        return json
    },
})

/**
 * Central's API for the user logged in to the app: fetch with their session. Used for
 * the facility-hub namespaces (a namespace that does not exist yet answers 404) and the
 * approval steps (which include a JSON patch).
 */
export const useApi = (): Api => {
    const { baseUrl } = useConfig()
    return useMemo(() => apiFor(baseUrl), [baseUrl])
}

/** A namespace listing, or nothing if the namespace does not exist yet. */
export const listNamespace = async <T>(
    api: Api,
    ns: string,
    fields: string
): Promise<{ key: string; value: T }[]> => {
    const res = await getOrNull<{ entries?: Record<string, unknown>[] }>(
        api,
        `/dataStore/${ns}?fields=${fields}&paging=false`
    )
    // With fields=. each entry is {key, value}; with named fields, {key, <field>...}.
    return (res?.entries ?? []).map((e) => {
        const { key, value, ...rest } = e as { key: string; value?: T }
        return { key, value: (value ?? rest) as T }
    })
}
