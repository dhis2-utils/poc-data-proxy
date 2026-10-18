import { gcm } from '@noble/ciphers/aes.js'
import { x25519 } from '@noble/curves/ed25519.js'
import { hkdf } from '@noble/hashes/hkdf.js'
import { sha256 } from '@noble/hashes/sha2.js'
import { randomBytes, utf8ToBytes } from '@noble/hashes/utils.js'

/**
 * Encrypts plaintext to a hub with its box key, bound to its ID, as the hub opens it
 * (internal/hubaccount/box.go): an ephemeral X25519 key; a key derived from the shared
 * secret with HKDF-SHA256 (no salt, info "facility-hub account box <hub ID>"); AES-256-GCM
 * with "hub-account:<hub ID>" as authenticated data. The box is the ephemeral public key
 * (32 bytes), the nonce (12 bytes), then the ciphertext and tag. rnd is for tests only.
 */
export const seal = (
    {
        pub,
        plaintext,
        hubID,
    }: { pub: Uint8Array; plaintext: Uint8Array; hubID: string },
    rnd?: { eph: Uint8Array; nonce: Uint8Array }
): Uint8Array => {
    const eph = rnd?.eph ?? randomBytes(32)
    const nonce = rnd?.nonce ?? randomBytes(12)
    const shared = x25519.getSharedSecret(eph, pub)
    const key = hkdf(
        sha256,
        shared,
        undefined,
        utf8ToBytes(`facility-hub account box ${hubID}`),
        32
    )
    const ct = gcm(key, nonce, utf8ToBytes(`hub-account:${hubID}`)).encrypt(
        plaintext
    )
    const box = new Uint8Array(32 + 12 + ct.length)
    box.set(x25519.getPublicKey(eph), 0)
    box.set(nonce, 32)
    box.set(ct, 44)
    return box
}
