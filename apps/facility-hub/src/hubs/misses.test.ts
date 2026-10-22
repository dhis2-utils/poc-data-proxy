/**
 * @jest-environment node
 */
import { parseMiss } from './misses'

describe('parseMiss', () => {
    it("splits the hub's request line into method, path, parameters and client", () => {
        expect(parseMiss('GET /api/programs ?fields,paging [web]')).toEqual({
            method: 'GET',
            path: '/api/programs',
            params: ['fields', 'paging'],
            client: 'web',
        })
    })
    it('keeps {uid} and a request without parameters', () => {
        expect(parseMiss('GET /api/optionSets/{uid} ? [android]')).toEqual({
            method: 'GET',
            path: '/api/optionSets/{uid}',
            params: [],
            client: 'android',
        })
    })
    it('shows a line it cannot read as it is', () => {
        expect(parseMiss('something else')).toEqual({
            method: '',
            path: 'something else',
            params: [],
            client: '',
        })
    })
})
