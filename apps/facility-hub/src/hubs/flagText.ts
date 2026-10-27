import i18n from '@dhis2/d2-i18n'
import { FlagKind, Heartbeat } from './flags'
import { ago, gb } from './format'

/** The short label of a flag, for its tag. */
export const flagLabel = (kind: FlagKind): string =>
    ({
        never: i18n.t('Never reported'),
        silent: i18n.t('Silent'),
        pending: i18n.t('Pending data'),
        refused: i18n.t('Refused changes'),
        disk: i18n.t('Low disk'),
        usb: i18n.t('USB stick'),
        token: i18n.t('Token expired'),
        upgrade: i18n.t('Upgrade failed'),
        needsUpgrade: i18n.t('Needs upgrade'),
        unprotected: i18n.t('Unprotected entries'),
    })[kind]

/** A sentence explaining a flag, for the hub's page. */
export const flagSentence = (kind: FlagKind, hb?: Heartbeat): string => {
    switch (kind) {
        case 'never':
            return i18n.t(
                'This hub has never reported – it may not have finished its set-up, or cannot reach central.'
            )
        case 'silent':
            return i18n.t(
                'No heartbeat since {{- when}} – the hub may be off, or cannot reach central.',
                {
                    when: ago(hb?.time),
                }
            )
        case 'pending':
            return (hb?.stuckOutbox ?? 0) > 0
                ? i18n.t(
                      '{{- n}} saves wait for their user to log in on the hub again. After 7 days they become refused saves, which can be resent as another user.',
                      { n: hb?.stuckOutbox }
                  )
                : i18n.t(
                      '{{- n}} saves have waited more than a day to reach central (last sent {{- when}}).',
                      {
                          n: hb?.pendingOutbox,
                          when: ago(hb?.lastPush),
                      }
                  )
        case 'refused':
            return i18n.t(
                'Central refused {{- n}} changes made on this hub – see them below.',
                { n: hb?.issues }
            )
        case 'disk':
            return i18n.t("Only {{- free}} free on the hub's disk.", {
                free: gb(hb?.diskFreeBytes),
            })
        case 'usb':
            return hb?.usb?.present
                ? i18n.t(
                      "The hub's USB stick does not work ({{- error}}) – replace it.",
                      {
                          error: hb?.usb?.error || i18n.t('not writable'),
                      }
                  )
                : i18n.t(
                      "No USB stick in the hub – put the box's stick back, it holds the copy a new disk is restored from."
                  )
        case 'token':
            return i18n.t(
                'The token the hub uses for its housekeeping has expired – a staff member should take the hub over on its page.'
            )
        case 'upgrade':
            return i18n.t('The last upgrade failed – {{- reason}}', {
                reason: hb?.lastUpgrade?.reason ?? '',
            })
        case 'needsUpgrade':
            return i18n.t(
                'Central runs a DHIS2 version this hub release does not support – it only passes requests through until it is upgraded.'
            )
        case 'unprotected':
            return i18n.t(
                'Any user can change these entries on central – {{- entries}}. Check each, then protect it with sharing, read only for the public. Saving a setting in this app protects its entry; a hub protects its own registry entry once the hub owners group is named on the Approvals page.',
                { entries: (hb?.unprotected ?? []).join(', ') }
            )
    }
}
