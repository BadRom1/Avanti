// Package document porte le domaine des pièces du dossier : devis signés,
// factures scannées, photos de chantier, rapports d'expertise et courriers
// d'assurance. Il gère les métadonnées, le classement et le rattachement de
// chaque pièce à ce qu'elle justifie.
//
// Contenu attendu : les entités et invariants du domaine, les ports (dépôt de
// métadonnées, stockage du contenu binaire, calcul d'empreinte) et les services
// applicatifs. Le contenu des fichiers n'est jamais porté par le domaine : il
// est confié à un port de stockage dont l'implémentation vit dans
// internal/adapters/storage.
//
// Frontières : ce package n'importe aucun autre domaine, aucun adapter, ni
// internal/platform. Le rattachement d'une pièce à un Devis, une Facture ou une
// Etape est un couple (type de cible, identifiant), pas un import du domaine
// concerné.
package document
