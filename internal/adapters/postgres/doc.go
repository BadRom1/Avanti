// Package postgres implémente les ports de persistance des domaines au-dessus de
// PostgreSQL, via pgx v5 en mode natif (pas database/sql).
//
// Le schéma, lui, n'est pas ici : les migrations goose vivent dans
// internal/platform/migrate, qui les embarque et les rejoue au démarrage. La
// séparation est celle de R3 — appliquer un schéma est du socle, écrire les
// requêtes d'un domaine est de l'adapter.
//
// Frontières : c'est un adapter, il a donc le droit d'importer les domaines
// (pour implémenter leurs interfaces) et internal/platform (pour le pool de
// connexions et le logger). Il n'est jamais importé par un domaine : seul cmd/
// choisit de le brancher. Il n'importe pas non plus une autre famille
// d'adapters : leur seul point de rencontre est cmd/avanti.
package postgres
