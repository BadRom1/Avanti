// Package platform regroupe la plomberie technique transverse d'Avanti :
// chargement de la configuration, journalisation structurée, pool de connexions
// à la base, cycle de vie du serveur HTTP et informations de build.
//
// Frontières : platform est le socle le plus bas de l'application. Il n'importe
// ni les domaines (internal/devis, internal/planning, internal/finance,
// internal/document, internal/identity) ni les adapters (internal/adapters/...).
// La dépendance va toujours dans l'autre sens : les adapters et cmd/ consomment
// platform. Cette règle est vérifiée automatiquement par depguard (voir
// .golangci.yml) — si un import interdit apparaît, le lint échoue.
package platform
