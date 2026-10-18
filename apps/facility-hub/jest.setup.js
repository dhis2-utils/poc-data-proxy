// Jest 27's environments do not expose the Web Crypto API that browsers and Node have:
// the app draws its random values from crypto.getRandomValues.
if (!globalThis.crypto?.getRandomValues) {
    globalThis.crypto = require('crypto').webcrypto
}
