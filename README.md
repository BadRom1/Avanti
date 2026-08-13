# Avanti

Avanti — pilotage de la reconstruction d'une maison : devis, planning, finances et suivi assurance en un seul endroit, avec un accès agent IA (MCP) pour tout gérer depuis Claude. Repartir de l'avant, un parpaing à la fois.

## Statut

**En construction.** Le dépôt contient aujourd'hui le squelette et le harnais de
qualité : la structure hexagonale, les règles de frontières vérifiées
automatiquement, la chaîne de lint, de sécurité et de tests, et la CI. Les
domaines métier ne sont pas encore implémentés. Rien n'est utilisable en l'état.

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

## Démarrage

```sh
git clone https://github.com/Romain-Badino/Avanti.git
cd Avanti
make tools    # installe l'outillage épinglé dans ./bin
make hooks    # active les hooks git du dépôt
make ci       # vérifie que tout est vert
```

`make hooks` exécute `git config core.hooksPath .githooks`. **Ce réglage est
local à votre clone** : git ne l'installe pas tout seul, chaque contributeur doit
lancer la commande une fois. Le hook `pre-commit` rejoue alors la recherche de
secrets, l'analyse statique et les tests rapides avant chaque commit.

## Cibles make

| Cible | Effet |
|---|---|
| `make help` | Liste les cibles disponibles |
| `make build` | Compile le binaire dans `./bin/avanti` |
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
    postgres/        Persistance et migrations
    web/             Interface humaine (HTTP, templates, HTMX)
    mcp/             Interface agent (MCP + OAuth 2.1)
    storage/         Stockage du contenu des documents
    mail/            Notifications sortantes
    export/          PDF assurance, CSV comptable, archive
  platform/          Socle technique : config, logs, base, serveur
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
