// Package planning porte le domaine de l'ordonnancement du chantier : les
// Etapes de travaux, leurs dépendances, les Jalons contractuels, et le calcul
// des dates ainsi que des retards qui en découlent.
//
// Le vocabulaire métier français (Etape, Jalon) est conservé dans les
// identifiants exportés : c'est le langage ubiquitaire du projet.
//
// Contenu : les entités et invariants du domaine (une Etape ne peut pas
// démarrer avant ses prérequis terminés, les dépendances ne forment pas de
// cycle), le port de persistance et le service applicatif. Le statut d'une
// étape et le diagramme de Gantt ne sont pas stockés : ils sont dérivés des
// dates et des dépendances au moment du rendu.
//
// Frontières : ce package n'importe aucun autre domaine, aucun adapter, ni
// internal/platform. Le rattachement d'une Etape au Devis qui la finance est un
// identifiant faible (devisID), pas un import du package devis.
package planning
