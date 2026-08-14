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

## À faire

### Lot 9 — Serveur MCP

- Adapter `internal/adapters/mcp` avec le SDK Go officiel
  (github.com/modelcontextprotocol/go-sdk — vérifier la version et la spec
  d'autorisation MCP courante sur modelcontextprotocol.io).
- Transport HTTP (streamable) monté sur le serveur existant, authentifié par
  les jetons OAuth du lot 4b via le port `identity.TokenVerifier` ; publier le
  Protected Resource Metadata (RFC 9728) pointant vers l'AS embarqué ;
  resserrer `checkResource` (RFC 8707) sur l'URL canonique du serveur MCP
  (limitation documentée dans oauth_authorize.go).
- Tools par domaine, bornés par les scopes du jeton : consultation
  (finances, planning, devis, documents), écriture (enregistrer un devis reçu,
  une facture/dépense, avancer une étape), préparation d'un envoi assurance.
- **Toute action d'envoi (mail, transmission) = préparation + confirmation
  explicite** ; l'envoi de mail effectif n'est PAS dans ce lot (pas de port
  MailSender branché en V1 sans décision de Romain).
- Tests : flow complet jeton OAuth → tool MCP autorisé/refusé par scope.

### Lot 10 — Packaging et revue finale

- Dockerfile multi-stage (binaire statique + assets embarqués), compose de
  production exemple (app + Postgres + volume documents), docs d'installation
  self-hosted pas à pas (créer les comptes, générer AVANTI_OAUTH_SECRET,
  brancher Claude via MCP).
- Seed de démonstration optionnel (`avanti seed --demo` ou équivalent).
- Nettoyage : compte de test de la base dev, TODO restants, `make mutation`
  sur les domaines et renforcement des tests les plus faibles.
- Revue de code globale finale (sous-agent critique sur l'ensemble) et passe
  de cohérence documentaire (README, ARCHITECTURE, ce fichier).

## Hors scope V1 (décisions de cadrage)

- Accès agent IA pour l'architecte (le modèle rôle+scope le permet déjà).
- Notifications/rappels automatiques avancés.
- Envoi de mails automatique (toute transmission reste confirmée par un humain).
- Hébergement définitif : non tranché (Railway probable, cohérent avec
  l'existant de Romain).
