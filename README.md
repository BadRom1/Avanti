# Avanti

Avanti — pilotage de la reconstruction d'une maison : devis, planning, finances et suivi assurance en un seul endroit, avec un accès agent IA (MCP) pour tout gérer depuis Claude. Repartir de l'avant, un parpaing à la fois.

## Statut

**En construction.** Le socle applicatif tourne : l'application démarre, lit sa
configuration, applique ses migrations, sert une interface web et répond à ses
sondes d'exploitation. Les domaines métier — devis, planning, finances,
documents, identité — ne sont pas encore implémentés : la page d'accueil est un
provisoire assumé. Rien n'est donc utilisable pour piloter un vrai chantier, mais
la charpente est en place et vérifiée par la CI.

## Stack

- **Go**, un seul binaire, auto-hébergeable
- **PostgreSQL** via `pgx` v5, migrations `goose` embarquées dans le binaire
- **Interface web** rendue serveur (`html/template`) avec HTMX vendoré — pas de
  build front, pas de CDN
- **Accès agent IA** par un serveur MCP (SDK Go officiel), protégé par OAuth 2.1
  embarqué (`ory/fosite`)
- Sessions `scs`, mots de passe en `argon2id`, traductions `go-i18n` (français
  fourni)

Le fil conducteur : rien à installer d'autre que le binaire et Postgres. Les
choix et leurs motifs sont détaillés dans [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).

## Prérequis

- **Go 1.26.5** ou plus récent (voir la directive `go` de `go.mod`)
- **make**, **git**, **curl**
- **Docker** — pour lancer Postgres en développement
- Aucun outil de qualité à installer à la main : `make tools` s'en charge et les
  place dans `./bin`, à des versions épinglées.

## Démarrage rapide

```sh
git clone https://github.com/Romain-Badino/Avanti.git
cd Avanti
make tools          # installe l'outillage épinglé dans ./bin
make hooks          # active les hooks git du dépôt
cp .env.example .env
make dev-db-up      # démarre le PostgreSQL de développement
make run            # lance l'application
```

L'interface répond alors sur <http://localhost:8080>. Deux sondes complètent la
page d'accueil :

```sh
curl -i http://localhost:8080/healthz   # le processus répond (sans toucher à la base)
curl -i http://localhost:8080/readyz    # la base répond aussi
```

`make hooks` exécute `git config core.hooksPath .githooks`. **Ce réglage est
local à votre clone** : git ne l'installe pas tout seul, chaque contributeur doit
lancer la commande une fois. Le hook `pre-commit` rejoue alors la recherche de
secrets, l'analyse statique et les tests rapides avant chaque commit.

## Configuration

Tout passe par des variables d'environnement préfixées `AVANTI_`, décrites une par
une dans [.env.example](.env.example). Seule `AVANTI_DATABASE_URL` n'a pas de
valeur par défaut. Une valeur invalide fait échouer le démarrage en nommant la
variable fautive — toutes à la fois s'il y en a plusieurs, pour éviter la série
de redémarrages.

`make run` charge `.env` s'il existe ; en production, ces variables se passent par
l'environnement du service (unité systemd, conteneur…) et `.env` n'a pas lieu
d'être. Il n'entre jamais dans l'historique git.

## Base de développement

[compose.yaml](compose.yaml) ne lève que PostgreSQL, sur le **port hôte 5439** et
la boucle locale — le port standard 5432 est souvent déjà pris sur un poste de
développement, et une collision se manifesterait par une application connectée à
la mauvaise base. Ce n'est pas un modèle de déploiement : en production, Avanti
est un binaire face à un PostgreSQL administré à part.

## Cibles make

| Cible | Effet |
|---|---|
| `make help` | Liste les cibles disponibles |
| `make build` | Compile le binaire dans `./bin/avanti` |
| `make run` | Lance l'application (charge `.env` s'il existe) |
| `make dev-db-up` | Démarre le PostgreSQL de développement et attend qu'il réponde |
| `make dev-db-down` | L'arrête, en conservant les données |
| `make dev-db-reset` | L'arrête et jette le volume de données |
| `make dev-db-psql` | Ouvre un `psql` sur la base de développement |
| `make test` | Tests avec détecteur de course et couverture |
| `make lint` | Analyse statique, **frontières hexagonales comprises** |
| `make fmt` | Reformate le code (gofumpt + goimports) |
| `make sec` | `gosec` sur le code, `govulncheck` sur les dépendances |
| `make secrets` | `gitleaks` sur l'arbre de travail et l'historique |
| `make mutation` | Tests de mutation sur les domaines (lent, best-effort) |
| `make ci` | Enchaîne lint, test, sec et secrets — ce que vérifie la CI |
| `make tools` | Installe l'outillage épinglé dans `./bin` |
| `make hooks` | Active les hooks git du dépôt |
| `make clean` | Supprime `./bin` et les artefacts de couverture |

## Commandes du binaire

```
avanti serve      démarre le serveur HTTP (commande par défaut)
avanti version    affiche l'identité du binaire (équivalent : avanti --version)
```

## Tests

`make test` couvre tout. Les tests d'intégration du socle (migrations, `/readyz`)
ont besoin d'un vrai PostgreSQL et l'obtiennent de trois façons, dans cet ordre :

1. `AVANTI_TEST_DATABASE_URL`, si la variable est renseignée — c'est le chemin de
   la CI, où un service PostgreSQL tourne déjà ;
2. un conteneur jetable levé par [testcontainers](https://golang.testcontainers.org/),
   sinon — le chemin par défaut sur un poste de développement, rien à préparer ;
3. faute des deux, ils se **sautent** proprement : `make test` reste vert sans
   Docker.

Chaque test d'intégration se taille sa propre base, pour que « les migrations
s'appliquent sur une base vierge » soit vérifié sur une base réellement vierge.

## Structure du dépôt

```
cmd/avanti/          Point d'entrée — le seul endroit qui assemble tout
internal/
  devis/             Domaine : consultation des artisans, offres, acceptation
  planning/          Domaine : étapes, jalons, dépendances, retards
  finance/           Domaine : factures, acomptes, suivi assurance
  document/          Domaine : pièces du dossier et leur classement
  identity/          Domaine transverse : comptes, rôles, scopes
  adapters/
    postgres/        Persistance
    web/             Interface humaine : routes, gabarits, CSS, HTMX vendoré,
                     catalogue de traductions — tout embarqué dans le binaire
    mcp/             Interface agent (MCP + OAuth 2.1)
    storage/         Stockage du contenu des documents
    mail/            Notifications sortantes
    export/          PDF assurance, CSV comptable, archive
  platform/          Socle technique, un paquet par responsabilité :
    config/          Lecture et validation de l'environnement AVANTI_
    logging/         Journal structuré (slog)
    db/              Pool PostgreSQL (pgx) et sonde de disponibilité
    migrate/         Migrations SQL embarquées (goose)
    server/          Serveur HTTP, intergiciels, sondes, arrêt gracieux
compose.yaml         PostgreSQL de développement (port hôte 5439)
.env.example         Configuration commentée, à copier en .env
docs/ARCHITECTURE.md Règles de frontières et choix de stack
.githooks/           Hook pre-commit (activé par make hooks)
```

Les frontières entre ces répertoires ne sont pas une convention de nommage : un
domaine n'importe ni un autre domaine, ni un adapter, ni `platform`, et
`depguard` fait échouer le lint si la règle est enfreinte. Le détail est dans
[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).

## Contribuer

Avant d'ouvrir une pull request : `make ci` doit être vert. La CI rejoue
exactement les mêmes vérifications, avec les mêmes versions d'outils.

Si un linter signale du code, la règle est de corriger le code plutôt que la
configuration. Les rares exceptions sont documentées, avec leur motif, dans
`.golangci.yml` et dans `docs/ARCHITECTURE.md`.

## Licence

[GNU Affero General Public License v3.0](LICENSE).

Le choix est délibéré : Avanti manipule des données personnelles sensibles
(finances, assurance, documents d'un sinistre). L'AGPL garantit qu'une instance
proposée en service à des tiers doit publier ses modifications, et donc que ses
utilisateurs gardent la capacité d'auditer ce qui tourne réellement.
