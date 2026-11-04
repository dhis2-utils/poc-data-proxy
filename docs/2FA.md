# Facility hub: two-factor login

A study of two-factor authentication (2FA) for the hub, written 4 November 2026 on the `study-2fa` branch (base f70c7d5, the same tree as `ready-all` 601519e). The question was whether supporting it would be difficult. Nothing here changes the hub. The options at the end are proposals, and the decisions marked "Olav" are open.

Everything in the first two tables was checked on DHIS2 2.42.7.1 and 2.43.2.1 (broker instances from the Sierra Leone seeds), on Android Capture 3.5.0 on the Samsung tablet, and with Chromium against central and against a hub running the live test kit. Screenshots are in `windows/out/2fa/` in the shared repo. Rows marked "source" come from reading Capture 3.5.0 and its SDK 1.15.0, because neither instance nor the demo central has the setting that turns that path on.

## Central

| | 2.42.7 | 2.43.2 |
|---|---|---|
| Kinds of 2FA | TOTP (authenticator app) and email. TOTP is on by default; email needs `login.security.email_2fa.enabled = on` in dhis.conf, a mail server and a verified address. Email answered 409 "Email based 2FA is not enabled" (E3045) here. `GET /api/configuration/twoFactorMethods` says which are on. | same |
| How a user enrols | Profile app, or `POST /api/2fa/enrollTOTP2FA`, `GET /api/2fa/qrCodeJson` (the secret, only while enrolling), `POST /api/2fa/enabled` with a code. An admin can turn it off for a user: `POST /api/users/{uid}/twoFA/disabled`. Central cannot require 2FA of a user or a role. | same |
| `/api/auth/login`, no code or a wrong code | 200 `{"loginStatus":"INCORRECT_TWO_FACTOR_CODE_TOTP"}`, no cookie | same |
| `/api/auth/login`, wrong password (with any code) | 401 "Bad credentials" | same |
| `/api/auth/login`, right code | 200 SUCCESS and a session cookie. The cookie works for reads and tracker writes; it lasts `system.session.timeout`, 1 hour by default. | same |
| Basic auth on `/api/me` | 401 | same |
| A PAT made after 2FA was turned on | Made without error, then refused on every request: 401 "The API token is disabled, locked or 2FA is enabled." | same |
| A PAT made and used before 2FA was turned on | not tested | kept working after 2FA was turned on: 200 for 34 minutes, then 401 after a restart of central, so a cache on central. A PAT made before but first used after was refused. |
| Wrong codes in a row | no lockout: 7 wrong codes, then the right one, logged in, also with `keyLockMultipleFailedLogins` on | same |

Two things follow for any client. The answer to a right password is different from a wrong one (200 against 401), so the code is the only thing between a known password and the account. And nothing on central slows down guessing of codes.

## Clients

| | Android Capture 3.5.0 | Capture web, against central | Capture web, through the hub today |
|---|---|---|---|
| Online login of a 2FA user | Fails: "The username or password are incorrect". It sends Basic auth, central answers 401. Same on 2.42 and 2.43 (screenshots `a42-01`, `a43-01`). | Asks for the code after the password ("Two-factor authentication", `web42-central-2`), then logs in. | The login screen asks for the code (the hub passes central's answer on, `web42-hub-2`) and the login succeeds. The next request fails: the hub sends the user's PAT upstream, and central refuses it (401, `web42-hub-3`). |
| The hub's own page (`/hub/login`) | n/a | n/a | "Login failed (HTTP 200)": the form has no code field. |
| Offline login | Password only, checked against a hash on the tablet. A user who turned 2FA on after an online login still logged in offline with the password alone (`a42-04`), then failed online (`a42-05`). | n/a | Password only, until the hub's first sync cycle with that user's PAT. The cycle gets 401 and marks the user "needs login". After that the hub refuses the user offline ("needs connection"), and every online login clears the mark until the next cycle sets it again. |
| Changes made offline | Basic auth on sync (a 2FA user cannot get this far). | n/a | Never reach central: the push uses the refused PAT. They wait for an online login that does not help, and are refused after 7 days (`loginWait`). |
| Wrong codes | n/a | not limited by central | not limited by the hub either: the throttle counts only 401, and wrong codes answer 200 (14 in a row passed). This is F2 of the 27 October review, still open. |
| 2FA with OAuth2 (source) | On 2.43 and later, if central runs its own authorization server (`oauth2.server.enabled = on`, off by default), Capture enrols the device once in the browser (`/api/auth/enrollDevice`, then RFC 7591 registration with a key kept in the Android keystore). It logs in through central's own login page, which asks for the code, and gets an access token and a 30-day rotating refresh token. Offline it asks for a 4-digit offline code or biometrics, not the 2FA code. Capture's own 2FA settings screen is only shown for these accounts. | n/a | n/a |

So FINDINGS item 13 is out of date in one point. It says web 2FA users log in offline with the password alone. That holds only until the first sync cycle; after it a 2FA user cannot use the hub at all, online or offline.

Android is the reference here, and on its own it gives no 2FA offline either. With Basic it cannot log in a 2FA user at all. With OAuth2 the second factor offline is the enrolled device plus a short code, and the 2FA code is asked only online, by central.

## Options

The options below are for offline login. Each of them first needs one change, because today a 2FA user is locked out of the hub.

**Needed first: a credential that central accepts.** For 2FA users the hub cannot use a PAT, online or for pushes. The hub already reads `/api/me` at login; `twoFactorType` (`TOTP_ENABLED`, `EMAIL_ENABLED`) tells it.

- Online, use the session's central cookie for that user instead of the PAT (`proxy.Upstream`, `core.UpstreamAuth`). The cookie works for reads and writes; checked by hand on 2.42.
- Make no PAT for these users, so the sync cycle does not mark them "needs login".
- Pull their data with the cookie while they have a session, and otherwise with the hub token where it is enough.
- Push their offline changes only with the cookie of their next online login. The cookie lives 1 hour by default, so the push must start at the login. The refusal text says "log in online to send".
- Count `INCORRECT_TWO_FACTOR_CODE_*` as a failed login in the throttle (F2).

Cost: one more credential kind in the push and pull paths, with its tests on 2.42 and 2.43. Staff: nothing new. It fails when the user does not log in online for 7 days: the changes are refused, and a manager can resend them as another user, as now.

**(a) A TOTP of the hub's own.** At the first online login the hub shows a QR code and keeps a secret per user. Offline it answers the login screen with `INCORRECT_TWO_FACTOR_CODE_TOTP`, so the stock login app asks for the code without a new screen.
- Store: a secret per user. Offline login must check it before the store is unlocked, so it lives in the key file, sealed with the device key where there is one. It cannot be bound into the key slot, as a code changes every 30 seconds and the slot needs a fixed secret.
- Code: an enrolment screen, a check in `loginOffline`, a reset on the hub page, the throttle on codes.
- Staff: a second authenticator entry, "Facility hub", next to central's. Central's secret cannot be reused: central shows it only while the user enrols, and staff enrol in the profile app, not through the hub.
- Failure: a lost or new phone locks the user out offline until a hub manager resets the hub's secret (online, as hub admin). A phone without a clock that the hub trusts (NO-RTC.md) refuses right codes.
- Android: no gain. Android cannot log in a 2FA user at all.

**(b) A registered browser.** At an online login with the code, the hub sets a long-lived device cookie and keeps its hash. Offline it asks for the password and that cookie.
- Store: a table of (user, device hash, first and last use), with a list and a "forget" button on the hub page.
- Code: about the size of the refusal pages. No new screen for staff; the offline refusal says "log in once online from this browser".
- Staff: nothing new.
- Failure: a new tablet, another browser or cleared site data locks the user out offline until the next online login. On a PC shared by all staff the factor means "this PC", which offline login at the facility already implies.
- This is Android's own model under OAuth2: device enrolled online, short check offline.

**(c) Accept the gap and document it.** Do the change in "Needed first", and keep offline login password-only for 2FA users. Write it in DEPLOYMENT.md: a country that relies on 2FA gets password-only logins offline at the facility.

**(d) 2FA users online only.** At login, a user with 2FA gets no key slot and no PAT. The hub passes them to central with their session cookie, as it does already for users who are not facility staff ("pass-through only", REVIEW-2 F5). Offline the hub refuses them, as it refuses any user it does not know.
- Code: a check in `registerOnline` and a test; the online path exists. The throttle fix is part of it.
- Staff: a 2FA user who must work offline needs an account without 2FA. The country decides who that is.
- Failure: none beyond today's pass-through; 2FA keeps its full meaning.

**(e) OAuth2 like Android.** Where central runs its authorization server, the hub enrols itself once, and each user logs in through central's login page in the browser. The hub keeps that user's 30-day refresh token, sealed under the data key, and pushes with it. Offline login stays as in (b) or (c). This is the only option that sends a 2FA user's offline changes days later as that user. It needs dhis.conf changes on central (`server.base.url`, `oauth2.server.enabled`, a signing keystore), which neither test instance nor the demo central has. It is also the only way to support 2FA users on Android through the hub. That would mean the hub answers Capture's OAuth2 discovery and passes the browser steps to central. Untested; months, not days.

## Online Android through the hub

No separate fix is possible today. Capture 3.5.0 has no code prompt for Basic accounts, so a 2FA user fails directly against central exactly as through the hub (tested on the tablet against 2.42 and 2.43). Android 2FA works only through OAuth2 (option (e)).

## Recommendation

Do (d) now: 2FA users online only, with the throttle counting wrong codes. It is small, it ends today's lock-out, and it keeps 2FA whole on the hub. If a country needs 2FA staff offline, add (b) on top. It costs staff nothing, matches Android's model, and fails in a way an online login repairs. Leave (a) out: a second authenticator entry and a manager reset for each lost phone are a high price for a code the hub alone checks. Keep (e) in view for the day a country turns on central's authorization server.

Decisions for Olav:

1. Are 2FA accounts online only on the hub (d), or offline with the password alone (c)?
2. If 2FA staff must work offline: is password plus a registered browser (b) enough?
3. Is Android 2FA through the hub out of scope until central's OAuth2 is in use (e)?
4. Correct FINDINGS item 13 once these are decided.

## Not confirmed

- The OAuth2 path of Android (enrolment, browser login with the code, offline code, refresh tokens) is from source and DHIS2's documentation only. No instance available had `oauth2.server.enabled`. The documentation also says the device enrolment was backported to 2.42 (DHIS2-19948), but SDK 1.15.0 uses OAuth2 only from 2.43.
- Email 2FA was not tried; it needs dhis.conf and a mail server. Central's login answers for it (`INCORRECT_TWO_FACTOR_CODE_EMAIL`, a sent code) are not checked.
- How long a PAT used before 2FA keeps working on 2.43 without a restart (over 34 minutes) was not followed to the end; the hub should not rely on it.
- The session-cookie credential in "Needed first" and option (d) are not built. Only the cookie's reads and writes were checked by hand.
- How Android's sync behaves when its user turns 2FA on while logged in: the next sync reported success, but whether it reached central (with a central session cookie from the login) was not checked.
