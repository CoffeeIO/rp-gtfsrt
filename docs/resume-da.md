# rp-gtfsrt – kort resumé

**Se demoen:** https://coffeeio.github.io/rp-gtfsrt/ – en optaget S-togsdag
(onsdag 28. oktober 2026) afspillet i browseren, med køreplanstid og realtid
side om side. Man kan skifte mellem data fra SIRI og fra API'et.

**Problemet.** Google Maps og andre rejseapps viser kun køreplanstider i
Danmark – ikke forsinkelser og aflysninger. Rejseplanen har realtidsdata og
udgiver dem allerede som SIRI på Dataudveksleren (det nationale
adgangspunkt), men ikke i det format (GTFS-Realtime), som Google og andre
rejseapps tager imod.

**Løsningen.** rp-gtfsrt er en færdig open source-bro, der omsætter
Rejseplanens realtidsdata til GTFS-Realtime for hele Danmark:

* **Forsinkelser** for hvert stop på hver tur – tog, S-tog, metro, letbane
  og bus
* **Aflysninger**, både hele ture og enkelte stop
* **Trafikmeddelelser** knyttet til de berørte stoppesteder

Broen abonnerer på Rejseplanens 11 SIRI-ET-datasæt på Dataudveksleren
(ét pr. operatør) og samler beskederne pr. tur. Hver tur matches til den
rigtige tur i Rejseplanens egen GTFS-køreplan ud fra stopfølge og tider.
Tvetydige tilfælde udelades hellere end at gætte. Den kan også hente fra
Rejseplanens API i stedet.

**Dokumenteret kvalitet:**

* SIRI, live for hele landet (27.–28. oktober 2026): 99 % af turene
  matchet, for alle operatører inklusive S-tog
* SIRI og API optaget samme dag: når begge har en forudsigelse for samme
  stop, er de enige inden for 1 minut i 92,5 % og inden for 2 minutter i
  98 % af tilfældene – og SIRI når flere ture i løbet af dagen
* Alle 44.319 ture i Danmark på én dag: 0 forkerte match
* MobilityData's officielle GTFS-Realtime-validator: 0 fejl
* Ingen API-kald og ingen kvote med SIRI; kører hele landet på én lille
  server

**S-togsdagen i demoen** (onsdag 28. oktober 2026, 07:41–21:00, fra SIRI):

* 1.015 ture med realtidsdata
* 217 ture (21 %) var mindst 3 minutter forsinket i mindst 2 minutter
  undervejs – den største forsinkelse var 16 minutter
* API'et viser samme dag 10 %. Forskellen ligger mest ved de sidste stop
  på turen, hvor SIRI forudsiger mere forsinkelse end API'et

Google Maps viser i dag ingen af disse forsinkelser.

**Klar til drift.** Docker-image, overvågning (Prometheus), sikker
opdatering af køreplanen, en simulator til test uden adgang, og en guide til
at indsende feedet til Google.

**Hvad der mangler for at gå live:**

1. En vært for de tre feeds (Rejseplanen, en operatør eller en partner) –
   eller at Rejseplanen udstiller GTFS-Realtime direkte fra egne systemer
2. At ejeren af GTFS-feedet hos Google tilføjer realtidsfeedet

Koden er gratis (Apache-2.0): https://github.com/CoffeeIO/rp-gtfsrt
