package devis

import (
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// Bornes des textes d'une demande.
const (
	maxLotLength         = 120
	maxDescriptionLength = 4000
)

// DemandeDevis est la consultation d'un lot de travaux : ce qu'on a demandé, à
// qui, et quand.
//
// C'est elle qui donne son sens à la comparaison. Deux devis ne se comparent
// que parce qu'ils répondent à la même demande — même lot, même description,
// même moment. Un devis reçu hors demande n'aurait rien en face de lui.
type DemandeDevis struct {
	// ID identifie la demande.
	ID ID
	// Lot est l'intitulé du lot de travaux consulté : « Charpente »,
	// « Électricité », « Menuiseries extérieures ». C'est le titre que porte la
	// demande dans toute l'interface.
	Lot string
	// Description précise ce qui a été demandé : surfaces, matériaux, contraintes
	// de chantier. Facultative, mais c'est elle qui permet de dire six mois plus
	// tard pourquoi deux devis diffèrent de vingt pour cent.
	Description string
	// Artisans sont les entreprises sollicitées. La liste peut être vide : on
	// crée souvent la demande avant d'avoir arrêté qui consulter.
	Artisans []Artisan
	// SentAt est la date d'envoi de la consultation.
	SentAt time.Time
	// CreatedBy est l'acteur qui a ouvert la demande.
	CreatedBy ActeurID
	// CreatedAt est la date d'enregistrement dans Avanti, distincte de [SentAt] :
	// une consultation partie par courrier se saisit après coup.
	CreatedAt time.Time
	// UpdatedAt est la date de la dernière modification.
	UpdatedAt time.Time
}

// Artisan cherche une entreprise sollicitée par sa raison sociale, sans tenir
// compte de la casse.
func (d DemandeDevis) Artisan(entreprise string) (Artisan, bool) {
	needle := strings.ToLower(strings.TrimSpace(entreprise))

	index := slices.IndexFunc(d.Artisans, func(candidate Artisan) bool {
		return strings.EqualFold(candidate.Entreprise, needle)
	})
	if index < 0 {
		return Artisan{}, false
	}

	return d.Artisans[index], true
}

// NormalizeLot met l'intitulé d'un lot sous sa forme canonique et refuse un
// intitulé vide.
func NormalizeLot(raw string) (string, error) {
	lot := strings.Join(strings.Fields(raw), " ")

	if lot == "" {
		return "", ErrEmptyLot
	}
	if utf8.RuneCountInString(lot) > maxLotLength {
		return "", fmt.Errorf("%w : intitulé de lot de plus de %d caractères", ErrTextTooLong, maxLotLength)
	}

	return lot, nil
}

// normalizeText borne un texte libre sans en changer la mise en forme : les
// retours à la ligne d'une description ou de notes font partie de ce qui a été
// saisi. Seuls les blancs de début et de fin partent.
func normalizeText(raw string, limit int, label string) (string, error) {
	text := strings.TrimSpace(raw)

	if utf8.RuneCountInString(text) > limit {
		return "", fmt.Errorf("%w : %s de plus de %d caractères", ErrTextTooLong, label, limit)
	}

	return text, nil
}
