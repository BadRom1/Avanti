// Package storage implémente le port de stockage de contenu binaire du domaine
// document. L'implémentation de référence écrit sur le système de fichiers local
// (cohérent avec un déploiement self-hosted) ; un objet compatible S3 est un
// second choix branché à la configuration.
//
// Frontières : c'est un adapter, il implémente une interface définie par le
// domaine document et peut utiliser internal/platform. Aucun domaine ne
// l'importe, et il n'importe aucune autre famille d'adapters : leur seul point
// de rencontre est cmd/avanti.
package storage
