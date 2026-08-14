# Avanti

Avanti — pilotage de la reconstruction d'une maison : devis, planning, finances et suivi assurance en un seul endroit, avec un accès agent IA (MCP) pour tout gérer depuis Claude. Repartir de l'avant, un parpaing à la fois.

## Statut

**V1 fonctionnelle.** Tout ce qu'il faut pour piloter un chantier est en place
et vérifié par la CI :

- **les quatre domaines métier** — demandes de devis et comparaison des offres
  (avec un seul devis retenu par consultation, garanti en base), planning en
  étapes et jalons avec dépendances et Gantt dérivé, factures et acomptes en
  centimes entiers avec suivi assurance et exports CSV/PDF, pièces du dossier
  classées et rattachées à ce qu'elles justifient ;
- **l'identité et les accès** : comptes en ligne de commande, rôles
  `proprietaire` et `collaborateur`, scopes vérifiés route par route ;
- **l'accès agent IA** : serveur MCP embarqué, protégé par le serveur
  d'autorisation OAuth 2.1 embarqué lui aussi — un agent Claude se branche avec
  la seule URL de l'instance et le consentement d'un propriétaire ;
- **le packaging self-hosted** : Dockerfile, compose de production exemple, et
  [docs/INSTALLATION.md](docs/INSTALLATION.md) pour le pas à pas complet.

Une règle traverse le tout : **aucun envoi automatique**. L'application prépare
le dossier d'assurance, l'agent IA aussi ; la transmission reste un geste
humain. Les ajustements se feront à l'usage — l'état exact et les décisions
encore ouvertes sont dans [docs/FEUILLE-DE-ROUTE.md](docs/FEUILLE-DE-ROUTE.md).

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

- **Go 1.26.6** ou plus récent (voir la directive `go` de `go.mod`)
- **make**, **git**, **curl**
- **Docker** — pour lancer Postgres en développement
- Aucun outil de qualité à installer à la main : `make tools` s'en charge et les
  place dans `./bin`, à des versions épinglées.

## Démarrage rapide

Le pas à pas ci-dessous est celui d'un poste de **développement**. Pour
déployer une instance réelle — Docker, reverse proxy, comptes, sauvegardes,
agent Claude — suivre [docs/INSTALLATION.md](docs/INSTALLATION.md).

```sh
git clone https://github.com/Romain-Badino/Avanti.git
cd Avanti
make tools          # installe l'outillage épinglé dans ./bin
make hooks          # active les hooks git du dépôt
cp .env.example .env
make dev-db-up      # démarre le PostgreSQL de développement
make build          # compile ./bin/avanti
./bin/avanti user add --email vous@exemple.fr --nom "Votre Nom" --role proprietaire
make run            # lance l'application
```

L'interface répond alors sur <http://localhost:8080> et demande la connexion.
Deux sondes y échappent :

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

## Comptes et connexion

Avanti n'a **pas de page d'inscription**, et n'en aura pas : c'est une instance
privée, sans personne à inscrire. Les comptes se créent en ligne de commande, sur
la machine qui héberge l'instance.

```sh
# Mot de passe demandé au terminal, sans écho, puis redemandé pour confirmation.
avanti user add --email vous@exemple.fr --nom "Votre Nom" --role proprietaire

# Ou engendré par Avanti et affiché une seule fois — pratique pour un script
# d'installation, ou pour un compte dont le mot de passe va dans un gestionnaire.
avanti user add --email architecte@exemple.fr --nom "Amélie Dupré" \
                --role collaborateur --generate

avanti user list                                    # qui existe, avec quel rôle
avanti user disable --email architecte@exemple.fr   # ferme l'accès
avanti user enable  --email architecte@exemple.fr   # le rouvre

# Mot de passe perdu : réinitialisation par l'hôte, sans l'ancien mot de passe.
avanti user set-password --email vous@exemple.fr --generate

# Changement de rôle, à effet immédiat (le rôle est relu à chaque requête).
avanti user set-role --email architecte@exemple.fr --role proprietaire
```

La CLI sur la machine hôte est la **racine de confiance** de l'instance : pas
de page d'inscription, pas de réinitialisation en ligne — qui peut exécuter
`avanti user` détient déjà la base, et c'est là, et là seulement, que les
comptes s'administrent.

Les sous-commandes `user` lisent la même configuration que `avanti serve` et
appliquent les migrations manquantes si `AVANTI_MIGRATE_ON_START` le permet : le
premier compte se crée donc sur une base neuve, avant que le serveur n'ait jamais
tourné.

### Les deux rôles

| Rôle | Ce qu'il ouvre | Accès agent IA (MCP) |
|---|---|---|
| `proprietaire` | tout : devis, planning, finances, documents | oui |
| `collaborateur` | devis et planning, en lecture et écriture | non |

`collaborateur` est le profil de l'intervenant extérieur — l'architecte : il
travaille sur les devis et le planning par l'interface web, et ne voit ni les
finances ni les pièces du dossier. Ces sections ne lui sont pas grisées, elles ne
lui sont pas affichées.

Le seul critère imposé au mot de passe est sa **longueur : douze caractères au
minimum**, sans aucune règle de composition. Les mots de passe sont hachés en
argon2id ; Avanti ne peut donc pas retrouver un mot de passe perdu — `avanti
user set-password` en pose un nouveau, avec les mêmes règles qu'à la création.

Un compte n'est jamais supprimé. `user disable` ferme l'accès et **coupe les
sessions web déjà ouvertes** sur ce compte, sans attendre leur expiration ; les
actions que le compte a signées continuent de le désigner.

## Connecter un agent IA (MCP)

Le serveur MCP est servi sur `https://votre-instance/mcp`, et tout le reste se
découvre tout seul : l'agent lit le document de découverte, s'enregistre comme
client OAuth (enregistrement dynamique), et un propriétaire consent dans son
navigateur. Il n'y a **que l'URL à donner** :

```sh
claude mcp add --transport http avanti https://votre-instance/mcp
```

Sur claude.ai, la même URL s'ajoute en connecteur personnalisé. Le pas à pas
est dans [docs/INSTALLATION.md](docs/INSTALLATION.md), section « Brancher
Claude ».

Le modèle tient en une phrase : **chaque agent passe par OAuth avec le compte de
son utilisateur**, jamais avec un compte machine ni une clé partagée. Un agent ne
peut donc rien faire de plus que la personne qui l'a autorisé, et l'autorisation
se retire sans toucher au compte. L'accès agent demande le scope `mcp`, que seul
le rôle `proprietaire` porte : un `collaborateur` ne peut pas en ouvrir un. Les
outils exposés consultent les quatre domaines et écrivent devis, factures,
acomptes et étapes ; aucun n'envoie quoi que ce soit — la transmission à
l'assurance reste un geste humain.

Une seule chose est à préparer : **`AVANTI_OAUTH_SECRET`**, obligatoire au
démarrage. C'est la clé HMAC qui signe les codes d'autorisation et les jetons.
Engendrez la vôtre, une fois pour toutes :

```sh
openssl rand -base64 32
```

Trente-deux octets au minimum. La valeur d'exemple de `.env.example` convient en
développement et est refusée au démarrage si `AVANTI_ENV` vaut `production` :
publiée dans ce dépôt, la garder reviendrait à n'avoir aucune clé. En changer
déconnecte tous les agents autorisés, qui devront l'être à nouveau — c'est aussi
la façon de tout révoquer d'un coup.

Le flux, ses garde-fous et les décisions de sécurité sont détaillés dans
[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md), section « OAuth 2.1 et l'accès
agent ».

## Commandes du binaire

```
avanti serve      démarre le serveur HTTP (commande par défaut)
avanti user       gère les comptes (« avanti user » pour le détail)
avanti seed       remplit une instance vide de données d'essai (« avanti seed » pour le détail)
avanti version    affiche l'identité du binaire (équivalent : avanti --version)
```

`avanti seed demo --email vous@exemple.fr` crée un jeu de démonstration complet
— consultations, devis retenu, factures, acomptes, étapes, jalons, pièces PDF —
attribué à un compte existant. C'est un outil de découverte : il refuse de
tourner quand `AVANTI_ENV` vaut `production`, et dès que la base contient la
moindre donnée métier.

## Tests

`make test` couvre tout. Les tests d'intégration — migrations et `/readyz` pour le
socle, dépôt des comptes et contraintes de table pour l'adapter PostgreSQL — ont
besoin d'un vrai PostgreSQL et l'obtiennent de trois façons, dans cet ordre :

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
  identity/          Domaine transverse : comptes, rôles, scopes, Actor
  adapters/
    postgres/        Persistance
    web/             Interface humaine : routes, gabarits, CSS, HTMX vendoré,
                     catalogue de traductions — tout embarqué dans le binaire ;
                     le serveur d'autorisation OAuth 2.1 y est monté aussi
    mcp/             Interface agent (MCP), consommatrice des jetons OAuth
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
compose.production.yaml  Exemple commenté de déploiement (app + Postgres + volumes)
Dockerfile           Image de production (build statique, distroless, non-root)
.env.example         Configuration commentée, à copier en .env
docs/ARCHITECTURE.md Règles de frontières et choix de stack
docs/INSTALLATION.md Installation self-hosted pas à pas, agent Claude compris
docs/FEUILLE-DE-ROUTE.md  Ce qui est fait, ce qui reste
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
