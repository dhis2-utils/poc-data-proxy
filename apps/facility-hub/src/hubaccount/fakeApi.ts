import { Api, ApiError } from './api'

const clone = <T>(v: T): T =>
    v === undefined ? v : JSON.parse(JSON.stringify(v))

type User = {
    id: string
    username: string
    password: string
    disabled: boolean
    organisationUnits: string[]
    dataViewOrganisationUnits: string[]
    teiSearchOrganisationUnits: string[]
    roles: string[]
    groups: string[]
}

/**
 * An in-memory central for the approval tests: the endpoints an approval uses, behaving
 * as DHIS2 does (and as internal/fakecentral does for the hub's tests).
 */
export class FakeApi implements Api {
    store: Record<string, Record<string, unknown>> = {}
    sharing: Record<string, unknown> = {}
    users: Record<string, User> = {}
    roleAuthorities: Record<string, string[]> = {}
    log: string[] = []
    // The logged-in user, and who created each entry (its owner).
    me = 'MeUser00001'
    creators: Record<string, string> = {}
    // A path whose next request fails with this status (tests of failed steps).
    failNext: Record<string, number> = {}
    // Run before the second GET of the registry entry (a change meanwhile).
    beforeReread?: () => void
    private registryReads = 0

    entry(ns: string, key: string) {
        return this.store[ns]?.[key] as Record<string, unknown> | undefined
    }

    async request(method: string, path: string, body?: unknown) {
        const [p, query = ''] = path.split('?')
        this.log.push(`${method} ${p}`)
        const fail = this.failNext[`${method} ${p}`]
        if (fail) {
            delete this.failNext[`${method} ${p}`]
            throw new ApiError(fail, 'failing as asked')
        }
        let m = /^\/dataStore\/([^/]+)\/([^/]+?)(\/metaData)?$/.exec(p)
        if (m) {
            const [, ns, key, meta] = m
            const k = decodeURIComponent(key)
            if (meta) {
                const owner = this.creators[`${ns}/${k}`] ?? this.me
                return {
                    id: `meta-${ns}-${k}`,
                    createdBy: { id: owner },
                    sharing: { owner },
                }
            }
            const has = this.store[ns] && k in this.store[ns]
            switch (method) {
                case 'GET':
                    if (!has) {
                        throw new ApiError(404, 'not found')
                    }
                    if (
                        ns === 'facility-hub-registry' &&
                        ++this.registryReads === 2
                    ) {
                        this.beforeReread?.()
                    }
                    return clone(this.store[ns][k])
                case 'POST':
                    if (has) {
                        throw new ApiError(409, 'exists')
                    }
                    this.store[ns] = {
                        ...this.store[ns],
                        [k]: clone(body),
                    }
                    this.creators[`${ns}/${k}`] = this.me
                    return {}
                case 'PUT':
                    if (!has) {
                        throw new ApiError(404, 'not found')
                    }
                    this.store[ns][k] = clone(body)
                    return {}
                case 'DELETE':
                    if (!has) {
                        throw new ApiError(404, 'not found')
                    }
                    delete this.store[ns][k]
                    delete this.sharing[`meta-${ns}-${k}`]
                    delete this.creators[`${ns}/${k}`]
                    return {}
            }
        }
        if (p === '/me' && method === 'GET') {
            return { id: this.me }
        }
        if (p === '/sharing' && method === 'POST') {
            this.sharing[new URLSearchParams(query).get('id') ?? ''] = body
            return {}
        }
        m = /^\/userRoles\/(\w+)$/.exec(p)
        if (m && method === 'GET') {
            return { id: m[1], authorities: this.roleAuthorities[m[1]] ?? [] }
        }
        if (p === '/users' && method === 'GET') {
            const name = new URLSearchParams(query)
                .get('filter')
                ?.replace('username:eq:', '')
            const u = Object.values(this.users).find((x) => x.username === name)
            const refs = (ids: string[]) => ids.map((id) => ({ id }))
            return {
                users: u
                    ? [
                          {
                              id: u.id,
                              disabled: u.disabled,
                              organisationUnits: refs(u.organisationUnits),
                              dataViewOrganisationUnits: refs(
                                  u.dataViewOrganisationUnits
                              ),
                              teiSearchOrganisationUnits: refs(
                                  u.teiSearchOrganisationUnits
                              ),
                              userRoles: u.roles.map((id) => ({
                                  id,
                                  authorities: this.roleAuthorities[id] ?? [],
                              })),
                          },
                      ]
                    : [],
            }
        }
        if (p === '/users' && method === 'POST') {
            const b = body as Record<string, unknown>
            const ids = (k: string) =>
                ((b[k] as { id: string }[]) ?? []).map((x) => x.id)
            this.users[b.id as string] = {
                id: b.id as string,
                username: b.username as string,
                password: b.password as string,
                disabled: false,
                organisationUnits: ids('organisationUnits'),
                dataViewOrganisationUnits: ids('dataViewOrganisationUnits'),
                teiSearchOrganisationUnits: ids('teiSearchOrganisationUnits'),
                roles: ids('userRoles'),
                groups: [],
            }
            return {}
        }
        m = /^\/users\/(\w+)$/.exec(p)
        if (m && method === 'PATCH') {
            const u = this.users[m[1]]
            if (!u) {
                throw new ApiError(404, 'no such user')
            }
            for (const op of body as {
                op: string
                path: string
                value: string
            }[]) {
                if (op.op === 'replace' && op.path === '/password') {
                    u.password = op.value
                }
            }
            return {}
        }
        m = /^\/userGroups\/(\w+)\/users\/(\w+)$/.exec(p)
        if (m && method === 'POST') {
            this.users[m[2]]?.groups.push(m[1])
            return {}
        }
        throw new ApiError(404, `not in the fake: ${method} ${path}`)
    }
}
