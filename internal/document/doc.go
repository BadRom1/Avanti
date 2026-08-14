// Package document porte le domaine des pièces du dossier : devis signés,
// factures scannées, photos de chantier, rapports d'expertise et courriers
// d'assurance. Il gère les métadonnées, le classement et le rattachement de
// chaque pièce à ce qu'elle justifie.
//
// Le contenu des fichiers n'est jamais porté par le domaine : il est confié au
// port [Storage], dont les implémentations vivent dans
// internal/adapters/storage — c'est le point d'extension officiel du projet
// (docs/ARCHITECTURE.md §3). Les métadonnées, elles, passent par le port
// [Repository].
//
// Frontières : ce package n'importe aucun autre domaine, aucun adapter, ni
// internal/platform. Le rattachement d'une pièce à un Devis, une Facture ou une
// Etape est un couple (type de cible, identifiant), pas un import du domaine
// concerné.
package document
