// Package devis porte le domaine de la consultation des artisans : demandes de
// devis, propositions chiffrées reçues, comparaison des offres, et décision qui
// fait entrer un lot de travaux en exécution.
//
// Le vocabulaire métier français est conservé tel quel dans les identifiants
// exportés (Devis, DemandeDevis, Artisan, Statut, Comparaison) : c'est le
// langage ubiquitaire du projet, celui qu'emploient les documents papier
// échangés avec les entreprises et l'assurance. Le reste du code — verbes,
// helpers techniques, noms de champs — est en anglais.
//
// Deux invariants tiennent tout le domaine :
//
//   - un devis naît [StatutRecu] et ne peut aller que vers [StatutRetenu] ou
//     [StatutRefuse]. Une décision ne se reprend pas ;
//   - une demande porte au plus un devis retenu. Retenir refuse par ricochet les
//     devis concurrents encore reçus, et une demande décidée n'accepte plus de
//     nouveau devis. Les deux moitiés de cet invariant sont tenues par la base
//     — index unique partiel et trigger — parce qu'une vérification en Go se
//     laisse doubler par une écriture concurrente ; ce que le domaine vérifie
//     avant d'écrire sert à refuser tôt, pas à garantir.
//
// Les montants sont des entiers de centimes ([Montant]), jamais des flottants :
// 11 800,50 € n'a pas de représentation binaire exacte, et deux additions
// suffisent à faire diverger une comparaison de ce que dit le papier.
//
// Frontières : ce package n'importe aucun autre domaine, aucun adapter, ni
// internal/platform. L'acteur d'une action arrive en valeur ([ActeurID]) et les
// pièces jointes sont désignées par identifiant ([Devis.DocumentIDs]), jamais
// par un pointeur vers l'agrégat d'un autre domaine.
package devis
