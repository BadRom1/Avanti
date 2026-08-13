# Architecture d'Avanti

Ce document décrit la forme du code d'Avanti et, surtout, les règles qui la
maintiennent. Il est prescriptif : ce qui est écrit ici est vérifié
automatiquement partout où c'est possible, et une divergence entre ce document et
le code est un bug de l'un ou de l'autre.

## 1. Forme générale : un monolithe modulaire hexagonal

Avanti est **un seul binaire**, déployé seul, qui parle à **une seule base**
PostgreSQL. Ce n'est pas un choix par défaut : c'est le format qui correspond à
l'usage visé — une instance auto-hébergée par un particulier ou un petit
collectif qui reconstruit une maison. Une architecture distribuée coûterait ici
un ordre de grandeur d'exploitation pour zéro bénéfice.

« Monolithe » décrit le déploiement, pas l'organisation interne. À l'intérieur,
le code est découpé en **domaines** indépendants, chacun construit en
**architecture hexagonale** : le métier au centre, exprimé en Go pur, et le monde
extérieur — base de données, HTTP, fichiers, SMTP, agents IA — branché par des
**ports** que le domaine définit et que des **adapters** implémentent.

Le bénéfice recherché est concret : la logique métier se teste sans base ni
serveur, elle survit au remplacement d'une brique technique, et un domaine peut
être extrait plus tard si le besoin apparaît — sans que ce soit un objectif.

```
                    ┌──────────────────────────────────┐
                    │           cmd/avanti             │
                    │  (assemble : lit la config,      │
                    │   choisit les implémentations)   │
                    └────────────┬─────────────────────┘
                                 │ injecte
             ┌───────────────────┴────────────────────┐
             ▼                                        ▼
   ┌────────────────────┐                  ┌────────────────────────┐
   │  internal/adapters │  implémentent    │  internal/<domaine>    │
   │  postgres  web     │ ───────────────▶ │  entités               │
   │  mcp       storage │     les ports    │  ports (interfaces)    │
   │  mail      export  │                  │  services applicatifs  │
   └─────────┬──────────┘                  └────────────────────────┘
             │ utilisent                        (n'importe rien du projet)
             ▼
   ┌────────────────────┐
   │ internal/platform  │  config, logs, pool DB, serveur HTTP
   └────────────────────┘
```

Le sens des flèches est l'essentiel : **toute dépendance pointe vers le
domaine**, jamais l'inverse.

## 2. Les règles de frontières

Quatre règles, sans exception. Trois d'entre elles ne relèvent pas du goût :
elles sont encodées dans `depguard` (voir `.golangci.yml`), donc une violation
fait échouer `make lint`, donc la CI, donc la fusion. La quatrième, R2, n'est
pas mécanisable — voir « Vérification automatique » en fin de section.

### R1 — Un domaine n'importe rien du projet

Un package `internal/<domaine>` n'importe **ni un autre domaine, ni un adapter,
ni `internal/platform`**. Il n'a le droit qu'à la bibliothèque standard et à des
bibliothèques métier sans effet de bord — et chacune de ces bibliothèques doit
être inscrite nommément dans la règle `depguard` du domaine, ce qui fait de son
adoption une décision visible en revue plutôt qu'un import de plus.

Conséquences pratiques :

- pas de `*pgxpool.Pool` dans un service de domaine — le domaine déclare une
  interface `Repository` et reçoit une implémentation ;
- pas de `slog.Logger` global — la journalisation est le travail de l'appelant ;
- pas de `os.Getenv` — la configuration arrive en paramètre de construction.

### R2 — Les références inter-domaines sont des identifiants faibles

Une `Facture` du domaine `finance` concerne un `Devis` du domaine `devis`. Elle
le désigne par **un identifiant**, `Facture.devisID`, jamais par un pointeur vers
l'agrégat `Devis` ni par un import du package.

C'est ce qui rend R1 tenable. Le prix est assumé : pas de jointure en mémoire
entre agrégats de domaines différents, et l'assemblage d'une vue transverse (un
tableau de bord qui mêle devis, factures et étapes) se fait dans l'adapter web,
en interrogeant chaque domaine puis en composant le résultat. Ce prix est
volontaire : il empêche le glissement progressif vers une grosse boule de boue où
tout connaît tout.

Un identifiant faible n'est pas une chaîne nue : chaque domaine exporte son
propre type d'identifiant (`devis.ID`, `finance.ID`), ce qui interdit au
compilateur de confondre l'identifiant d'un devis avec celui d'une facture.

### R3 — `internal/platform` ne connaît ni domaine ni adapter

`platform` est le socle technique : configuration, journalisation, pool de
connexions, cycle de vie du serveur HTTP, informations de build. C'est la couche
la plus basse, et rien ne dépend d'elle en retour. Elle ne doit jamais devenir le
fourre-tout où atterrit le code que personne ne sait où mettre.

Concrètement, un paquet par responsabilité, tous sous la même règle `depguard` :
`config` (variables `AVANTI_`, validées d'un bloc au démarrage), `logging`
(`slog`), `db` (pool `pgx` et sonde de disponibilité), `migrate` (SQL embarqué,
rejoué par `goose`), `server` (délais d'attente, intergiciels, sondes
d'exploitation, arrêt gracieux). Deux conséquences directes de R3 méritent d'être
notées, parce qu'elles ne sont pas mécanisables :

- **le serveur ne connaît pas les pages qu'il sert.** Les routes applicatives lui
  arrivent en `http.Handler`, que `cmd/avanti` lui passe. C'est ce qui lui permet
  d'ignorer `adapters/web` ;
- **le socle ne décide pas de la vie du processus.** `Serve` s'arrête sur
  annulation de contexte ; le gestionnaire de `SIGINT` et `SIGTERM` est installé
  par `cmd/avanti`, seul endroit légitime pour trancher quand le programme meurt.

### R4 — Seul `cmd/` assemble

`cmd/avanti` est **le seul endroit du dépôt** qui a le droit de connaître à la
fois les domaines et les adapters. Il lit la configuration, instancie les
adapters concrets, les injecte dans les services de domaine et démarre le
serveur. Tout le reste du code ignore quelles implémentations sont branchées.

Cela vaut aussi **entre familles d'adapters** : `adapters/web` n'importe pas
`adapters/postgres`. Une vue transverse s'assemble en interrogeant les services
des domaines, pas en court-circuitant vers la couche de persistance d'à côté.

C'est ce qui rend le test simple : un service de domaine reçoit un faux
`Repository` en trois lignes, sans base ni conteneur.

### Vérification automatique

Les règles R1, R3 et R4 sont vérifiées **structurellement** par `depguard` à
chaque `make lint`. Chaque répertoire du dépôt a sa règle, en `list-mode:
strict` : elle énumère ce que ce répertoire a le droit d'importer, et tout le
reste est refusé. `cmd/avanti` est la seule exception, sans règle de frontière,
parce que c'est l'assembleur.

Ce sont des listes d'autorisation, pas d'interdiction, et ce choix est le cœur
du dispositif. Une liste d'interdiction énumère l'interdit : ce qu'on oublie
d'y écrire — un sixième domaine, une nouvelle famille d'adapters — devient
autorisé **en silence**, et la brèche peut vivre des mois. Une liste
d'autorisation énumère le permis : le même oubli rend l'import interdit, et il
se manifeste **bruyamment** dès la première tentative, avec un message qui
indique quoi ajouter et où. L'entretien est le même dans les deux cas ; seule la
direction de l'échec change, et c'est elle qui décide si l'architecture tient.

Un garde-fou complète le dispositif : tout package sous `internal/` qui n'a pas
sa propre règle ne voit que la bibliothèque standard. Créer un répertoire sans
déclarer ses frontières ne passe donc pas inaperçu.

Les règles ont été validées en introduisant délibérément des imports interdits —
domaine vers domaine, domaine vers adapter, `platform` vers domaine, adapter
vers adapter d'une autre famille, package interne non déclaré : le lint les a
tous rejetés, avec le message expliquant l'alternative à suivre. Les imports
légitimes symétriques (adapter vers domaine et vers `platform`, package vers son
propre sous-arbre, `cmd/` vers tout) ont été vérifiés comme passants.

R2 n'est pas mécanisable par un linter : elle relève de la revue de code.

## 3. Le modèle d'extension : les ports sont les points d'extension

Avanti n'a pas de système de plugins au sens d'un chargement dynamique de code.
**Les ports du domaine *sont* l'interface d'extension officielle.**

Concrètement, étendre Avanti consiste à :

1. implémenter en Go une interface déjà définie par un domaine — par exemple le
   port de stockage du domaine `document`, pour envoyer les pièces vers un objet
   compatible S3 plutôt que sur le disque local ;
2. enregistrer cette implémentation dans la configuration, que `cmd/avanti` lit
   au démarrage pour choisir quoi brancher.

Ce que cela implique : une extension se compile avec le binaire. Il faut
reconstruire Avanti pour l'activer.

**Pourquoi pas les `plugin` de Go ni un moteur de scripts en V1 ?** Le paquet
`plugin` de la bibliothèque standard impose que le plugin et l'hôte soient
compilés avec exactement la même version de Go et les mêmes versions de toutes
les dépendances communes ; en pratique, cela transforme chaque mise à jour en
casse silencieuse. Un moteur de scripts embarqué ajouterait une surface
d'exécution de code arbitraire dans une application qui détient des documents
d'assurance et des données financières. Le rapport bénéfice/risque ne le justifie
pas pour une V1.

La contrepartie est réelle et acceptée : pas d'écosystème d'extensions
installables à chaud. En échange, chaque extension bénéficie du typage statique,
du même harnais de tests et du même lint que le cœur — et la surface d'attaque ne
grandit pas.

## 4. Les domaines et leur périmètre

Quatre domaines métier, plus un domaine transverse d'identité.

Le **vocabulaire métier reste en français** dans les identifiants exportés :
`Devis`, `Facture`, `Acompte`, `Etape`, `Jalon`, `Artisan`. C'est le langage
ubiquitaire du projet — celui des documents papier échangés avec les entreprises
et l'assurance. Traduire « devis » en « quote » créerait une double traduction
permanente entre le code et la réalité, et c'est exactement ce que le langage
ubiquitaire sert à éviter.

La frontière, pour qu'elle ne se discute pas à chaque revue :

- **en anglais, tous les identifiants techniques** — types, fonctions, méthodes,
  constantes, variables, champs de structure, noms de tests et noms de fichiers.
  `Hasher`, `NormalizeEmail`, `sessionCookieName`, `login.go` ;
- **en français, deux choses seulement** : le vocabulaire métier listé ci-dessus,
  et tout ce que l'utilisateur voit ou saisit — routes (`/connexion`,
  `/deconnexion`), noms de champs de formulaire (`mot_de_passe`), valeurs de rôle
  et de scope stockées en base (`proprietaire`, `collaborateur`), clés et textes
  du catalogue i18n, sorties de la CLI, flags (`--nom`, `--role`) et messages
  d'erreur ;
- **les commentaires et la documentation restent en français**, y compris quand
  ils citent un identifiant anglais.

Un nom technique français est donc un écart à corriger, pas un choix de style ;
une chaîne française visible de l'utilisateur ne se traduit pas en anglais sous
prétexte d'uniformité.

### `devis` — la consultation des artisans

Demandes de devis, propositions chiffrées reçues, comparaison des offres,
acceptation qui fait entrer un lot de travaux en exécution. C'est le point
d'entrée de tout le reste : un devis accepté engage un montant (que `finance`
suivra) et déclenche des travaux (que `planning` ordonnancera).

### `planning` — l'ordonnancement du chantier

`Etape`s de travaux, dépendances entre elles, `Jalon`s contractuels, calcul des
dates et détection des retards. Le diagramme de Gantt n'est pas une donnée
stockée : il est **dérivé** des étapes et de leurs dépendances au moment du
rendu. Invariant central : une étape ne peut pas démarrer avant ses prérequis.

### `finance` — l'argent du chantier

`Facture`s reçues, `Acompte`s versés, rapprochement avec les montants acceptés,
suivi des indemnités d'assurance. Les montants sont manipulés en **centimes
entiers**, jamais en flottants — un arrondi binaire sur un dossier d'assurance
n'est pas une approximation acceptable. Invariant central : le cumul des acomptes
ne dépasse pas le montant engagé.

### `document` — les pièces du dossier

Devis signés, factures scannées, photos de chantier, rapports d'expertise,
courriers d'assurance. Le domaine gère les métadonnées, le classement et le
rattachement de chaque pièce à ce qu'elle justifie. **Le contenu binaire ne passe
jamais par le domaine** : il est confié à un port de stockage. Le rattachement à
un devis, une facture ou une étape est un couple (type de cible, identifiant),
conformément à R2.

### `identity` — identité et accès (transverse)

Comptes, mots de passe hachés en **argon2id**, rôles, et scopes qui bornent ce
qu'un porteur de jeton peut faire — qu'il s'agisse d'un humain sur l'interface
web ou d'un agent IA passant par MCP.

Ce domaine est transverse mais **ne viole pas R1** : les autres domaines ne
l'importent pas. Ils reçoivent l'identité de l'appelant en paramètre (un
identifiant d'acteur et ses scopes), plutôt que d'aller l'interroger. La
mécanique OAuth 2.1 elle-même n'est pas dans le domaine : c'est un adapter qui la
branche dessus.

#### Le modèle `Actor` et les scopes

Deux types séparent ce qui est stocké de ce qui autorise, et cette séparation est
la clé de R1 :

- **`User`** est le compte tel qu'il vit en base : email, nom d'affichage,
  empreinte de mot de passe, rôle, activité, horodatages. Il ne sort pas
  d'`identity` et de son adapter de persistance ;
- **`Actor`** est ce que **tout le reste de l'application reçoit** pour autoriser
  une action : un identifiant de compte et un jeu de scopes, rien de plus. Ses
  champs sont privés et il se construit à partir d'un rôle, de sorte que personne
  ne puisse s'ajouter un scope en chemin. Son unique question utile est
  `Allows(scope)`.

Les domaines métier recevront donc `identity.Actor` **en paramètre** de leurs cas
d'usage. Ils n'importent pas `identity` pour autant : c'est l'adapter appelant —
web aujourd'hui, MCP demain — qui obtient l'acteur et le transmet. Un service de
devis reste ainsi testable sans base de comptes, et R1 tient sans exception.

Les scopes sont des constantes typées de la forme `<domaine>:read` et
`<domaine>:write` pour les quatre domaines métier, plus un scope `mcp` distinct :
un droit sur les devis ne dit rien du canal par lequel on l'exerce.

Les rôles n'en sont pas la somme libre. Une **table unique** dans le domaine dit
quels scopes chaque rôle porte, et c'est la seule source de vérité — aucun code
ne doit déduire un droit d'un test « si le rôle est propriétaire » :

| Rôle | Scopes | Accès agent IA |
|---|---|---|
| `proprietaire` | les huit scopes de domaine, plus `mcp` | oui |
| `collaborateur` | `devis:read`, `devis:write`, `planning:read`, `planning:write` | non |

Pas de permission réglable compte par compte : à deux propriétaires et un
intervenant extérieur, des scopes à l'unité coûteraient une interface
d'administration, des combinaisons à tester, et l'occasion de se tromper — pour un
besoin qui n'existe pas. Un troisième profil s'ajouterait en une constante et une
ligne de table.

Deux conséquences valent d'être notées, parce qu'elles ne se devinent pas :

- **un compte désactivé donne un acteur anonyme**, sans aucun scope. La
  désactivation vaut retrait des droits même si un chemin d'appel oubliait de
  vérifier l'activité du compte ;
- **l'interface web relit le compte à chaque requête** plutôt que de mettre le
  rôle en session. Une désactivation ou un changement de rôle prend donc effet
  immédiatement, sans attendre l'expiration des sessions ouvertes. Le prix est une
  lecture par requête sur une table de trois lignes.

#### Le garde-fou contre la force brute

Le formulaire de connexion compte les échecs en mémoire et bloque temporairement
au-delà d'un seuil. La clé de comptage est le couple **(compte visé, adresse IP de
la connexion TCP)** : `X-Forwarded-For` est ignoré, parce qu'un en-tête qu'un
client peut écrire librement offre au premier attaquant venu un compteur neuf à
chaque requête.

La conséquence est à connaître avant de déployer : **derrière un reverse proxy,
tout le trafic arrive avec l'adresse du proxy**, la moitié « IP » de la clé
devient constante et la limite dégénère en simple limite par compte — ce qui reste
utile contre le bourrinage d'un mot de passe, mais ne distingue plus les
attaquants des utilisateurs légitimes. Si un proxy de confiance devient le mode de
déploiement standard, il faudra le déclarer explicitement en configuration (liste
d'adresses de proxys de confiance) et n'accepter `X-Forwarded-For` que de leur
part, plutôt que de faire confiance à l'en-tête par défaut.

## 5. La stack technique

Chaque choix est arrêté. Les versions exactes vivent dans `go.mod` et le
`Makefile`, pas ici — un document qui répète des numéros de version devient faux
au premier `go get`.

| Besoin | Choix | Motif |
|---|---|---|
| Base de données | PostgreSQL | Contraintes réelles, transactions, JSON quand il faut |
| Accès base | `jackc/pgx` v5, en mode natif | Le pilote Postgres de référence en Go ; `database/sql` ferait perdre les types Postgres pour rien |
| Migrations | `pressly/goose`, embarquées dans le binaire | Un binaire qui migre sa propre base : rien à installer côté hôte |
| Interface web | `html/template` rendu serveur + HTMX **vendoré** | Pas de build front, pas de CDN, pas de JavaScript à auditer |
| Sessions | `alexedwards/scs` | Sessions serveur, adossées à Postgres |
| Mots de passe | `alexedwards/argon2id` | argon2id est la recommandation courante pour le hachage de mots de passe |
| Accès agent IA | SDK MCP Go officiel (`modelcontextprotocol/go-sdk`) | Le SDK de référence du protocole |
| Autorisation MCP | OAuth 2.1 embarqué via `ory/fosite` | MCP distant exige OAuth ; fosite évite d'écrire soi-même un serveur d'autorisation |
| Traductions | `nicksnyder/go-i18n` | Français seul fourni, mais l'externalisation est faite dès le départ |
| Licence | **AGPL-3.0** | Une instance hébergée pour des tiers doit rendre ses modifications |

Le fil conducteur : **rien à installer d'autre que le binaire et Postgres**.
HTMX est vendoré, les migrations sont embarquées, il n'y a pas de chaîne de build
front. C'est ce qui rend l'auto-hébergement tenable pour quelqu'un qui n'est pas
administrateur système.

### Sur la licence AGPL-3.0

Le choix est délibéré. Avanti manipule des données personnelles sensibles
(finances, assurance, documents d'un sinistre). L'AGPL garantit qu'une instance
proposée en service à des tiers doit publier ses modifications : l'utilisateur
d'un Avanti hébergé par un tiers garde la capacité d'auditer ce qui tourne
réellement.

## 6. Le harnais de qualité

Tout est câblé dans le `Makefile`, avec des outils **épinglés à une version
exacte et installés dans `./bin`**, de sorte que le contributeur et la CI
exécutent les mêmes binaires. `make ci` enchaîne l'ensemble.

| Cible | Rôle |
|---|---|
| `make lint` | `golangci-lint` en configuration stricte, **frontières hexagonales incluses** via depguard |
| `make test` | `go test -race -cover ./...` |
| `make sec` | `gosec` (code) et `govulncheck` (dépendances et bibliothèque standard) |
| `make secrets` | `gitleaks` sur l'arbre de travail **et** sur l'historique git |
| `make mutation` | Tests de mutation sur les domaines — best-effort, hors CI |
| `make ci` | `lint` + `test` + `sec` + `secrets` |

Un hook `pre-commit` (`.githooks/`, activé par `make hooks`) rejoue secrets, lint
et tests rapides avant chaque commit — assez pour attraper l'essentiel sans
rendre le commit pénible.

### Deux écarts assumés, et pourquoi

**`misspell` et la prose française.** `misspell` corrige de l'anglais ; la
documentation d'Avanti est en français. Des mots français parfaitement corrects
(« analyse », « connexions », « persistance », « programme ») sont pris pour des
fautes de frappe anglaises. Plutôt que de désactiver le linter — il attrape de
vraies fautes dans les identifiants et les messages anglais — les mots concernés
sont listés dans `ignore-rules` de `.golangci.yml`. La liste grandira avec la
prose. Y ajouter un mot français est légitime ; y ajouter un identifiant de code
ne l'est pas.

**Les tests de mutation sont best-effort.** L'état de l'art Go a été évalué au
moment d'écrire ce document :

- `zimmski/go-mutesting` : dernier commit en juin 2021, dernière version `v1.2`
  la même année. Le projet est à l'arrêt.
- `go-gremlins/gremlins` : version `v0.6.0` publiée en décembre 2025, dépôt
  encore actif début 2026. Avance lentement, mais il avance.

**`gremlins` est retenu** : c'est le seul des deux qui soit encore vivant, et il
a été vérifié fonctionnel sur Go 1.26 — sur un package témoin, il génère bien ses
mutants et les classe correctement.

Deux réserves à connaître, toutes deux observées puis contournées dans le
`Makefile` :

- gremlins déduit le délai d'exécution d'un mutant de la durée de la suite de
  référence. Sur une suite rapide, le délai calculé est si court que **tous** les
  mutants sortent en `TIMED OUT` au lieu d'être jugés — corrigé par
  `--timeout-coefficient 10` ;
- gremlins v0.6.0 **n'étend pas le motif récursif `...` de Go**.
  `gremlins unleash ./internal/...` affiche « No results to report » et n'analyse
  rien, sans le moindre message d'erreur. C'est la panne la plus désagréable pour
  une cible dont on lit la sortie et non le code de retour : elle ressemble à « il
  n'y a rien à signaler ». La cible énumère donc les domaines un par un, ce qui
  coûte une ligne par nouveau domaine et se voit en revue.

À titre de repère, sur `identity` — le premier domaine écrit — `make mutation`
rend 47 mutants tués sur 47 jugés, soit 100 % d'efficacité pour 85 % de couverture
de mutateurs. Les 8 mutants classés « not covered » portent sur des expressions de
`switch` et sur une constante calculée à la compilation, que gremlins attribue à
des lignes qu'il croit non couvertes : ce sont des artefacts de l'outil, pas des
trous de test.

`make mutation` reste **hors de `make ci`** : trop lent pour chaque push, et sa
sortie est un indicateur de la qualité des tests, pas un quality gate qui doit
bloquer une fusion.

## 7. Ce que ce document n'engage pas encore

Le socle applicatif et le domaine `identity` existent — `internal/platform`,
`internal/identity`, `adapters/postgres`, `adapters/web` et `cmd/avanti` sont
écrits et testés — mais **les quatre packages de domaine métier ne contiennent
toujours que leur `doc.go`**. Les règles ci-dessus ont donc été écrites avant le
code qu'elles gouvernent, et c'est délibéré : le harnais qui les applique était
vert avant la première ligne de socle, ce qui fait qu'aucun code n'a pu les
enfreindre par accident en chemin.

Quatre règles de fonctionnement se sont ajoutées avec le socle et l'identité, et
valent pour la suite :

- **une migration publiée ne se modifie plus.** Les fichiers SQL sont embarqués
  dans le binaire et rejoués à chaque démarrage ; réécrire une migration déjà
  appliquée quelque part rendrait deux instances divergentes sans que rien ne le
  signale. On en ajoute une autre.
- **toute chaîne affichée passe par le catalogue de traductions**, dès maintenant
  et même si le français est la seule langue fournie. Un test relie les
  identifiants employés par les gabarits au catalogue : en oublier un fait
  échouer la suite. Seules les sondes `/healthz` et `/readyz` y échappent, parce
  que leur lecteur est un orchestrateur et non un humain.
- **une route web est protégée par défaut.** L'intergiciel d'authentification
  énumère les exceptions publiques — `/connexion` et `/static/` — et exige une
  session partout ailleurs. C'est le sens de l'erreur qui décide de cette forme :
  oublier d'inscrire une nouvelle route dans les exceptions la rend protégée, donc
  visible tout de suite ; oublier de poser un décorateur « protégé » sur une
  nouvelle route l'ouvrirait à tout le monde, en silence.
- **la protection CSRF est celle de la bibliothèque standard.**
  `net/http.CrossOriginProtection`, apparu en Go 1.25, refuse toute requête non
  sûre dont l'en-tête `Sec-Fetch-Site` annonce une origine tierce ou dont
  l'`Origin` ne correspond pas. Elle remplace un jeton synchronisé maison — sa
  réserve en session, sa rotation, ses cas particuliers — sans remplacer
  `SameSite=Lax` sur le cookie de session, qui reste posé : les deux couvrent la
  même attaque par des chemins différents, et un client qui échapperait à l'une
  devrait encore passer l'autre. L'URL publique de l'instance est déclarée origine
  de confiance, sans quoi un reverse proxy qui réécrit `Host` ferait refuser des
  requêtes légitimes.

Restent à trancher au fil de l'implémentation : le format d'échange des vues
transverses entre domaines et adapter web, la stratégie de pagination, et la
politique de rétention des documents. Ces décisions seront ajoutées ici au
moment où elles seront prises.
