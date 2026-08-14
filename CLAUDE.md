# CLAUDE.md — conventions de travail du dépôt Avanti

Ce fichier s'adresse aux agents (Claude Code ou autre) qui travaillent sur ce
dépôt. Les règles d'architecture vivent dans `docs/ARCHITECTURE.md` (prescriptif,
vérifié par le harnais) ; l'état d'avancement et les prochains lots dans
`docs/FEUILLE-DE-ROUTE.md`. Lis les deux avant toute modification.

## Rituel de travail (validé avec Romain)

Chaque lot de travail suit ce cycle, sans exception :

1. **Implémentation** par un sous-agent avec un brief complet (le sous-agent ne
   voit pas la conversation : objectif, fichiers, conventions, critères
   d'acceptation).
2. **Vérification par le lead** : relire le vrai diff (`git diff`), exécuter
   `make ci` soi-même, jouer le parcours en réel (l'app démarrée, curl) — ne
   jamais se fier au seul rapport du sous-agent.
3. **Critique** par un second sous-agent (revue adversariale : chercher ce que
   le harnais et les tests de l'implémenteur ne voient pas).
4. **Corrections**, puis **commit** (message en français, trailer
   `Co-Authored-By` de l'agent).

## Règles impératives

- **`make ci` vert avant tout commit.** Un test rouge se corrige, ne se
  contourne pas (jamais de `--no-verify`, jamais d'exclusion de test).
- **Jamais de version de dépendance écrite de mémoire** : `go get` sans version
  puis vérification via `go list -m -versions`, ou releases GitHub. Ceci vaut
  pour les outils du Makefile et les actions CI.
- **Frontières hexagonales** : encodées en allow-lists depguard strictes dans
  `.golangci.yml` (procédure d'ajout documentée sur place). Un domaine
  n'importe RIEN du dépôt — pas même `internal/identity` : l'autorisation par
  scopes se fait dans les adapters, le domaine reçoit l'identifiant d'acteur en
  simple valeur.
- **Nommage** : identifiants techniques en ANGLAIS. Le français est réservé au
  vocabulaire métier exporté (Devis, DemandeDevis, Facture, Acompte, Etape,
  Jalon, Artisan) et à tout l'user-visible (routes, textes i18n, sorties CLI,
  valeurs de rôles/scopes). Commentaires et documentation en français.
- **Aucune chaîne UI en dur** : tout passe par le catalogue i18n
  (`internal/adapters/web/locales/fr.json`).
- **Montants en centimes entiers** (int64), jamais de flottant — parsing
  décimal exact des saisies en euros.
- **Aucun secret en dur**, même d'exemple (placeholders `change-me`). gitleaks
  tourne en pre-commit et en CI.
- Ne pas laisser de processus résiduel après une validation manuelle
  (`pkill -f 'avanti serve'` au besoin).
- Le mot français dans la prose que misspell prend pour une faute anglaise
  s'ajoute à `ignore-rules` de `.golangci.yml` (sections « Français courant »
  ou « Fragments », documentées sur place) — on ne désactive pas le linter.

## Environnement de développement

- `make tools` installe l'outillage épinglé dans `./bin` ; `make hooks` active
  le pre-commit ; `make dev-db-up` lance le PostgreSQL de dev (port 5439) ;
  `make run` démarre l'app (variables : voir `.env.example`).
- Tests d'intégration : testcontainers-go par défaut ; si Docker est absent ou
  peu fiable (ex. sandbox cloud), fournir `AVANTI_TEST_DATABASE_URL` — les
  tests l'utilisent en priorité. Dans un sandbox avec PostgreSQL local :
  `service postgresql start` puis créer la base et exporter la variable.
- `make ci` = lint + tests (-race) + gosec/govulncheck + gitleaks. `make
  mutation` (Gremlins) est best-effort, hors CI.
- `avanti seed demo --email <compte existant>` remplit une base de dev VIDE
  d'un jeu de démonstration inter-domaines (refusé en production et dès que la
  base a vécu) — pratique pour valider un parcours en réel.

## Comptes et données de dev

Les comptes se créent par `avanti user add` (pas de page d'inscription, choix
délibéré), en dev comme en production. Une base de dev locale peut donc
contenir des comptes d'essai créés au fil des sessions (par exemple
`test-lead@avanti.local`) : ce sont des artefacts locaux, rien dans le dépôt —
code, migrations, seed — ne les crée ni ne les référence.
