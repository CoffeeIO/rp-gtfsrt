# rp-gtfsrt – kort resumé

**Problemet.** Google Maps og andre rejseapps viser kun køreplanstider i
Danmark – ikke forsinkelser og aflysninger. Rejseplanen har realtidsdata,
men de udgives ikke i det standardformat (GTFS-Realtime), som Google og
andre tjenester bruger.

**Løsningen.** rp-gtfsrt er en færdig open source-bro, der omsætter
Rejseplanens realtidsdata til GTFS-Realtime for hele Danmark:

* **Forsinkelser** for hvert stop på hver tur – tog, S-tog, metro, letbane
  og bus
* **Aflysninger**, både hele ture og enkelte stop
* **Køretøjers position**
* **Trafikmeddelelser** knyttet til de berørte stoppesteder

Hver realtidsobservation matches til den rigtige tur i Rejseplanens egen
GTFS-køreplan ud fra stopfølge og tider. Tvetydige tilfælde udelades
hellere end at gætte.

**Dokumenteret kvalitet** (8.–9. oktober 2026):

* Alle 44.319 ture i Danmark på én dag: 0 forkerte match
* Live-test mod Rejseplanens API: 1.345 af 1.345 afgange matchet korrekt
* MobilityData's officielle GTFS-Realtime-validator: 0 fejl
* Kører hele landet på én lille server (ca. 330 MB hukommelse)

**Klar til drift.** Docker-image, overvågning (Prometheus), sikker ugentlig
opdatering af køreplanen, en simulator til test uden API-nøgle, og en guide
til at indsende feedet til Google.

**Hvad der mangler for at gå live:**

1. Adgang til Rejseplanens API uden den gratis nøgles kvotebegrænsning
2. En vært for de tre feeds (Rejseplanen, en operatør eller en partner)
3. At ejeren af GTFS-feedet hos Google tilføjer realtidsfeedet

Koden er gratis (Apache-2.0): https://github.com/CoffeeIO/rp-gtfsrt
