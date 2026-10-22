/** A read the hub answered "needs connection" offline, as the heartbeat reports it. */
export type Miss = {
    method: string
    path: string
    params: string[]
    client: string
}

const line = /^(\S+) (\S+) \?(\S*) \[([^\]]*)\]$/

/**
 * Splits the hub's request line ("GET /api/programs ?fields,paging [web]": method,
 * path with {uid} for identifiers, parameter names, client) into its parts. A line it
 * cannot read is shown whole as the path.
 */
export const parseMiss = (request: string): Miss => {
    const m = line.exec(request)
    if (!m) {
        return { method: '', path: request, params: [], client: '' }
    }
    return {
        method: m[1],
        path: m[2],
        params: m[3] ? m[3].split(',') : [],
        client: m[4],
    }
}
