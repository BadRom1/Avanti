package devis

import "fmt"

// Montant est une somme en centimes d'euro.
//
// Le centime entier est l'unité de tout le domaine, et ce n'est pas un détail
// d'implémentation. Un flottant ne représente pas exactement 11 800,50 € ; sur
// une comparaison de devis, deux additions suffisent à faire apparaître un
// centime qui n'existe pas, et c'est précisément le chiffre que l'utilisateur
// vérifie contre le papier de l'artisan.
//
// La conversion depuis et vers la notation « 11 800,50 » est le travail de
// l'interface, pas du domaine : [Montant.Split] lui donne les deux entiers dont
// elle a besoin pour la faire sans jamais passer par un flottant.
type Montant int64

// centimesParEuro est le facteur de la seule conversion d'unité du domaine.
const centimesParEuro = 100

// MaxMontant borne un devis à cent millions d'euros.
//
// La borne n'est pas là pour juger du prix des travaux : elle est là pour
// qu'une saisie manifestement fautive — un montant collé deux fois, des
// centimes pris pour des euros — soit refusée à l'entrée plutôt que de fausser
// une comparaison. Elle laisse aussi une marge confortable avant tout risque de
// débordement d'int64 sur une somme de devis.
const MaxMontant Montant = 100_000_000 * centimesParEuro

// Valid dit si le montant est celui d'un devis recevable : strictement positif
// et sous [MaxMontant]. Un devis à zéro n'est pas un devis.
func (m Montant) Valid() bool {
	return m > 0 && m <= MaxMontant
}

// Split sépare le montant en euros entiers et centimes restants, tous deux
// positifs pour un montant positif.
//
// C'est ce qu'il faut pour écrire « 11 800,50 » sans flottant, et c'est la seule
// façon offerte de le faire : rien dans le domaine ne rend un float64, de sorte
// qu'aucun appelant ne puisse en fabriquer un par commodité.
func (m Montant) Split() (euros, centimes int64) {
	value := int64(m)
	if value < 0 {
		value = -value
	}
	return value / centimesParEuro, value % centimesParEuro
}

// String rend le montant en centimes, avec son unité. C'est une forme de
// journal et de débogage : l'affichage à l'utilisateur passe par l'interface,
// qui sait grouper les milliers et poser le symbole de la monnaie.
func (m Montant) String() string {
	return fmt.Sprintf("%d centimes", int64(m))
}
