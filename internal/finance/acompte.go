package finance

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// MoyenPaiement est le canal par lequel un acompte a été versé.
//
// Les valeurs sont en français : stockées telles quelles en base, affichées
// telles quelles.
type MoyenPaiement string

// Les moyens de paiement reconnus.
const (
	// MoyenVirement est le virement bancaire, le cas le plus courant.
	MoyenVirement MoyenPaiement = "virement"
	// MoyenCheque est le chèque.
	MoyenCheque MoyenPaiement = "cheque"
	// MoyenEspeces est le paiement en espèces.
	MoyenEspeces MoyenPaiement = "especes"
	// MoyenCarte est le paiement par carte bancaire.
	MoyenCarte MoyenPaiement = "carte"
)

// allMoyensPaiement énumère les moyens dans l'ordre où ils sont présentés à
// l'utilisateur. C'est la référence de [MoyenPaiement.Known].
var allMoyensPaiement = []MoyenPaiement{MoyenVirement, MoyenCheque, MoyenEspeces, MoyenCarte}

// AllMoyensPaiement renvoie les moyens reconnus, dans un ordre stable.
//
// La tranche renvoyée est une copie : la modifier ne change rien au domaine.
func AllMoyensPaiement() []MoyenPaiement {
	return slices.Clone(allMoyensPaiement)
}

// Known indique si le moyen fait partie de ceux que le domaine reconnaît.
func (m MoyenPaiement) Known() bool {
	return slices.Contains(allMoyensPaiement, m)
}

// String rend le moyen tel qu'il est stocké.
func (m MoyenPaiement) String() string {
	return string(m)
}

// NormalizeMoyenPaiement met une saisie de moyen de paiement sous sa forme
// canonique et refuse ce que le domaine ne reconnaît pas.
func NormalizeMoyenPaiement(raw string) (MoyenPaiement, error) {
	moyen := MoyenPaiement(strings.ToLower(strings.TrimSpace(raw)))
	if !moyen.Known() {
		return "", fmt.Errorf("%w : %q", ErrUnknownMoyenPaiement, raw)
	}

	return moyen, nil
}

// Acompte est un versement fait à une entreprise — acompte à la commande,
// situation intermédiaire, solde.
//
// C'est un paiement par nature : il n'a pas de statut de règlement, seulement
// un suivi d'assurance. L'entité se manipule par valeur et ses transitions
// rendent un nouvel Acompte plutôt que de muter le récepteur.
type Acompte struct {
	// ID identifie l'acompte.
	ID ID
	// DevisID rattache l'acompte au devis retenu qu'il paie, par identifiant
	// faible (R2 de docs/ARCHITECTURE.md). Vide, c'est un versement hors devis
	// — et il échappe alors à l'invariant du cumul, faute de montant engagé à
	// comparer.
	DevisID string
	// Entreprise est le nom de qui a été payé. Obligatoire.
	Entreprise string
	// Montant est la somme versée, en centimes. Toujours strictement positive.
	Montant Montant
	// Date est la date du versement.
	Date time.Time
	// Moyen est le canal du versement.
	Moyen MoyenPaiement
	// Notes porte ce que le versement ne dit pas. Facultatives.
	Notes string
	// Assurance est le suivi d'indemnisation de la pièce.
	Assurance SuiviAssurance
	// RecordedBy est l'acteur qui a saisi l'acompte.
	RecordedBy ActeurID
	// CreatedAt est la date d'enregistrement dans Avanti, distincte de [Date].
	CreatedAt time.Time
	// UpdatedAt est la date de la dernière modification.
	UpdatedAt time.Time
}

// MarkEnvoyeAssurance marque l'acompte comme transmis à l'assurance.
func (a Acompte) MarkEnvoyeAssurance(at time.Time) (Acompte, error) {
	suivi, err := a.Assurance.send(at)
	if err != nil {
		return Acompte{}, err
	}

	sent := a
	sent.Assurance = suivi
	sent.UpdatedAt = at.UTC()

	return sent, nil
}

// MarkRembourse marque l'acompte comme indemnisé du montant donné.
func (a Acompte) MarkRembourse(rembourse Montant, at time.Time) (Acompte, error) {
	suivi, err := a.Assurance.refund(rembourse, a.Montant, at)
	if err != nil {
		return Acompte{}, err
	}

	refunded := a
	refunded.Assurance = suivi
	refunded.UpdatedAt = at.UTC()

	return refunded, nil
}
