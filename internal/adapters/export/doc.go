// Package export produit les livrables destinés à sortir d'Avanti : dossier PDF
// pour l'assurance ou l'expert, tableaux CSV pour le comptable, archive complète
// pour la réversibilité des données.
//
// Frontières : c'est un adapter, il lit les domaines via leurs services et peut
// utiliser internal/platform. Aucun domaine ne l'importe, et il n'importe
// aucune autre famille d'adapters : leur seul point de rencontre est
// cmd/avanti.
package export
