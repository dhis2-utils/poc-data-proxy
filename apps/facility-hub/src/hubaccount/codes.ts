import { hmac } from '@noble/hashes/hmac.js'
import { sha256 } from '@noble/hashes/sha2.js'
import { utf8ToBytes } from '@noble/hashes/utils.js'

// The two codes people read out to each other, as the hub computes them
// (internal/hubaccount/codes.go, docs/FACILITY-HUB-APP.md).

const alphabet = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567'

// base32 is RFC 4648 base32 without padding.
const base32 = (bytes: Uint8Array): string => {
    let out = ''
    let bits = 0
    let value = 0
    for (const b of bytes) {
        value = (value << 8) | b
        bits += 8
        while (bits >= 5) {
            out += alphabet[(value >>> (bits - 5)) & 31]
            bits -= 5
        }
    }
    if (bits > 0) {
        out += alphabet[(value << (5 - bits)) & 31]
    }
    return out
}

// group writes a code in groups of five (move code) or four (account code).
const group = (s: string): string => {
    const n = s.length === 10 ? 5 : 4
    const parts: string[] = []
    for (let i = 0; i < s.length; i += n) {
        parts.push(s.slice(i, i + n))
    }
    return parts.join('-')
}

const concat = (...parts: Uint8Array[]): Uint8Array => {
    const out = new Uint8Array(parts.reduce((n, p) => n + p.length, 0))
    let at = 0
    for (const p of parts) {
        out.set(p, at)
        at += p.length
    }
    return out
}

/** The code a hub waiting for an account shows on its page: 16 characters (80 bits). */
export const accountCode = (
    pub: Uint8Array,
    hubID: string,
    facility: string
): string =>
    group(
        base32(
            sha256(
                concat(
                    utf8ToBytes(
                        `facility-hub account code\x00${hubID}\x00${facility}\x00`
                    ),
                    pub
                )
            )
        ).slice(0, 16)
    )

/** The code for moving a hub to an address: 10 characters (50 bits). */
export const moveCode = (moveKey: Uint8Array, address: string): string =>
    group(
        base32(
            hmac(sha256, moveKey, utf8ToBytes(normalizeAddress(address)))
        ).slice(0, 10)
    )

const allowed = /^[A-Za-z0-9\-._~!$&'()*+,;=/:@[\]]*$/

/**
 * Refuses an address a move code is never made for: anything but
 * http(s)://host[:port][/path] in the allowed characters, without user, query or
 * fragment, and with a port given if there is a colon after the host. Returns the
 * reason, or null.
 */
export const checkAddress = (raw: string): string | null => {
    const s = raw.trim()
    if (!allowed.test(s)) {
        return "an address of letters, digits and - . _ ~ ! $ & ' ( ) * + , ; = / : @ only"
    }
    const m = /^([A-Za-z]+):\/\/([^/]*)(\/.*)?$/.exec(s)
    if (
        !m ||
        !['http', 'https'].includes(m[1].toLowerCase()) ||
        m[2] === '' ||
        m[2].includes('@') ||
        m[2].endsWith(':')
    ) {
        return 'an address like https://central.example.org/dhis'
    }
    return null
}

/**
 * The form of an address a move code is made for (it must pass checkAddress): no
 * surrounding spaces or trailing slash, scheme and host in lower case, no default port,
 * the path as typed.
 */
export const normalizeAddress = (raw: string): string => {
    const s = raw.trim().replace(/\/+$/, '')
    const m = /^([A-Za-z]+):\/\/([^/]*)(\/.*)?$/.exec(s)
    if (!m) {
        return s
    }
    const scheme = m[1].toLowerCase()
    let host = m[2].toLowerCase()
    const defaultPort =
        scheme === 'https' ? ':443' : scheme === 'http' ? ':80' : ''
    if (defaultPort && host.endsWith(defaultPort)) {
        host = host.slice(0, -defaultPort.length)
    }
    return `${scheme}://${host}${m[3] ?? ''}`
}

/** A code as typed, without spaces or dashes, in upper case. */
export const normalizeCode = (typed: string): string =>
    typed.replace(/[-\s]/g, '').toUpperCase()
