/**
 * @jest-environment node
 */
/// <reference types="node" />
import fs from 'fs'
import path from 'path'
import {
    accountCode,
    canonical,
    checkAddress,
    instructionCode,
    moveCode,
    normalizeAddress,
    normalizeCode,
} from './codes'

// The table the hub checks too (internal/hubaccount/codes_test.go, TestAddressTable).
const table = JSON.parse(
    fs.readFileSync(
        path.join(
            __dirname,
            '../../../../internal/hubaccount/testdata/addresses.json'
        ),
        'utf8'
    )
) as { accepted: { address: string; normalized: string }[]; refused: string[] }

// The values of docs/FACILITY-HUB-APP.md and internal/hubaccount/codes_test.go: the
// hub computes the same codes.
const fill = (n: number, b: number) => new Uint8Array(n).fill(b)

describe('codes', () => {
    it('gives the hub its account code', () => {
        expect(accountCode(fill(32, 1), 'hub-Abc123XyZ90', 'DiszpKrYNg8')).toBe(
            'VMC4-PMVY-I5K4-UMUO'
        )
    })

    it('depends on the facility', () => {
        expect(
            accountCode(fill(32, 1), 'hub-Abc123XyZ90', 'g8upMTyEZGZ')
        ).not.toBe('VMC4-PMVY-I5K4-UMUO')
    })

    it.each([
        'https://new.example.org/dhis',
        ' HTTPS://New.Example.org/dhis/ ',
        'https://new.example.org:443/dhis',
    ])('gives the move code for %j', (address) => {
        expect(moveCode(fill(32, 2), address)).toBe('IJ3O2-BXZPR')
    })

    it('depends on the port and the path', () => {
        const base = moveCode(fill(32, 2), 'https://new.example.org/dhis')
        expect(
            moveCode(fill(32, 2), 'https://new.example.org:8443/dhis')
        ).not.toBe(base)
        expect(moveCode(fill(32, 2), 'https://new.example.org')).not.toBe(base)
    })

    it.each(table.accepted)(
        'accepts $address as the hub does',
        ({ address, normalized }) => {
            expect(checkAddress(address)).toBeNull()
            expect(normalizeAddress(address)).toBe(normalized)
        }
    )

    it.each(table.refused)('refuses %j as the hub does', (address) => {
        expect(checkAddress(address)).not.toBeNull()
    })

    it.each([
        'https://bücher.example/dhis',
        'https://h.example/a b',
        'https://h.example/x?y=1',
        'https://h.example/x#f',
        'ftp://h.example',
        'https://h.example/a{b}',
        'https://h.example:/dhis',
        'https://h.example/a%20b',
    ])('refuses the address %j', (address) => {
        expect(checkAddress(address)).not.toBeNull()
    })

    it('accepts an address with a port and a path', () => {
        expect(checkAddress('https://new.example.org:8443/dhis')).toBeNull()
    })

    it('reads a code however it was typed', () => {
        for (const typed of ['abcde-fghij', 'ABCDE FGHIJ', 'abcdefghij']) {
            expect(normalizeCode(typed)).toBe('ABCDEFGHIJ')
        }
    })
})

// The hub gives the same code for the same input (internal/hubaccount/codes_test.go,
// TestInstructionCodeVector).
describe('instructionCode', () => {
    const key = new Uint8Array(32).fill(2)
    const obj = JSON.parse(
        '{"url":"https://new.example.org/dhis","from":"2026-11-12T08:00:00.000Z","issued":"2026-11-11T10:00:00.000Z",' +
            '"note":"Bø <&> \\u2028 \\"q\\" \\n","n":[10,1.5,true,null,{"b":1,"a":"x"}],"codes":{"hub-x":"zzz"}}'
    )
    it('matches the hub', () => {
        expect(
            instructionCode(key, 'hub-Abc123XyZ90', {
                kind: 'facility-hub-admin/central-move',
                obj,
            })
        ).toBe('EiA0hT5/YySwjrCFD87XUg==')
    })
    it('depends on the hub, the kind and every value', () => {
        const base = instructionCode(key, 'hub-A', { kind: 'k', obj })
        expect(instructionCode(key, 'hub-B', { kind: 'k', obj })).not.toBe(base)
        expect(instructionCode(key, 'hub-A', { kind: 'k2', obj })).not.toBe(
            base
        )
        expect(
            instructionCode(key, 'hub-A', {
                kind: 'k',
                obj: { ...obj, url: 'https://x' },
            })
        ).not.toBe(base)
        expect(
            instructionCode(key, 'hub-A', {
                kind: 'k',
                obj: { ...obj, codes: {} },
            })
        ).toBe(base)
    })
    it('writes JSON as the hub does', () => {
        expect(
            canonical({
                b: [1, 'é\u2028'],
                a: { d: null, c: true, e: undefined },
            })
        ).toBe('{"a":{"c":true,"d":null},"b":[1,"é\\u2028"]}')
    })
})
