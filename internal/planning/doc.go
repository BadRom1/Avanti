// Package planning porte le domaine de l'ordonnancement du chantier : les
// Etapes de travaux, leurs dépendances, les Jalons contractuels, et le calcul
// des dates ainsi que des retards qui en découlent.
//
// Le vocabulaire métier français (Etape, Jalon) est conservé dans les
// identifiants exportés : c'est le langage ubiquitaire du projet.
//
// Contenu attendu : les entités et invariants du domaine (une Etape ne peut pas
// démarrer avant ses prérequis), les ports (persistance, horloge) et les
// services applicatifs. Le diagramme de Gantt n'est pas stocké : il est dérivé
// des Etapes et de leurs dépendances au moment du rendu.
//
// Frontières : ce package n'importe aucun autre domaine, aucun adapter, ni
// internal/platform. Le rattachement d'une Etape au Devis qui la finance est un
// identifiant faible (devisID), pas un import du package devis.
package planning
