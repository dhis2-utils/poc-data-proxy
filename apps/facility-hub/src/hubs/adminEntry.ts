import { Api, ApiError } from '@/hubaccount/api'

/**
 * Writes facility-hub-admin/<key>, which must be writable by administrators only
 * (docs/DEPLOYMENT.md, section 6: a planned move there moves every hub). A new
 * dataStore entry is public (read and write) until its sharing is set, so the entry is
 * created empty if missing, shared as public read only, and only then written.
 */
export const writeAdminEntry = async (
    api: Api,
    key: string,
    value: unknown
) => {
    const path = `/dataStore/facility-hub-admin/${encodeURIComponent(key)}`
    try {
        await api.request('POST', path, {})
    } catch (e) {
        if (!(e instanceof ApiError && e.status === 409)) {
            throw e
        }
    }
    const meta = (await api.request('GET', `${path}/metaData`)) as {
        id: string
    }
    await api.request(
        'POST',
        `/sharing?type=dataStore&id=${encodeURIComponent(meta.id)}`,
        {
            object: {
                publicAccess: 'r-------',
                userAccesses: [],
                userGroupAccesses: [],
            },
        }
    )
    await api.request('PUT', path, value)
}
