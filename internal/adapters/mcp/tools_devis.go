package mcp

import (
	"context"
	"fmt"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Romain-Badino/Avanti/internal/devis"
	"github.com/Romain-Badino/Avanti/internal/identity"
)

// artisanJSON est une entreprise sollicitée, telle que les tools la rendent.
type artisanJSON struct {
	Entreprise string `json:"entreprise" jsonschema:"raison sociale de l'entreprise"`
	Email      string `json:"email,omitempty" jsonschema:"adresse de contact, facultative"`
	Telephone  string `json:"telephone,omitempty" jsonschema:"numéro de contact, facultatif"`
}

// demandeJSON est une demande de devis, telle que les tools la rendent.
type demandeJSON struct {
	ID          string        `json:"id" jsonschema:"identifiant de la demande"`
	Lot         string        `json:"lot" jsonschema:"intitulé du lot de travaux consulté"`
	Description string        `json:"description,omitempty" jsonschema:"ce qui a été demandé aux entreprises"`
	Artisans    []artisanJSON `json:"artisans,omitempty" jsonschema:"entreprises sollicitées"`
	EnvoyeeLe   string        `json:"envoyee_le" jsonschema:"date d'envoi de la consultation, AAAA-MM-JJ"`
}

// devisJSON est un devis reçu, tel que les tools le rendent.
type devisJSON struct {
	ID              string   `json:"id" jsonschema:"identifiant du devis"`
	DemandeID       string   `json:"demande_id" jsonschema:"identifiant de la demande à laquelle il répond"`
	Entreprise      string   `json:"entreprise" jsonschema:"entreprise qui a chiffré"`
	MontantCentimes int64    `json:"montant_centimes" jsonschema:"prix proposé, en centimes d'euro entiers"`
	Statut          string   `json:"statut" jsonschema:"recu, retenu ou refuse"`
	RecuLe          string   `json:"recu_le" jsonschema:"date de réception, AAAA-MM-JJ"`
	ValideJusquau   string   `json:"valide_jusquau,omitempty" jsonschema:"expiration annoncée, vide si l'artisan n'a rien annoncé"`
	Notes           string   `json:"notes,omitempty" jsonschema:"réserves ou précisions saisies avec le devis"`
	DocumentIDs     []string `json:"document_ids,omitempty" jsonschema:"identifiants des pièces jointes du domaine document"`
}

// comparaisonJSON est une demande et ses devis mis en regard.
type comparaisonJSON struct {
	Demande       demandeJSON `json:"demande"`
	Devis         []devisJSON `json:"devis" jsonschema:"devis reçus, du moins-disant au plus-disant"`
	Close         bool        `json:"close" jsonschema:"vrai quand un devis est retenu : la comparaison est tranchée"`
	EcartCentimes int64       `json:"ecart_centimes" jsonschema:"écart entre la plus haute et la plus basse offre, en centimes ; zéro sous deux devis"`
}

// devisListeResult est la sortie de devis_liste.
type devisListeResult struct {
	Comparaisons []comparaisonJSON `json:"comparaisons" jsonschema:"toutes les consultations, de la plus récemment envoyée à la plus ancienne"`
}

func (h *Handler) handleDevisListe(ctx context.Context, req *sdk.CallToolRequest, _ struct{}) (*sdk.CallToolResult, devisListeResult, error) {
	comparaisons, err := readList(ctx, h, req, "devis_liste", identity.ScopeDevisRead,
		h.devis.Comparaisons, newComparaisonJSON)
	if err != nil {
		return nil, devisListeResult{}, err
	}

	return nil, devisListeResult{Comparaisons: comparaisons}, nil
}

// devisDetailInput est l'entrée de devis_detail.
type devisDetailInput struct {
	DemandeID string `json:"demande_id" jsonschema:"identifiant de la demande de devis"`
}

func (h *Handler) handleDevisDetail(ctx context.Context, req *sdk.CallToolRequest, in devisDetailInput) (*sdk.CallToolResult, comparaisonJSON, error) {
	var zero comparaisonJSON

	if _, err := h.requireScope(ctx, req, identity.ScopeDevisRead); err != nil {
		return nil, zero, err
	}

	comparaison, err := h.devis.Compare(ctx, devis.ID(in.DemandeID))
	if err != nil {
		return nil, zero, h.failTool(ctx, "devis_detail", err)
	}

	return nil, newComparaisonJSON(comparaison), nil
}

// devisEnregistrerInput est l'entrée de devis_enregistrer.
type devisEnregistrerInput struct {
	DemandeID       string `json:"demande_id" jsonschema:"identifiant de la demande, obligatoire"`
	Entreprise      string `json:"entreprise" jsonschema:"entreprise qui a chiffré, obligatoire"`
	Email           string `json:"email,omitempty" jsonschema:"adresse de contact de l'entreprise, facultative"`
	Telephone       string `json:"telephone,omitempty" jsonschema:"numéro de contact, facultatif"`
	MontantCentimes int64  `json:"montant_centimes" jsonschema:"prix proposé, en centimes d'euro entiers, strictement positif"`
	RecuLe          string `json:"recu_le" jsonschema:"date de réception, AAAA-MM-JJ"`
	ValiditeJours   int    `json:"validite_jours,omitempty" jsonschema:"durée de validité annoncée, en jours ; zéro si non renseignée"`
	Notes           string `json:"notes,omitempty" jsonschema:"réserves ou précisions, facultatives"`
}

// maxValiditeJours borne la durée de validité saisie : cent ans de jours. La
// borne n'est pas là pour juger d'une validité réelle — le domaine refuse déjà
// le négatif — mais pour qu'une valeur absurde soit refusée AVANT la conversion
// en time.Duration, qu'un très grand nombre de jours ferait déborder.
const maxValiditeJours = 36600

func (h *Handler) handleDevisEnregistrer(ctx context.Context, req *sdk.CallToolRequest, in devisEnregistrerInput) (*sdk.CallToolResult, devisJSON, error) {
	var zero devisJSON

	actor, err := h.requireScope(ctx, req, identity.ScopeDevisWrite)
	if err != nil {
		return nil, zero, err
	}

	if in.ValiditeJours < 0 || in.ValiditeJours > maxValiditeJours {
		return nil, zero, fmt.Errorf(
			"durée de validité invalide : %d jours — entre 0 et %d", in.ValiditeJours, maxValiditeJours)
	}

	recuLe, err := parseDate(in.RecuLe, "de réception")
	if err != nil {
		return nil, zero, err
	}

	proposition, err := h.devis.RecordDevis(ctx, devis.DevisInput{
		DemandeID: devis.ID(in.DemandeID),
		Artisan: devis.Artisan{
			Entreprise: in.Entreprise,
			Email:      in.Email,
			Telephone:  in.Telephone,
		},
		Montant:    devis.Montant(in.MontantCentimes),
		ReceivedAt: recuLe,
		Validity:   time.Duration(in.ValiditeJours) * 24 * time.Hour,
		Notes:      in.Notes,
		By:         devisActeur(actor),
	})
	if err != nil {
		return nil, zero, h.failTool(ctx, "devis_enregistrer", err)
	}

	return nil, newDevisJSON(proposition), nil
}

// newComparaisonJSON met une comparaison du domaine sous sa forme de sortie.
func newComparaisonJSON(comparaison devis.Comparaison) comparaisonJSON {
	out := comparaisonJSON{
		Demande:       newDemandeJSON(comparaison.Demande),
		Devis:         make([]devisJSON, 0, len(comparaison.Devis)),
		Close:         comparaison.Closed(),
		EcartCentimes: int64(comparaison.Ecart()),
	}
	for _, proposition := range comparaison.Devis {
		out.Devis = append(out.Devis, newDevisJSON(proposition))
	}
	return out
}

func newDemandeJSON(demande devis.DemandeDevis) demandeJSON {
	out := demandeJSON{
		ID:          demande.ID.String(),
		Lot:         demande.Lot,
		Description: demande.Description,
		EnvoyeeLe:   formatDate(demande.SentAt),
	}
	for _, artisan := range demande.Artisans {
		out.Artisans = append(out.Artisans, artisanJSON{
			Entreprise: artisan.Entreprise,
			Email:      artisan.Email,
			Telephone:  artisan.Telephone,
		})
	}
	return out
}

func newDevisJSON(proposition devis.Devis) devisJSON {
	out := devisJSON{
		ID:              proposition.ID.String(),
		DemandeID:       proposition.DemandeID.String(),
		Entreprise:      proposition.Artisan.Entreprise,
		MontantCentimes: int64(proposition.Montant),
		Statut:          proposition.Statut.String(),
		RecuLe:          formatDate(proposition.ReceivedAt),
		Notes:           proposition.Notes,
		DocumentIDs:     proposition.DocumentIDs,
	}
	if limit, known := proposition.ValidUntil(); known {
		out.ValideJusquau = formatDate(limit)
	}
	return out
}

// devisActeur traduit l'identité du jeton en valeur pour le domaine devis —
// même partage que l'adapter web : le domaine reçoit un identifiant d'acteur en
// simple valeur, jamais l'acteur lui-même (R1).
func devisActeur(actor identity.Actor) devis.ActeurID {
	return devis.ActeurID(actor.UserID().String())
}
