// Package mail est la famille d'adapters RÉSERVÉE aux notifications sortantes.
//
// Elle est vide en V1, et c'est un choix, pas un retard : aucun domaine ne
// définit de port d'envoi (pas de MailSender), aucune fonctionnalité livrée
// n'expédie quoi que ce soit — toute transmission (relance d'artisan, envoi à
// l'assurance) reste un geste humain, et la feuille de route classe les
// notifications automatiques hors du périmètre V1. Le répertoire existe pour
// que la frontière soit déjà tracée dans .golangci.yml le jour où un domaine
// définira un tel port ; d'ici là, rien ici ne promet d'envoyer un mail.
//
// Frontières : c'est un adapter, il implémentera des interfaces définies par
// les domaines et peut utiliser internal/platform. Aucun domaine ne l'importe,
// et il n'importe aucune autre famille d'adapters : leur seul point de
// rencontre est cmd/avanti.
package mail
