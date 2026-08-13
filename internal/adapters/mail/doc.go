// Package mail implémente les ports de notification sortante des domaines :
// envoi SMTP des relances aux artisans, alertes de jalon dépassé, réinitialisation
// de mot de passe.
//
// Frontières : c'est un adapter, il implémente des interfaces définies par les
// domaines et peut utiliser internal/platform. Aucun domaine ne l'importe, et
// il n'importe aucune autre famille d'adapters : leur seul point de rencontre
// est cmd/avanti.
package mail
