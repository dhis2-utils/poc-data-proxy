// Hub versions, compared as the hubs compare them (internal/upgrade CompareVersions):
// semantic-versioning precedence, so 0.3.0-rc1 < 0.3.0.

const split = (v: string) => {
    const [core, ...pre] = v.trim().replace(/^v/, '').split('+')[0].split('-')
    return {
        core: core.split('.').map((x) => parseInt(x, 10) || 0),
        pre: pre.length ? pre.join('-').split('.') : [],
    }
}

const numeric = (s: string) => /^\d+$/.test(s)

// A pre-release field: numeric ones numerically and below text ones, text in ASCII order.
const compareField = (a: string, b: string): number => {
    if (numeric(a) && numeric(b)) {
        return Math.sign(Number(a) - Number(b))
    }
    if (numeric(a) !== numeric(b)) {
        return numeric(a) ? -1 : 1
    }
    return a < b ? -1 : a > b ? 1 : 0
}

/** Negative when a comes before b, 0 when they are the same version, positive after. */
export const compareVersions = (a: string, b: string): number => {
    const pa = split(a)
    const pb = split(b)
    for (let i = 0; i < Math.max(pa.core.length, pb.core.length, 3); i++) {
        const d = (pa.core[i] ?? 0) - (pb.core[i] ?? 0)
        if (d !== 0) {
            return Math.sign(d)
        }
    }
    // A version with a pre-release comes before the release itself.
    if (!pa.pre.length || !pb.pre.length) {
        return Math.sign(pb.pre.length - pa.pre.length)
    }
    for (let i = 0; i < Math.min(pa.pre.length, pb.pre.length); i++) {
        const d = compareField(pa.pre[i], pb.pre[i])
        if (d !== 0) {
            return d
        }
    }
    return Math.sign(pa.pre.length - pb.pre.length)
}

/** A release in GitHub's list of the supplier's releases. */
export type GitHubRelease = {
    tag_name: string
    draft?: boolean
    prerelease?: boolean
}

/**
 * The versions a hub on a release would take: published, not marked as pre-releases,
 * and tagged vX.Y.Z without a pre-release suffix.
 */
export const releasedVersions = (list: GitHubRelease[]): string[] =>
    list
        .filter(
            (r) =>
                !r.draft && !r.prerelease && /^v\d+\.\d+\.\d+$/.test(r.tag_name)
        )
        .map((r) => r.tag_name.slice(1))
