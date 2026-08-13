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

## À faire

### Lot 6 — Domaine Document

- Domaine : métadonnées de pièces (devis signés, factures scannées, photos de
  chantier, rapports d'expertise, courriers assurance), classement, rattachement
  par référence faible (type de cible + id : devis, facture, étape) — R2.
- Le contenu binaire ne passe JAMAIS par le domaine : port de stockage
  (`DocumentStorage` ou équivalent), **point d'extension plugin officiel**.
- Adapter stockage filesystem par défaut (répertoire de `AVANTI_DOCUMENTS_DIR`,
  noms de fichiers non devinables, pas de traversée) ; **adapter S3-compatible
  en second, comme plugin de démonstration** (nouvelle famille d'adapters ou
  sous-package de storage ; choisi par la config, assemblé par cmd/).
- Permissions simples lecture/écriture via scopes document:read/write.
- UI : téléversement (limites de taille et de types MIME strictes), listing,
  rattachement depuis les pages devis (les devis portent déjà des documentIDs
  faibles), téléchargement authentifié (jamais de fichier servi sans contrôle
  de scope).
- Brancher le téléversement de pièces sur les pages Devis existantes.

### Lot 7 — Domaine Finance

- Entités : `Facture` (entreprise, montant en centimes, date, statut
  payée/impayée), `Acompte`/`Paiement` (montant, date, moyen), chacune avec un
  statut « envoyé à l'assurance » et un remboursement suivi.
- `devisID` optionnel en référence FAIBLE (string) — absent = dépense hors
  devis (achat direct, auto-construction).
- Invariant central : le cumul des acomptes ne dépasse pas le montant engagé.
- Vue de synthèse par devis retenu : engagé vs facturé vs payé vs remboursé
  assurance ; total chantier.
- Export assurance PDF/CSV via un port `ExportFormat` (second point d'extension
  plugin) — l'export liste les pièces (documents) associées.
- Scopes finance:read/write (les collaborateurs n'y ont pas accès).

### Lot 8 — Domaine Planning

- Entités : `Etape` (dates prévues/réelles, dépendances entre étapes, statut),
  `Jalon`. Détection de cycle dans les dépendances = erreur métier.
- Le Gantt est DÉRIVÉ des étapes et dépendances (pas une entité) ; deux étapes
  sans dépendance commune sont candidates à la parallélisation.
- Détection de retard = comparaison prévu vs réel/aujourd'hui.
- Référence faible optionnelle d'une étape vers un devis retenu (`devisID`).
- UI : liste des étapes + vue Gantt rendue serveur (SVG ou table CSS — pas de
  lib JS), jalons, retards mis en évidence.
- Scopes planning:read/write (accessibles au collaborateur/architecte).

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
