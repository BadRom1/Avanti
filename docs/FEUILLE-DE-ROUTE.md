# Feuille de route V1

Objectif : une V1 réellement utilisable pour piloter la reconstruction de la
maison — 4 domaines complets (devis, planning, finance, document), accès agent
IA (MCP) pour les deux propriétaires, UI web pour tous, self-hosted. Les
ajustements se feront à l'usage.

Le rituel de travail par lot est décrit dans `CLAUDE.md` ; l'architecture dans
`docs/ARCHITECTURE.md`.

## Fait

- **Lot 1-2 — Squelette et harnais** (`1904daf`) : layout hexagonal, depguard
  en allow-lists strictes, lint/gosec/govulncheck/gitleaks/mutation, CI, AGPL.
- **Lot 3 — Socle** (`00c2c31`) : config env, slog, pgx, migrations goose,
  serveur HTTP, i18n fr, HTMX vendoré, compose de dev (port 5439).
- **Lot 4a — Identité** (`23ba6fd`) : comptes argon2id, rôles
  proprietaire/collaborateur, scopes par domaine + scope mcp, sessions scs
  (anti-fixation, CSRF, anti-force-brute), CLI `avanti user`.
- **Lot 4b — OAuth 2.1** (`0bdfbfe`) : fosite, PKCE S256 obligatoire, DCR
  RFC 7591 avec garde-fous, metadata RFC 8414, rotation atomique des refresh
  tokens (transactional storage + test de concurrence), consentement avec
  identité vérifiable du client, port `identity.TokenVerifier`.
- **Lot 5 — Domaine Devis** : voir le commit correspondant (demandes de devis,
  comparaison, retenir/refuser avec invariant « un seul retenu par demande »,
  atomicité en base, UI de comparaison, gardes par scopes).
- **Lot 6 — Domaine Document** : métadonnées et classement des pièces,
  rattachement par référence faible (type de cible + id, borné et normalisé),
  port de stockage `document.Storage` (point d'extension plugin officiel) avec
  adapters filesystem par défaut (os.Root, clés UUID validées, écriture
  atomique par lien dur) et S3-compatible en démonstration (minio-go), choix
  par `AVANTI_STORAGE_BACKEND`, scopes document:read/write, UI de
  téléversement (MIME sniffé, allow-list stricte, 25 Mio max), listing,
  téléchargement authentifié, pièces rattachées sur les pages devis.

- **Lot 7 — Domaine Finance** : entités `Facture` et `Acompte` (centimes
  entiers, `devisID` en référence faible optionnelle, suivi assurance à
  transitions irréversibles, garde optimiste sur les mises à jour), invariant
  « cumul des acomptes ≤ montant engagé » tenu sous verrou consultatif en base
  (montant engagé passé en valeur par l'adapter — R1/R2), synthèse par devis
  retenu (engagé/facturé/payé/remboursé/reste) + hors devis + total chantier,
  export assurance CSV (BOM, cellules neutralisées) et PDF via le port
  `finance.ExportFormat` (second point d'extension plugin), justificatifs de
  facture rattachés et listés dans l'export, scopes finance:read/write.

- **Lot 8 — Domaine Planning** : entités `Etape` (statut et retards DÉRIVÉS
  des dates réelles, jamais stockés, transitions Start/Finish) et `Jalon`,
  dépendances en table de jointure intra-domaine avec `CheckAcyclic` pur
  (cycle = erreur métier) rejoué sous verrou consultatif avec l'existence des
  prérequis et leur terminaison au démarrage, garde optimiste sur toutes les
  mises à jour, Gantt dérivé pur (positions en millièmes entiers) rendu
  serveur en table colspan compatible CSP stricte, section « prêtes à
  démarrer » (candidates à la parallélisation), référence faible optionnelle
  vers un devis retenu, scopes planning:read/write.

- **Lot 9 — Serveur MCP** : adapter `internal/adapters/mcp` sur le SDK Go
  officiel (v1.7.0), transport HTTP streamable sans état monté par cmd/avanti
  à côté du web, authentification par jetons OAuth via le seul port
  `identity.TokenVerifier` (401/403 RFC 6750 avec WWW-Authenticate), Protected
  Resource Metadata RFC 9728 servi à ses deux URLs (racine et forme avec
  chemin), `checkResource` RFC 8707 resserré sur l'URL canonique `/mcp`
  (l'instance nue est refusée, ports par défaut normalisés, resource rejouée
  au point de terminaison des jetons), 14 tools français bornés par scopes
  (consultation des quatre domaines, écriture devis/facture/acompte/étapes,
  `assurance_preparer_envoi` avec avertissement explicite — aucun envoi,
  aucun port MailSender), protection localhost du SDK désactivée par décision
  argumentée (le Bearer défend seul, elle cassait le déploiement reverse
  proxy), flow complet OAuth → MCP testé contre PostgreSQL réel.

- **Lot 10 — Packaging** (revue globale finale restante, voir « À faire ») :
  Dockerfile multi-stage (binaire statique CGO_ENABLED=0, image distroless
  non-root avec certificats CA, assets et migrations embarqués),
  `compose.production.yaml` exemple commenté (app + PostgreSQL + volume
  documents, zéro secret en dur), `docs/INSTALLATION.md` pas à pas (comptes,
  secret OAuth, reverse proxy et ses réserves, branchement Claude via MCP,
  sauvegardes), `avanti seed demo` (refus en production et sur base non
  vierge, jeu cohérent inter-domaines via les services), nettoyage (aucun
  TODO, aucun compte de test dans le dépôt), `make mutation` avec
  renforcement des tests (document 100 %, devis 98,9 %, finance 98,7 %,
  planning 89,6 %, identity 100 %), README/ARCHITECTURE/CLAUDE.md remis en
  cohérence. Le build Docker n'a pas pu être exécuté dans l'environnement de
  développement (Docker absent) : à valider au premier `docker build`.

## À faire

### Revue globale finale (reste du Lot 10)

- Revue de code globale finale (sous-agent critique sur l'ensemble du dépôt)
  et corrections éventuelles — délibérément différée après le commit du
  packaging, sur décision de Romain.

## Hors scope V1 (décisions de cadrage)

- Accès agent IA pour l'architecte (le modèle rôle+scope le permet déjà).
- Notifications/rappels automatiques avancés.
- Envoi de mails automatique (toute transmission reste confirmée par un humain).
- Hébergement définitif : non tranché (Railway probable, cohérent avec
  l'existant de Romain).
