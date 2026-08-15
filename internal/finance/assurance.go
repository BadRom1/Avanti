package finance

import (
	"fmt"
	"slices"
	"time"
)

// StatutAssurance est l'état d'une pièce dans le suivi des indemnités.
//
// Les valeurs sont en français parce qu'elles sont stockées telles quelles en
// base et affichées telles quelles : c'est le même vocabulaire des deux côtés,
// et la correspondance se lit sans table de traduction.
type StatutAssurance string

// Les trois états du suivi, dans l'ordre du cycle de vie.
const (
	// AssuranceNonEnvoyee est l'état de naissance : la pièce n'a pas encore été
	// transmise à l'assurance.
	AssuranceNonEnvoyee StatutAssurance = "non_envoyee"
	// AssuranceEnvoyee dit que la pièce est partie vers l'assurance et attend
	// son indemnisation.
	AssuranceEnvoyee StatutAssurance = "envoyee"
	// AssuranceRemboursee dit que l'indemnité est arrivée, pour le montant que
	// porte le suivi.
	AssuranceRemboursee StatutAssurance = "remboursee"
)

// allStatutsAssurance énumère les statuts reconnus, dans l'ordre du cycle de
// vie. C'est la référence de [StatutAssurance.Known].
var allStatutsAssurance = []StatutAssurance{AssuranceNonEnvoyee, AssuranceEnvoyee, AssuranceRemboursee}

// Known indique si le statut fait partie de ceux que le domaine reconnaît.
func (s StatutAssurance) Known() bool {
	return slices.Contains(allStatutsAssurance, s)
}

// String rend le statut tel qu'il est stocké.
func (s StatutAssurance) String() string {
	return string(s)
}

// SuiviAssurance est le suivi d'indemnisation d'une pièce — facture ou acompte,
// les deux entités l'embarquent par valeur.
//
// Le cycle ne va que dans un sens : non_envoyee → envoyee → remboursee, jamais
// en arrière. Chaque transition emporte son horodatage, et le remboursement son
// montant : c'est ce que le dossier d'assurance devra dire, pièce par pièce.
type SuiviAssurance struct {
	// Statut est l'état du suivi.
	Statut StatutAssurance
	// SentAt est la date d'envoi à l'assurance, nulle tant que rien n'est parti.
	SentAt time.Time
	// MontantRembourse est l'indemnité reçue, en centimes. Zéro tant que la
	// pièce n'est pas remboursée ; jamais supérieure au montant de la pièce.
	MontantRembourse Montant
	// RefundedAt est la date du remboursement, nulle tant qu'il n'a pas eu lieu.
	RefundedAt time.Time
}

// newSuiviAssurance rend le suivi d'une pièce qui vient d'être saisie : rien
// n'est encore parti vers l'assurance.
func newSuiviAssurance() SuiviAssurance {
	return SuiviAssurance{Statut: AssuranceNonEnvoyee}
}

// send fait passer le suivi à « envoyée ». Seule une pièce jamais envoyée peut
// partir : renvoyer une pièce déjà transmise ou déjà remboursée est refusé.
func (s SuiviAssurance) send(at time.Time) (SuiviAssurance, error) {
	if at.IsZero() {
		return SuiviAssurance{}, fmt.Errorf("%w : date d'envoi à l'assurance", ErrMissingDate)
	}
	if s.Statut != AssuranceNonEnvoyee {
		return SuiviAssurance{}, fmt.Errorf("%w : %s → %s", ErrForbiddenAssuranceTransition, s.Statut, AssuranceEnvoyee)
	}

	sent := s
	sent.Statut = AssuranceEnvoyee
	sent.SentAt = at.UTC()

	return sent, nil
}

// refund fait passer le suivi à « remboursée ». Le remboursement exige une
// pièce déjà envoyée, et un montant strictement positif qui ne dépasse pas
// montantPiece — l'assurance ne rembourse pas plus que ce qui a été dépensé.
func (s SuiviAssurance) refund(rembourse, montantPiece Montant, at time.Time) (SuiviAssurance, error) {
	if at.IsZero() {
		return SuiviAssurance{}, fmt.Errorf("%w : date du remboursement", ErrMissingDate)
	}
	if s.Statut != AssuranceEnvoyee {
		return SuiviAssurance{}, fmt.Errorf("%w : %s → %s", ErrForbiddenAssuranceTransition, s.Statut, AssuranceRemboursee)
	}
	if rembourse <= 0 || rembourse > montantPiece {
		return SuiviAssurance{}, fmt.Errorf("%w : %s pour une pièce de %s", ErrInvalidRemboursement, rembourse, montantPiece)
	}

	refunded := s
	refunded.Statut = AssuranceRemboursee
	refunded.MontantRembourse = rembourse
	refunded.RefundedAt = at.UTC()

	return refunded, nil
}
