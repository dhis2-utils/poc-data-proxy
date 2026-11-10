import { apiFor } from './useApi'
import { ApiError } from '@/hubaccount/api'

// A fetch answer with just what the client reads.
const answer = (
    status: number,
    body: string,
    { type = 'application/json', redirected = false, url = '' } = {}
) =>
    ({
        status,
        ok: status >= 200 && status < 300,
        statusText: '',
        redirected,
        url,
        headers: new Map([['Content-Type', type]]),
        text: async () => body,
    }) as unknown as Response

const mockFetch = (res: Response) => {
    const f = jest.fn(async () => res)
    globalThis.fetch = f as unknown as typeof fetch
    return f
}

const expired = async (res: Response) => {
    mockFetch(res)
    const err = await apiFor('https://central')
        .request('GET', '/dataStore/facility-hub-registry')
        .catch((e) => e)
    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).status).toBe(401)
    expect((err as ApiError).message).toMatch(/session has expired/)
}

// After the session expires, central's login page must not read as "no hubs".
describe('apiFor', () => {
    it('asks for a 401 rather than the login page', async () => {
        const f = mockFetch(answer(200, '{"a":1}'))
        expect(await apiFor('https://central').request('GET', '/me')).toEqual({
            a: 1,
        })
        const init = (f.mock.calls[0] as unknown[])[1] as RequestInit
        expect(
            (init.headers as Record<string, string>)['X-Requested-With']
        ).toBe('XMLHttpRequest')
    })

    it('reads a 401 as an expired session', () =>
        expired(answer(401, '{"message":"Unauthorized"}')))

    it('reads the login page after a redirect as an expired session', () =>
        expired(
            answer(200, '<html>login</html>', {
                type: 'text/html;charset=UTF-8',
                redirected: true,
                url: 'https://central/dhis-web-login/',
            })
        ))

    it('reads any HTML answer as an expired session', () =>
        expired(answer(200, '<!doctype html>', { type: 'text/html' })))

    it('keeps other errors as they are', async () => {
        mockFetch(answer(409, '{"message":"exists"}'))
        const err = await apiFor('https://central')
            .request('POST', '/dataStore/x/y', {})
            .catch((e) => e)
        expect((err as ApiError).status).toBe(409)
        expect((err as ApiError).message).toBe('409 exists')
    })
})
