export class ApiError extends Error {
    constructor(
        readonly status: number,
        message: string
    ) {
        super(message)
    }
}

/** Requests to central's API, by path below /api. */
export interface Api {
    request(
        method: string,
        path: string,
        body?: unknown,
        contentType?: string
    ): Promise<unknown>
}

/** GET path, or null if central answers 404 (a dataStore key or namespace not made yet). */
export const getOrNull = async <T>(
    api: Api,
    path: string
): Promise<T | null> => {
    try {
        return (await api.request('GET', path)) as T
    } catch (e) {
        if (e instanceof ApiError && e.status === 404) {
            return null
        }
        throw e
    }
}
