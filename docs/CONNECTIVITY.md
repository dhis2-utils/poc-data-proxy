# Connectivity at facilities

What links to central look like where hubs go, and what the hub's tests should model. The measurements below were compiled on 6 November 2026 from published sources (section "Measurements"). The project has not checked them itself. The review that follows them is the project's reading, for its own use.

## How to use this document

- Test link conditions (`testkit.Netcut`, docs/PERFORMANCE.md, "A slow uplink") are set from the planning figures here and from field measurements, and this document says which.
- A national average flatters a facility: the one field measurement the project has, St Joseph Buyege HC in Uganda (29 October 2026), was 31 KB/s, about 0.25 Mbps, against 10–23 Mbps for Uganda's operators.
- Add field measurements to "Field measurements" as they come in.

## Field measurements

| Where, when | What was measured | Source |
|---|---|---|
| St Joseph Buyege HC, Butambala, Uganda, 29 Oct 2026 | 31 KB/s download | Field visit notes |
| Butaaka and St Joseph Buyege HC, 28–29 Oct 2026 | Registering a patient: about a minute, 3 minutes on a poor network | Field visit notes |

## Measurements: mobile networks in sub-Saharan Africa

Compiled 6 November 2026. All figures come from published measurements; none are estimates.

### Summary

Nationwide crowdsourced tests (MedUX, 2024–2026) show the main operators in most countries averaging 10–30 Mbps download. Smaller and state-owned operators often average 2–8 Mbps. Latency splits by region more than by income. East and Southern Africa mostly measure 35–80 ms. Much of West and Central Africa measures 100–220 ms, and Nigeria is 151–163 ms on every operator. Packet loss is 1–3% on the better networks and 4–9% on many others. Time to first byte ranges from 0.6 to 2 seconds.

These are averages. They flatter the experience of a typical user and say nothing about outages. Rural-specific measurements of latency and packet loss are almost absent from the published record.

### National measurements by country

Source: MedUX Connectivity Observatory. Tests run from phone apps across the whole country and on 3G, 4G and 5G combined. Each cell shows the range across operators, from the lowest operator to the highest. "–" means MedUX does not report that metric for the country.

| Country (data period) | Download, Mbps | Latency, ms | Packet loss, % | Time to first byte, s |
|---|---|---|---|---|
| Kenya (May–Jul 2025) | 9.4–31.1 | 35–220 | 1.1–2.7 | 0.56–1.45 |
| Uganda (Apr–Oct 2025) | 10.4–23.1 | 41–100 | 3.0–5.5 | 0.73–1.08 |
| Tanzania (Aug 2021) | 4.2–9.1 | 109–235 | – | – |
| DR Congo (Oct–Dec 2025) | 15.3–25.7 | 84–113 | 4.1–8.3 | 1.29–2.13 |
| Congo-Brazzaville (Jul–Aug 2026) | 20.3–37.9 | 92–100 | – | – |
| Central African Rep. (Oct–Dec 2025) | 4.3–21.0 | 167–204 | – | – |
| Cameroon (Oct–Dec 2025) | 4.5–22.3 | 153–199 | 3.4–8.9 | 1.04–1.98 |
| Nigeria (Aug–Oct 2025) | 12.9–37.6 | 151–163 | 6.1–7.4 | 0.89–1.09 |
| Ghana (Jan–Feb 2025) | 6.4–16.9 | 40–131 | 1.2–2.0 | 0.69–1.32 |
| Côte d'Ivoire (Sep–Dec 2024) | 16.3–27.1 | 44–124 | – | – |
| Burkina Faso (Jul–Aug 2026) | 19.6–34.4 | 67–104 | – | – |
| Mali (Oct–Nov 2025) | 4.4–31.2 | 106–165 | 2.8–4.4 | 1.05–1.31 |
| Niger (Oct–Dec 2025) | 1.9–23.0 | 148–223 | – | – |
| Senegal (Oct–Nov 2025) | 7.7–17.4 | 33–105 | 2.2–5.3 | 0.77–1.10 |
| Guinea (Oct–Dec 2025) | 5.6–16.8 | 54–157 | – | – |
| Guinea-Bissau (Oct–Dec 2025) | 15.0–21.4 | 38–109 | 2.7–6.6 | 0.90–1.22 |
| Sierra Leone (Oct–Dec 2024) | 4.5–17.0 | 133–158 | – | – |
| Liberia (Oct–Dec 2025) | 28.3–31.2 | 67–142 | 4.9–5.1 | 0.94–1.10 |
| Zambia (Oct–Nov 2023) | 8.2–15.1 | 69–75 | – | – |
| Malawi (Nov 2021) | 13.2–14.6 | 63–114 | – | – |
| Mozambique (Oct–Nov 2025) | 10.8–25.6 | 46–82 | 2.7–8.1 | 0.73–1.41 |
| Zimbabwe (Jan–Mar 2025) | 3.2–28.0 | 60–79 | 1.3–2.4 | 0.78–1.23 |
| Madagascar (Nov 2024–Jan 2025) | 11.9–34.3 | 33–106 | – | – |
| Botswana (Aug–Dec 2025) | 23.2–42.7 | 35–55 | 0.7–2.0 | 0.68–1.00 |
| South Africa (Jun–Aug 2025) | 17.7–44.8 | 25–34 | 3.6–8.0 | 0.60–0.75 |

How MedUX measures:

- **Latency:** the lowest of 10 pings to an in-country or on-net test server. The figure is a best case.
- **Packet loss:** measured over UDP with 100-byte packets, on an idle connection after the speed test.
- **Download:** multi-threaded HTTP, designed to measure the maximum available bandwidth.
- **Sample size:** roughly 30,000–700,000 tests per country.

There is no MedUX data for Ethiopia. The Tanzania, Malawi and Zambia reports are from 2021–2023.

### Rural and urban gaps

Rural-specific data is scarce. These are the only figures I found:

- **Kenya, Tanzania and South Africa:** mean latency in rural areas is more than 35 ms higher than in cities (Q4 2024). Source: OECD, using Ookla data.
- **Nigeria:** rural median download rose from 7.5 Mbps in January 2025 to 11 Mbps in December 2025. That is 40% below urban download, and rural upload is 65% below urban. Source: a report commissioned by the Nigerian Communications Commission (NCC), cited by an Ookla analyst. I only saw this second-hand.
- **Sub-Saharan Africa as a whole:** the coverage gap is 10% and the usage gap is 65% (GSMA, 2025 report on 2024 data). Rural adults are about 48% less likely than urban adults to use mobile internet; I only saw this figure in secondary coverage.

### Reliability and outages

Average speed tests do not capture outages. Two older field studies from health projects do:

- **Rural Uganda (Siedner et al. 2012):** a median of 1.5 data outages longer than 48 hours per person-month on devices using GPRS only. Adding SMS as a fallback cut this to 0.3.
- **Rural KwaZulu-Natal (published around 2014):** none of the remote clinics had 3G, and connections fell back to GPRS.

Opensignal's Consistent Quality metric counts how often a connection is good enough for demanding apps. In Nigeria (April–June 2026), 18.5% of tests on the best network (MTN) met the threshold, and 11.3% on Glo. MedUX publishes a consistency score of 22–55% for Ghana and 32–54% for Zimbabwe, but I could not find how it is defined.

### Caveats

- **Averages hide the bad tail.** MedUX does not publish percentiles, so the experience of a typical user is worse than the averages suggest.
- **Tests only run when there is a connection.** Areas with no coverage, and periods of outage, never appear in the data. Testers also skew towards smartphone users in towns.
- **Latency to a remote server will be higher.** The figures measure latency to a nearby test server. A DHIS2 server hosted in another country or region adds more, and latency rises further under load.
- **The West and Central Africa latency pattern needs checking.** The cause is likely traffic routed outside the country instead of through local exchange points or content servers. That is my inference; MedUX does not measure it.

### Planning figures (the compiler's assessment, not a measurement)

For system design outside the best networks, plan for:

- 1–5 Mbps of real throughput
- 150–300 ms round-trip time to the server
- a few percent packet loss
- outages lasting hours or days

### Sources

- MedUX Connectivity Observatory, Africa country reports: https://observatory.medux.com/ (for example Kenya Jul 2025, https://observatory.medux.com/africa/kenya/ke-july-2025/; Nigeria Oct 2025, https://observatory.medux.com/africa/nigeria/ng-october-2025/; Uganda Oct 2025, https://observatory.medux.com/africa/uganda/ug-october-2025/)
- OECD, Closing Broadband Connectivity Divides for All: https://www.oecd.org/en/publications/closing-broadband-connectivity-divides-for-all_d5ea99b2-en/full-report/state-of-connectivity-between-and-within-countries_e8df092b.html
- tech.africa, Ookla analyst on Nigeria rural speeds: https://tech.africa/ookla-5g-africa-ranking-analysis/
- GSMA, State of Mobile Internet Connectivity 2025: https://www.gsma.com/somic/wp-content/uploads/2025/09/The-State-of-Mobile-Internet-Connectivity-2025-Network-Coverage-and-Infrastructure.pdf
- Ecofin, GSMA rural usage gap: https://www.ecofinagency.com/news-digital/1109-48612-sub-saharan-africa-drove-2024-s-global-mobile-coverage-growth-gsma-finds
- Opensignal, Nigeria Mobile Network Experience, July 2026: https://insights.opensignal.com/reports/2026/07/nigeria/mobile-network-experience
- Siedner MJ et al. (2012), Optimizing network connectivity for mobile health technologies in sub-Saharan Africa, PLoS ONE: https://journals.plos.org/plosone/article?id=10.1371/journal.pone.0045643
- 3G mobile communications for telehealth in rural KwaZulu-Natal: https://pmc.ncbi.nlm.nih.gov/articles/PMC4312784/

## Review: what this means for the hub

**The time to first byte is the number that matters.** Capture web makes about 30 requests per patient, each waiting on a round trip. MedUX's latency is the lowest of 10 pings to a server in the country, a best case; its time to first byte, 0.6–2 s per request, is closer to what each of Capture's requests waits for. A new HTTPS connection costs two or three round trips before its first byte (TCP, then TLS); reused connections save that. The project's slow-uplink tests added 200 ms per request, which is optimistic: at 1 s per request, the 30 requests of a patient take 15–30 s of waiting, depending on how many run at once.

**Packet loss turns into stalls.** MedUX measures loss over UDP on an idle line. Over TCP, a lost packet is resent after a timeout, at least 200 ms on Linux and often a second, and the request waits for it; a loaded line loses more than an idle one. At 3–9 % loss, a screen of 15 requests is likely to have one stalled request. The test link does not model loss yet.

**Throughput at a facility is far below national figures.** The planning figure of 1–5 Mbps is 4–20 times what Buyege measured. Many staff, a shared modem, a weak signal indoors and data bundles all reduce it. For the hub, plan for tens of KB/s, and check compression on central (FINDINGS.md, "The demo central does not compress its answers").

**Outages last hours or days.** The two field studies are old (2012, about 2014) and from GPRS times, but match what the hub is built for: work offline for days, then catch up.

**What the measurements leave out:** percentiles (the bad tail is what staff live with), rural latency and loss, the distance to a national server hosted abroad, and the facility's own network. Some figures are second-hand (Nigeria rural, GSMA's usage gap).

**Test link conditions to use** (from the above, until field measurements say otherwise):

| Condition | Rate | Wait per request | Loss | Use |
|---|---|---|---|---|
| Good | 3 000 KB/s | 200 ms | none | best case, as measured so far |
| Typical rural | 31 KB/s | 1 s | 3 % | the field site's rate, MedUX's time to first byte for Uganda |
| Poor | 31 KB/s | 2 s | 8 % | the worst operators' figures |
| Outage | none | | | hours to days |
