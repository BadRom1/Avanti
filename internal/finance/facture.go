package finance

import (
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// Bornes des textes saisis. Elles ne défendent pas un format, elles bornent ce
// qu'une saisie peut faire stocker : un champ de formulaire n'a pas de limite
// naturelle, une colonne si.
const (
	maxEntrepriseLength = 200
	maxNumeroLength     = 80
	maxNotesLength      = 2000
	// maxDevisIDLength borne la référence faible vers un devis. Les identifiants
	// réels sont des UUID de 36 caractères ; la borne n'est pas là pour eux mais
	// pour qu'un POST forgé ne stocke pas un roman dans une colonne de
	// référence.
	maxDevisIDLength = 255
)

// StatutPaiement est l'état de règlement d'une facture.
//
// Les valeurs sont en français : stockées telles quelles en base, affichées
// telles quelles.
type StatutPaiement string

// Les deux états d'une facture, dans l'ordre du cycle de vie.
const (
	// PaiementImpayee est l'état de naissance : la facture est arrivée, rien
	// n'est encore parti.
	PaiementImpayee StatutPaiement = "impayee"
	// PaiementPayee dit que la facture est réglée. Le paiement ne se reprend
	// pas.
	PaiementPayee StatutPaiement = "payee"
)

// allStatutsPaiement est la référence de [StatutPaiement.Known].
var allStatutsPaiement = []StatutPaiement{PaiementImpayee, PaiementPayee}

// Known indique si le statut fait partie de ceux que le domaine reconnaît.
func (s StatutPaiement) Known() bool {
	return slices.Contains(allStatutsPaiement, s)
}

// String rend le statut tel qu'il est stocké.
func (s StatutPaiement) String() string {
	return string(s)
}

// Facture est une facture reçue d'une entreprise — ou une dépense directe qui
// en tient lieu.
//
// L'entité se manipule par valeur et ses transitions rendent une nouvelle
// Facture plutôt que de muter le récepteur : une facture passée dans une
// fonction ne peut pas revenir changée, et l'appelant décide explicitement de
// ce qu'il persiste.
type Facture struct {
	// ID identifie la facture.
	ID ID
	// DevisID rattache la facture au devis retenu qu'elle exécute, par
	// identifiant faible (R2 de docs/ARCHITECTURE.md) : une simple chaîne,
	// jamais le type du domaine devis. Vide, la facture est une dépense hors
	// devis — achat direct de matériaux, auto-construction.
	DevisID string
	// Entreprise est le nom de qui a facturé. Obligatoire, recopié depuis le
	// papier : ce qui figure sur la facture ne doit pas changer quand une liste
	// d'artisans est corrigée ailleurs.
	Entreprise string
	// Montant est le montant TTC de la facture, en centimes. Toujours
	// strictement positif.
	Montant Montant
	// Date est la date que porte la facture.
	Date time.Time
	// Numero est la référence que l'entreprise donne à sa facture. Facultatif :
	// un ticket de caisse n'en a pas.
	Numero string
	// Notes porte ce que la facture ne dit pas : un litige en cours, une remise
	// négociée au téléphone. Facultatives.
	Notes string
	// Paiement est l'état de règlement.
	Paiement StatutPaiement
	// PaidAt est la date du règlement, nulle tant que la facture est impayée.
	PaidAt time.Time
	// Assurance est le suivi d'indemnisation de la pièce.
	Assurance SuiviAssurance
	// RecordedBy est l'acteur qui a saisi la facture.
	RecordedBy ActeurID
	// CreatedAt est la date d'enregistrement dans Avanti, distincte de [Date] :
	// une facture reçue par courrier se saisit après coup.
	CreatedAt time.Time
	// UpdatedAt est la date de la dernière modification.
	UpdatedAt time.Time
}

// MarkPayee marque la facture comme réglée à la date donnée.
//
// La transition ne va que dans un sens : une facture payée le reste. Le refus
// ci-dessous attrape le double règlement séquentiel — celui qui relit une
// facture déjà payée ; deux règlements réellement simultanés, qui lisent tous
// deux une facture impayée, sont départagés par la garde optimiste du
// [Repository] (voir ErrConcurrentUpdate) : le second n'écrit pas.
func (f Facture) MarkPayee(at time.Time) (Facture, error) {
	if at.IsZero() {
		return Facture{}, fmt.Errorf("%w : date de paiement", ErrMissingDate)
	}
	if f.Paiement != PaiementImpayee {
		return Facture{}, fmt.Errorf("%w : %s", ErrFactureAlreadyPaid, f.ID)
	}

	paid := f
	paid.Paiement = PaiementPayee
	paid.PaidAt = at.UTC()
	paid.UpdatedAt = at.UTC()

	return paid, nil
}

// MarkEnvoyeeAssurance marque la facture comme transmise à l'assurance.
func (f Facture) MarkEnvoyeeAssurance(at time.Time) (Facture, error) {
	suivi, err := f.Assurance.send(at)
	if err != nil {
		return Facture{}, err
	}

	sent := f
	sent.Assurance = suivi
	sent.UpdatedAt = at.UTC()

	return sent, nil
}

// MarkRemboursee marque la facture comme indemnisée du montant donné.
func (f Facture) MarkRemboursee(rembourse Montant, at time.Time) (Facture, error) {
	suivi, err := f.Assurance.refund(rembourse, f.Montant, at)
	if err != nil {
		return Facture{}, err
	}

	refunded := f
	refunded.Assurance = suivi
	refunded.UpdatedAt = at.UTC()

	return refunded, nil
}

// normalizeEntreprise met un nom d'entreprise sous sa forme canonique et refuse
// un nom vide. Les suites de blancs sont réduites, comme pour les artisans du
// domaine devis : deux saisies de la même entreprise doivent se cumuler dans la
// même synthèse.
func normalizeEntreprise(raw string) (string, error) {
	entreprise := strings.Join(strings.Fields(raw), " ")
	if entreprise == "" {
		return "", ErrEmptyEntreprise
	}
	if utf8.RuneCountInString(entreprise) > maxEntrepriseLength {
		return "", fmt.Errorf("%w : nom d'entreprise de plus de %d caractères", ErrTextTooLong, maxEntrepriseLength)
	}

	return entreprise, nil
}

// normalizeDevisID nettoie une référence faible de devis. Vide reste vide —
// c'est une dépense hors devis — et le domaine ne vérifie pas que l'identifiant
// désigne quelque chose : il ne connaît pas le domaine devis (R2), il borne
// seulement ce qui se stocke.
func normalizeDevisID(raw string) (string, error) {
	devisID := strings.TrimSpace(raw)
	if utf8.RuneCountInString(devisID) > maxDevisIDLength {
		return "", fmt.Errorf("%w : plus de %d caractères", ErrInvalidDevisID, maxDevisIDLength)
	}

	return devisID, nil
}

// normalizeNumero nettoie la référence de facture : blancs internes réduits,
// borne appliquée après nettoyage. Vide reste vide, le champ est facultatif.
func normalizeNumero(raw string) (string, error) {
	numero := strings.Join(strings.Fields(raw), " ")
	if utf8.RuneCountInString(numero) > maxNumeroLength {
		return "", fmt.Errorf("%w : numéro de plus de %d caractères", ErrTextTooLong, maxNumeroLength)
	}

	return numero, nil
}

// normalizeNotes borne les notes sans en changer la mise en forme : les retours
// à la ligne font partie de ce qui a été saisi. Seuls les blancs de bordure
// partent.
func normalizeNotes(raw string) (string, error) {
	notes := strings.TrimSpace(raw)
	if utf8.RuneCountInString(notes) > maxNotesLength {
		return "", fmt.Errorf("%w : notes de plus de %d caractères", ErrTextTooLong, maxNotesLength)
	}

	return notes, nil
}
