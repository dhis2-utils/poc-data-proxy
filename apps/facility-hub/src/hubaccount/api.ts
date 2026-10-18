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
