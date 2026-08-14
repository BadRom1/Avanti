// Package finance porte le domaine de l'argent du chantier : les Factures reçues
// des artisans, les Acomptes versés, le rapprochement avec les montants acceptés
// et le suivi des indemnités d'assurance.
//
// Le vocabulaire métier français (Facture, Acompte) est conservé dans les
// identifiants exportés : c'est le langage ubiquitaire du projet.
//
// Contenu : les entités et invariants du domaine (le cumul des Acomptes d'un
// devis ne dépasse pas le montant engagé), les ports — persistance
// (Repository) et rendu du dossier d'assurance (ExportFormat, second point
// d'extension officiel) — et les services applicatifs. Les montants sont
// manipulés en centimes entiers, jamais en flottants.
//
// Frontières : ce package n'importe aucun autre domaine, aucun adapter, ni
// internal/platform. Une Facture référence son Devis par identifiant faible
// (Facture.devisID) : aucun import du package devis, aucune jointure en mémoire
// entre agrégats de domaines différents.
package finance
