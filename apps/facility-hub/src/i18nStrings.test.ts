import fs from 'fs'
import path from 'path'

// The platform's extractor (i18next-scanner, with ":" as its namespace separator) puts
// a string containing ":" in another namespace and leaves it out of en.pot, so it can
// never be translated. The app's own i18n has no namespaces; the strings must avoid ":".
const sources = (dir: string): string[] =>
    fs.readdirSync(dir, { withFileTypes: true }).flatMap((e) => {
        const p = path.join(dir, e.name)
        if (e.isDirectory()) {
            return e.name === 'locales' ? [] : sources(p)
        }
        return /\.tsx?$/.test(e.name) && !/\.test\.tsx?$/.test(e.name)
            ? [p]
            : []
    })

describe('translated strings', () => {
    it('contain no ":", which the extractor drops', () => {
        const call = /i18n\.t\(\s*(['"])((?:\\.|(?!\1)[^\\])*)\1/g
        const found: string[] = []
        let n = 0
        for (const file of sources(__dirname)) {
            for (const m of fs.readFileSync(file, 'utf8').matchAll(call)) {
                n++
                if (m[2].includes(':')) {
                    found.push(`${path.basename(file)}: ${m[2]}`)
                }
            }
        }
        expect(n).toBeGreaterThan(200)
        expect(found).toEqual([])
    })
})
