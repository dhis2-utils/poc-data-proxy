import config from '@dhis2/config-eslint'
import { defineConfig } from 'eslint/config'
import { includeIgnoreFile } from '@eslint/compat'
import { fileURLToPath } from 'node:url'

const gitignorePath = fileURLToPath(new URL('.gitignore', import.meta.url))

export default defineConfig([
    includeIgnoreFile(gitignorePath, 'Imported .gitignore patterns'),
    {
        extends: [config],
        // Resolve imports the way TypeScript does (package exports, the "@" alias):
        // the default resolver reports named exports of ESM packages as missing.
        settings: {
            'import/resolver': { typescript: true, node: true },
        },
    },
])
