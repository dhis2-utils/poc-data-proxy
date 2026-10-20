/**
 * @jest-environment node
 */
import { compareVersions, releasedVersions } from './versions'

// The hubs compare versions by semantic-versioning precedence (spec 2026-10-20, section
// 6, item 2; internal/upgrade CompareVersions): the app must agree with them.
describe('compareVersions', () => {
    it.each([
        ['0.3.0', '0.3.0', 0],
        ['v0.3.0', '0.3.0', 0],
        ['0.2.0', '0.3.0', -1],
        ['0.10.0', '0.9.0', 1],
        ['1.0.0', '0.99.99', 1],
        ['0.3.0-rc1', '0.3.0', -1],
        ['0.3.0', '0.3.0-rc1', 1],
        ['0.3.0-rc1', '0.3.0-rc2', -1],
        ['0.3.0-alpha', '0.3.0-alpha.1', -1],
        ['0.3.0-alpha.1', '0.3.0-alpha.beta', -1],
        ['0.3.0-alpha.2', '0.3.0-alpha.10', -1],
        ['0.3.0-1', '0.3.0-alpha', -1],
        ['0.3.0-beta.11', '0.3.0-rc.1', -1],
        ['0.3.0+build5', '0.3.0', 0],
        ['0.3.0-rc1', '0.2.9', 1],
    ])('%s against %s is %d', (a, b, want) => {
        expect(Math.sign(compareVersions(a, b))).toBe(want)
        expect(Math.sign(compareVersions(b, a))).toBe(-want || 0)
    })
})

describe('releasedVersions', () => {
    it('leaves out drafts, pre-releases and tags with a pre-release suffix', () => {
        expect(
            releasedVersions([
                { tag_name: 'v0.3.0' },
                { tag_name: 'v0.4.0', draft: true },
                { tag_name: 'v0.4.0-rc1' },
                { tag_name: 'v0.5.0', prerelease: true },
                { tag_name: 'hub-0.2.0' },
                { tag_name: 'v0.2.1' },
            ])
        ).toEqual(['0.3.0', '0.2.1'])
    })
})
