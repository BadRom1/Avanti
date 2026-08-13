// Package postgres implémente les ports de persistance des domaines au-dessus de
// PostgreSQL, via pgx v5 en mode natif (pas database/sql). Il porte aussi les
// migrations goose embarquées dans le binaire.
//
// Frontières : c'est un adapter, il a donc le droit d'importer les domaines
// (pour implémenter leurs interfaces) et internal/platform (pour le pool de
// connexions et le logger). Il n'est jamais importé par un domaine : seul cmd/
// choisit de le brancher. Il n'importe pas non plus une autre famille
// d'adapters : leur seul point de rencontre est cmd/avanti.
package postgres
