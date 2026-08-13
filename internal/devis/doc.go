// Package devis porte le domaine de la consultation des artisans : demandes de
// devis, propositions chiffrées reçues, comparaison des offres, et acceptation
// qui fait entrer un lot de travaux en exécution.
//
// Le vocabulaire métier français est conservé tel quel dans les identifiants
// exportés (Devis, Lot, Poste, Artisan) : c'est le langage ubiquitaire du
// projet, celui qu'emploient les documents papier échangés avec les entreprises
// et l'assurance. Le reste du code (verbes, helpers techniques) est en anglais.
//
// Contenu attendu : les entités et invariants du domaine, les ports (interfaces
// Go décrivant ce dont le domaine a besoin — persistance, horloge, notification)
// et les services applicatifs qui orchestrent les cas d'usage.
//
// Frontières : ce package n'importe aucun autre domaine, aucun adapter, ni
// internal/platform. Une référence vers un autre domaine se fait par identifiant
// faible (un DevisID transporté, jamais un pointeur vers son agrégat).
package devis
