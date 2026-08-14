package finance

import "fmt"

// Montant est une somme en centimes d'euro.
//
// Le centime entier est l'unité de tout le domaine, et ce n'est pas un détail
// d'implémentation : un flottant ne représente pas exactement 11 800,50 €, et
// sur un dossier d'assurance, un centime qui apparaît ou disparaît à l'addition
// n'est pas une approximation acceptable.
//
// Le type est celui du domaine finance, distinct de devis.Montant : R2 de
// docs/ARCHITECTURE.md interdit d'importer un autre domaine, y compris pour lui
// emprunter un type. Le montant engagé d'un devis retenu arrive donc ici EN
// VALEUR, converti par l'adapter appelant.
type Montant int64

// centimesParEuro est le facteur de la seule conversion d'unité du domaine.
const centimesParEuro = 100

// MaxMontant borne une pièce à cent millions d'euros — la même borne que celle
// des devis, puisque les factures et acomptes se rapprochent des montants
// engagés.
//
// La borne n'est pas là pour juger du prix des travaux : elle est là pour
// qu'une saisie manifestement fautive — un montant collé deux fois, des
// centimes pris pour des euros — soit refusée à l'entrée plutôt que de fausser
// une synthèse. Elle laisse aussi une marge confortable avant tout risque de
// débordement d'int64 sur un cumul de pièces.
const MaxMontant Montant = 100_000_000 * centimesParEuro

// Valid dit si le montant est celui d'une pièce recevable : strictement positif
// et sous [MaxMontant]. Une facture à zéro n'est pas une facture.
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
