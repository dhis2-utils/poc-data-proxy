import { Tag } from '@dhis2/ui'
import React from 'react'
import styles from './FlagTags.module.css'
import { Flag } from '@/hubs/flags'
import { flagLabel } from '@/hubs/flagText'

export const FlagTags = ({ flags }: { flags: Flag[] }) => (
    <span className={styles.tags}>
        {flags.map((f) => (
            <Tag
                key={f.kind}
                negative={f.kind !== 'needsUpgrade'}
                neutral={f.kind === 'needsUpgrade'}
            >
                {flagLabel(f.kind)}
            </Tag>
        ))}
    </span>
)
