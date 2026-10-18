/**
 * @jest-environment node
 */
import { x25519 } from '@noble/curves/ed25519.js'
import { utf8ToBytes } from '@noble/hashes/utils.js'
import { seal } from './box'

const fill = (n: number, b: number) => new Uint8Array(n).fill(b)
const hex = (b: Uint8Array) =>
    Array.from(b, (x) => x.toString(16).padStart(2, '0')).join('')

// The same fixed inputs as TestBoxVector in internal/hubaccount/box_test.go, which
// pins these bytes and opens them with the hub's own code.
describe('seal', () => {
    it('seals as the hub opens', () => {
        const hubPub = x25519.getPublicKey(fill(32, 5))
        const box = seal(
            {
                pub: hubPub,
                plaintext: utf8ToBytes('{"username":"hub-x"}'),
                hubID: 'hub-x',
            },
            {
                eph: fill(32, 3),
                nonce: fill(12, 4),
            }
        )
        expect(hex(box)).toBe(
            '5dfedd3b6bd47f6fa28ee15d969d5bb0ea53774d488bdaf9df1c6e0124b3ef2204040404040404040404040479e45e7ffcb136196cd285a04af837229429c31346c1b39e97d56e3b482473413f97313b'
        )
    })

    it('uses fresh randomness otherwise', () => {
        const hubPub = x25519.getPublicKey(fill(32, 5))
        const a = seal({
            pub: hubPub,
            plaintext: utf8ToBytes('x'),
            hubID: 'hub-x',
        })
        const b = seal({
            pub: hubPub,
            plaintext: utf8ToBytes('x'),
            hubID: 'hub-x',
        })
        expect(hex(a)).not.toBe(hex(b))
        expect(a.length).toBe(32 + 12 + 1 + 16)
    })
})
