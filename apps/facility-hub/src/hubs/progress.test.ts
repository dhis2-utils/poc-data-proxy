/**
 * @jest-environment node
 */
/// <reference types="node" />
import { progress } from './progress'
import { HubRow } from './useHubs'

const hub = (id: string, extra: Partial<HubRow> = {}): HubRow => ({
    id,
    registry: { hubId: id },
    flags: [],
    retired: false,
    waiting: false,
    ...extra,
})

describe('progress', () => {
    it('counts the hubs in the stage, on the version, failed and waiting', () => {
        const choice = {
            version: '0.3.0',
            pilotHubs: ['hub-on', 'hub-wait', 'hub-fail', 'hub-old'],
            percent: 0,
        }
        const hubs = [
            hub('hub-on', { heartbeat: { version: '0.3.0' } }),
            hub('hub-wait', { heartbeat: { version: '0.2.0' } }),
            hub('hub-fail', {
                heartbeat: {
                    version: '0.2.0',
                    lastUpgrade: {
                        result: 'failed',
                        to: '0.3.0',
                        reason: 'health check failed',
                    },
                },
            }),
            hub('hub-outside', { heartbeat: { version: '0.2.0' } }),
            hub('hub-old', { retired: true, heartbeat: { version: '0.1.0' } }),
        ]
        expect(progress(hubs, choice)).toEqual({
            inStage: 3,
            onVersion: 1,
            failed: [{ id: 'hub-fail', reason: 'health check failed' }],
            waiting: 1,
        })
    })
    it('does not count a failure of another version', () => {
        const choice = { version: '0.4.0', pilotHubs: ['hub-a'], percent: 0 }
        const hubs = [
            hub('hub-a', {
                heartbeat: {
                    version: '0.2.0',
                    lastUpgrade: { result: 'failed', to: '0.3.0' },
                },
            }),
        ]
        expect(progress(hubs, choice).failed).toEqual([])
        expect(progress(hubs, choice).waiting).toBe(1)
    })
})
