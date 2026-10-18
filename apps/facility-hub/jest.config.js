// The platform's Jest defaults, plus: the @noble cryptography packages ship as ES
// modules only, so they are transformed like the app's own code (in pnpm's layout,
// .../node_modules/.pnpm/@noble+x/node_modules/@noble/x); and the "@" import alias.
// (by file path: the package does not export its config)
const defaults = require(
    require('path').join(
        __dirname,
        'node_modules/@dhis2/cli-app-scripts/config/jest.config.js'
    )
)

module.exports = {
    setupFiles: ['<rootDir>/jest.setup.js'],
    transformIgnorePatterns: [
        '/node_modules/(?!(\\.pnpm|@noble|moment/dist/))',
    ],
    moduleNameMapper: {
        ...defaults.moduleNameMapper,
        '^@/(.*)$': '<rootDir>/src/$1',
    },
}
