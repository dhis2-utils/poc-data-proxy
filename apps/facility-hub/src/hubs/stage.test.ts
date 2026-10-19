/**
 * @jest-environment node
 */
/// <reference types="node" />
import fs from 'fs'
import path from 'path'
import { inStage, nextStage, stageLabel } from './stage'

// internal/upgrade/testdata/stages.json: the rule the hubs use (upgrade.InStage).
const rows = JSON.parse(
    fs.readFileSync(
        path.join(
            __dirname,
            '../../../../internal/upgrade/testdata/stages.json'
        ),
        'utf8'
    )
) as { hubId: string; pilotHubs: string[]; percent: number; in: boolean }[]

describe('inStage', () => {
    it('matches the hubs on every row of the shared table', () => {
        expect(rows.length).toBeGreaterThanOrEqual(40)
        for (const r of rows) {
            expect([
                r.hubId,
                inStage(
                    {
                        version: '1.0.0',
                        pilotHubs: r.pilotHubs,
                        percent: r.percent,
                    },
                    r.hubId
                ),
            ]).toEqual([r.hubId, r.in])
        }
    })
})

describe('nextStage', () => {
    const c = { version: '0.3.0', pilotHubs: ['hub-a'], percent: 0 }
    it('goes pilot hubs, 10, 50, 100 and stays at 100', () => {
        expect(nextStage(c).percent).toBe(10)
        expect(nextStage({ ...c, percent: 10 }).percent).toBe(50)
        expect(nextStage({ ...c, percent: 50 }).percent).toBe(100)
        expect(nextStage({ ...c, percent: 100 }).percent).toBe(100)
        expect(nextStage(c).pilotHubs).toEqual(['hub-a'])
    })
    it('labels the stage', () => {
        expect(stageLabel(c)).toBe('Pilot hubs only')
        expect(stageLabel({ ...c, percent: 50 })).toBe(
            'Pilot hubs and 50 % of hubs'
        )
        expect(stageLabel({ ...c, percent: 100 })).toBe('All hubs')
        expect(stageLabel({ ...c, pilotHubs: [], percent: 0 })).toBe('No hubs')
    })
})
