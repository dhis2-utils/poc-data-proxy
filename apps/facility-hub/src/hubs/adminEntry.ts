import { Api, ApiError } from '@/hubaccount/api'

type Meta = {
    id: string
    createdBy?: { id?: string }
    sharing?: { owner?: string }
}

/**
 * Writes facility-hub-admin/<key>, which must be writable by administrators only
 * (docs/DEPLOYMENT.md, section 6: a planned move there moves every hub). A new
 * dataStore entry is public (read and write) until its sharing is set, and its owner
 * (who created it) keeps write access whatever the sharing says. So an entry created by
 * someone else is deleted and created again; it is created empty, shared as public read
 * only, and only then written.
 */
export const writeAdminEntry = async (
    api: Api,
    key: string,
    value: unknown
) => {
    const path = `/dataStore/facility-hub-admin/${encodeURIComponent(key)}`
    const metaOf = async () =>
        (await api.request('GET', `${path}/metaData`)) as Meta
    try {
        await api.request('POST', path, {})
    } catch (e) {
        if (!(e instanceof ApiError && e.status === 409)) {
            throw e
        }
        const [meta, me] = await Promise.all([
            metaOf(),
            api.request('GET', '/me?fields=id') as Promise<{ id: string }>,
        ])
        if ((meta.sharing?.owner ?? meta.createdBy?.id) !== me.id) {
            await api.request('DELETE', path)
            await api.request('POST', path, {})
        }
    }
    const meta = await metaOf()
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
