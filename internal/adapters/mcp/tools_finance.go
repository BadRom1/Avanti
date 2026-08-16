package mcp

import (
	"context"
	"errors"
	"fmt"
	"sort"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Romain-Badino/Avanti/internal/devis"
	"github.com/Romain-Badino/Avanti/internal/finance"
	"github.com/Romain-Badino/Avanti/internal/identity"
)

// suiviAssuranceJSON est le suivi d'indemnisation d'une pièce.
type suiviAssuranceJSON struct {
	Statut                   string `json:"statut" jsonschema:"non_envoyee, envoyee ou remboursee"`
	EnvoyeeLe                string `json:"envoyee_le,omitempty" jsonschema:"date d'envoi à l'assurance, vide tant que rien n'est parti"`
	MontantRembourseCentimes int64  `json:"montant_rembourse_centimes,omitempty" jsonschema:"indemnité reçue, en centimes ; zéro tant que la pièce n'est pas remboursée"`
	RembourseeLe             string `json:"remboursee_le,omitempty" jsonschema:"date du remboursement, vide tant qu'il n'a pas eu lieu"`
}

func newSuiviAssuranceJSON(suivi finance.SuiviAssurance) suiviAssuranceJSON {
	return suiviAssuranceJSON{
		Statut:                   suivi.Statut.String(),
		EnvoyeeLe:                formatInstant(suivi.SentAt),
		MontantRembourseCentimes: int64(suivi.MontantRembourse),
		RembourseeLe:             formatInstant(suivi.RefundedAt),
	}
}

// factureJSON est une facture, telle que les tools la rendent.
type factureJSON struct {
	ID              string             `json:"id" jsonschema:"identifiant de la facture"`
	DevisID         string             `json:"devis_id,omitempty" jsonschema:"devis retenu que la facture exécute ; vide pour une dépense hors devis"`
	Entreprise      string             `json:"entreprise" jsonschema:"qui a facturé"`
	Numero          string             `json:"numero,omitempty" jsonschema:"référence de la facture, facultative"`
	Date            string             `json:"date" jsonschema:"date que porte la facture, AAAA-MM-JJ"`
	MontantCentimes int64              `json:"montant_centimes" jsonschema:"montant TTC, en centimes d'euro entiers"`
	Notes           string             `json:"notes,omitempty"`
	Paiement        string             `json:"paiement" jsonschema:"impayee ou payee"`
	PayeeLe         string             `json:"payee_le,omitempty" jsonschema:"date du règlement, vide pour une facture impayée"`
	Assurance       suiviAssuranceJSON `json:"assurance"`
}

func newFactureJSON(facture finance.Facture) factureJSON {
	return factureJSON{
		ID:              facture.ID.String(),
		DevisID:         facture.DevisID,
		Entreprise:      facture.Entreprise,
		Numero:          facture.Numero,
		Date:            formatDate(facture.Date),
		MontantCentimes: int64(facture.Montant),
		Notes:           facture.Notes,
		Paiement:        facture.Paiement.String(),
		PayeeLe:         formatInstant(facture.PaidAt),
		Assurance:       newSuiviAssuranceJSON(facture.Assurance),
	}
}

// acompteJSON est un acompte versé, tel que les tools le rendent.
type acompteJSON struct {
	ID              string             `json:"id" jsonschema:"identifiant de l'acompte"`
	DevisID         string             `json:"devis_id,omitempty" jsonschema:"devis retenu que l'acompte paie ; vide pour un versement hors devis"`
	Entreprise      string             `json:"entreprise" jsonschema:"qui a été payé"`
	Date            string             `json:"date" jsonschema:"date du versement, AAAA-MM-JJ"`
	MontantCentimes int64              `json:"montant_centimes" jsonschema:"somme versée, en centimes d'euro entiers"`
	Moyen           string             `json:"moyen" jsonschema:"virement, cheque, especes ou carte"`
	Notes           string             `json:"notes,omitempty"`
	Assurance       suiviAssuranceJSON `json:"assurance"`
}

func newAcompteJSON(acompte finance.Acompte) acompteJSON {
	return acompteJSON{
		ID:              acompte.ID.String(),
		DevisID:         acompte.DevisID,
		Entreprise:      acompte.Entreprise,
		Date:            formatDate(acompte.Date),
		MontantCentimes: int64(acompte.Montant),
		Moyen:           acompte.Moyen.String(),
		Notes:           acompte.Notes,
		Assurance:       newSuiviAssuranceJSON(acompte.Assurance),
	}
}

// ligneSyntheseJSON est une ligne du rapprochement financier.
type ligneSyntheseJSON struct {
	DevisID             string `json:"devis_id,omitempty" jsonschema:"devis retenu de la ligne ; vide pour la ligne hors devis et le total"`
	Libelle             string `json:"libelle,omitempty" jsonschema:"lot et entreprise du devis retenu"`
	DevisDisparu        bool   `json:"devis_disparu,omitempty" jsonschema:"vrai quand des pièces référencent un devis qui n'est plus résoluble"`
	EngageCentimes      int64  `json:"engage_centimes" jsonschema:"montant du devis retenu, en centimes"`
	FactureCentimes     int64  `json:"facture_centimes" jsonschema:"cumul des factures, en centimes"`
	PayeCentimes        int64  `json:"paye_centimes" jsonschema:"cumul de ce qui est sorti, en centimes"`
	RembourseCentimes   int64  `json:"rembourse_centimes" jsonschema:"cumul des indemnités reçues, en centimes"`
	ResteAPayerCentimes int64  `json:"reste_a_payer_centimes" jsonschema:"engagé moins payé, en centimes ; peut être négatif sur un dépassement réel"`
}

// financesSyntheseResult est la sortie de finances_synthese.
type financesSyntheseResult struct {
	Lignes    []ligneSyntheseJSON `json:"lignes" jsonschema:"une ligne par devis retenu, plus les références mortes"`
	HorsDevis *ligneSyntheseJSON  `json:"hors_devis,omitempty" jsonschema:"cumul des pièces sans rattachement ; absent s'il n'y en a aucune"`
	Total     ligneSyntheseJSON   `json:"total" jsonschema:"total chantier, toutes pièces confondues"`
}

// handleFinancesSynthese rend le même assemblage transverse que la page web des
// finances : les devis retenus et leurs montants engagés viennent du domaine
// devis, les cumuls du domaine finance, et la composition se fait ici (R2).
func (h *Handler) handleFinancesSynthese(ctx context.Context, req *sdk.CallToolRequest, _ struct{}) (*sdk.CallToolResult, financesSyntheseResult, error) {
	var zero financesSyntheseResult

	if _, err := h.requireScope(ctx, req, identity.ScopeFinanceRead); err != nil {
		return nil, zero, err
	}

	retenus, err := h.devisRetenus(ctx)
	if err != nil {
		return nil, zero, h.failTool(ctx, "finances_synthese", err)
	}

	totaux, err := h.finance.Totaux(ctx)
	if err != nil {
		return nil, zero, h.failTool(ctx, "finances_synthese", err)
	}

	return nil, newSynthese(retenus, totaux), nil
}

// newSynthese assemble le rapprochement — le pendant de newSynthese dans
// l'adapter web, en données structurées : une référence morte est un booléen
// devis_disparu plutôt qu'un libellé traduit.
func newSynthese(retenus []retenuInfo, totaux finance.Totaux) financesSyntheseResult {
	out := financesSyntheseResult{Lignes: make([]ligneSyntheseJSON, 0, len(retenus))}

	var engageTotal int64
	seen := make(map[string]bool, len(retenus))
	for _, retenu := range retenus {
		seen[retenu.id] = true
		engageTotal += int64(retenu.montant)

		total := totaux.ParDevis[retenu.id]
		out.Lignes = append(out.Lignes, ligneSyntheseJSON{
			DevisID:             retenu.id,
			Libelle:             retenu.label,
			EngageCentimes:      int64(retenu.montant),
			FactureCentimes:     int64(total.Facture),
			PayeCentimes:        int64(total.Paye),
			RembourseCentimes:   int64(total.Rembourse),
			ResteAPayerCentimes: int64(retenu.montant) - int64(total.Paye),
		})
	}

	// Les références mortes gardent leur ligne, dans un ordre stable : les
	// taire fausserait le total chantier.
	orphans := make([]string, 0)
	for devisID := range totaux.ParDevis {
		if !seen[devisID] {
			orphans = append(orphans, devisID)
		}
	}
	sort.Strings(orphans)
	for _, devisID := range orphans {
		total := totaux.ParDevis[devisID]
		out.Lignes = append(out.Lignes, ligneSyntheseJSON{
			DevisID:           devisID,
			DevisDisparu:      true,
			FactureCentimes:   int64(total.Facture),
			PayeCentimes:      int64(total.Paye),
			RembourseCentimes: int64(total.Rembourse),
		})
	}

	if totaux.HorsDevis != (finance.TotalFinance{}) {
		out.HorsDevis = &ligneSyntheseJSON{
			FactureCentimes:   int64(totaux.HorsDevis.Facture),
			PayeCentimes:      int64(totaux.HorsDevis.Paye),
			RembourseCentimes: int64(totaux.HorsDevis.Rembourse),
		}
	}

	out.Total = ligneSyntheseJSON{
		EngageCentimes:      engageTotal,
		FactureCentimes:     int64(totaux.Chantier.Facture),
		PayeCentimes:        int64(totaux.Chantier.Paye),
		RembourseCentimes:   int64(totaux.Chantier.Rembourse),
		ResteAPayerCentimes: engageTotal - int64(totaux.Chantier.Paye),
	}

	return out
}

// financesFacturesResult est la sortie de finances_factures.
type financesFacturesResult struct {
	Factures []factureJSON `json:"factures" jsonschema:"toutes les factures, de la plus récente à la plus ancienne"`
}

func (h *Handler) handleFinancesFactures(ctx context.Context, req *sdk.CallToolRequest, _ struct{}) (*sdk.CallToolResult, financesFacturesResult, error) {
	factures, err := readList(ctx, h, req, "finances_factures", identity.ScopeFinanceRead,
		h.finance.Factures, newFactureJSON)
	if err != nil {
		return nil, financesFacturesResult{}, err
	}

	return nil, financesFacturesResult{Factures: factures}, nil
}

// financesAcomptesResult est la sortie de finances_acomptes.
type financesAcomptesResult struct {
	Acomptes []acompteJSON `json:"acomptes" jsonschema:"tous les acomptes, du plus récent au plus ancien"`
}

func (h *Handler) handleFinancesAcomptes(ctx context.Context, req *sdk.CallToolRequest, _ struct{}) (*sdk.CallToolResult, financesAcomptesResult, error) {
	acomptes, err := readList(ctx, h, req, "finances_acomptes", identity.ScopeFinanceRead,
		h.finance.Acomptes, newAcompteJSON)
	if err != nil {
		return nil, financesAcomptesResult{}, err
	}

	return nil, financesAcomptesResult{Acomptes: acomptes}, nil
}

// factureEnregistrerInput est l'entrée de facture_enregistrer.
type factureEnregistrerInput struct {
	DevisID         string `json:"devis_id,omitempty" jsonschema:"devis RETENU que la facture exécute ; vide pour une dépense hors devis"`
	Entreprise      string `json:"entreprise" jsonschema:"qui a facturé, obligatoire"`
	MontantCentimes int64  `json:"montant_centimes" jsonschema:"montant TTC, en centimes d'euro entiers, strictement positif"`
	Date            string `json:"date" jsonschema:"date que porte la facture, AAAA-MM-JJ"`
	Numero          string `json:"numero,omitempty" jsonschema:"référence de la facture, facultative"`
	Notes           string `json:"notes,omitempty"`
}

func (h *Handler) handleFactureEnregistrer(ctx context.Context, req *sdk.CallToolRequest, in factureEnregistrerInput) (*sdk.CallToolResult, factureJSON, error) {
	var zero factureJSON

	actor, err := h.requireScope(ctx, req, identity.ScopeFinanceWrite)
	if err != nil {
		return nil, zero, err
	}

	retenu, err := h.resolveRetenu(ctx, in.DevisID)
	if err != nil {
		return nil, zero, h.failTool(ctx, "facture_enregistrer", err)
	}

	date, err := parseDate(in.Date, "de la facture")
	if err != nil {
		return nil, zero, err
	}

	facture, err := h.finance.RecordFacture(ctx, finance.FactureInput{
		DevisID:    retenu.id,
		Entreprise: in.Entreprise,
		Montant:    finance.Montant(in.MontantCentimes),
		Date:       date,
		Numero:     in.Numero,
		Notes:      in.Notes,
		By:         financeActeur(actor),
	})
	if err != nil {
		return nil, zero, h.failTool(ctx, "facture_enregistrer", err)
	}

	return nil, newFactureJSON(facture), nil
}

// acompteEnregistrerInput est l'entrée de acompte_enregistrer.
type acompteEnregistrerInput struct {
	DevisID         string `json:"devis_id,omitempty" jsonschema:"devis RETENU que l'acompte paie ; vide pour un versement hors devis"`
	Entreprise      string `json:"entreprise" jsonschema:"qui a été payé, obligatoire"`
	MontantCentimes int64  `json:"montant_centimes" jsonschema:"somme versée, en centimes d'euro entiers, strictement positive"`
	Date            string `json:"date" jsonschema:"date du versement, AAAA-MM-JJ"`
	Moyen           string `json:"moyen" jsonschema:"virement, cheque, especes ou carte"`
	Notes           string `json:"notes,omitempty"`
}

// handleAcompteEnregistrer enregistre un acompte. C'est ici que le montant
// engagé passe du domaine devis au domaine finance, EN VALEUR : l'adapter relit
// le devis retenu et transmet son montant, le domaine finance n'ira jamais le
// chercher lui-même (R1/R2) — le modèle exact de l'adapter web.
func (h *Handler) handleAcompteEnregistrer(ctx context.Context, req *sdk.CallToolRequest, in acompteEnregistrerInput) (*sdk.CallToolResult, acompteJSON, error) {
	var zero acompteJSON

	actor, err := h.requireScope(ctx, req, identity.ScopeFinanceWrite)
	if err != nil {
		return nil, zero, err
	}

	retenu, err := h.resolveRetenu(ctx, in.DevisID)
	if err != nil {
		return nil, zero, h.failTool(ctx, "acompte_enregistrer", err)
	}

	date, err := parseDate(in.Date, "du versement")
	if err != nil {
		return nil, zero, err
	}

	moyen, err := finance.NormalizeMoyenPaiement(in.Moyen)
	if err != nil {
		return nil, zero, h.failTool(ctx, "acompte_enregistrer", err)
	}

	acompte, err := h.finance.RecordAcompte(ctx, finance.AcompteInput{
		DevisID:       retenu.id,
		Entreprise:    in.Entreprise,
		Montant:       finance.Montant(in.MontantCentimes),
		Date:          date,
		Moyen:         moyen,
		Notes:         in.Notes,
		MontantEngage: finance.Montant(retenu.montant),
		By:            financeActeur(actor),
	})
	if err != nil {
		return nil, zero, h.failTool(ctx, "acompte_enregistrer", err)
	}

	return nil, newAcompteJSON(acompte), nil
}

// retenuInfo est ce que les tools financiers savent d'un devis retenu : son
// identifiant, son libellé « lot — entreprise » et son montant engagé.
type retenuInfo struct {
	id      string
	label   string
	montant devis.Montant
}

// devisRetenus rend les devis retenus avec leur libellé — la lecture transverse
// que la synthèse et le dossier d'assurance partagent, identique à celle de
// l'adapter web.
//
// Elle expose des données du domaine devis (lots, entreprises, montants
// engagés) sous le seul scope finance:read, et c'est une décision assumée : le
// montant engagé d'un devis retenu est une donnée financière au sens propre —
// c'est la ligne budgétaire du lot, celle que la synthèse rapproche du facturé
// et du payé — et l'exiger sous devis:read aussi rendrait la synthèse illisible
// à qui a précisément le droit de lire les finances. Les justificatifs, eux,
// restent derrière document:read (voir factureJustificatifs) : une pièce du
// dossier n'est pas une ligne budgétaire.
func (h *Handler) devisRetenus(ctx context.Context) ([]retenuInfo, error) {
	comparaisons, err := h.devis.Comparaisons(ctx)
	if err != nil {
		return nil, err
	}

	retenus := make([]retenuInfo, 0, len(comparaisons))
	for _, comparaison := range comparaisons {
		retenu, ok := comparaison.Retenu()
		if !ok {
			continue
		}
		retenus = append(retenus, retenuInfo{
			id:      retenu.ID.String(),
			label:   comparaison.Demande.Lot + " — " + retenu.Artisan.Entreprise,
			montant: retenu.Montant,
		})
	}

	return retenus, nil
}

// resolveRetenu relit le devis désigné et vérifie qu'il est bien RETENU. La
// question est posée au domaine devis ([devis.Service.DevisRetenu]), que
// l'adapter web interroge de la même façon : deux copies de la règle dans deux
// familles d'adapters — qui ne peuvent pas se partager de code (R4) —
// finiraient par diverger. Ne reste ici que la traduction des refus dans le
// vocabulaire d'erreur des tools.
//
// Le montant engagé, lui, reste entre les mains de l'appelant : c'est lui qui
// le transmet au domaine finance en simple valeur (R1/R2). Un identifiant vide
// est le choix « hors devis » : rien à résoudre.
func (h *Handler) resolveRetenu(ctx context.Context, devisID string) (retenuInfo, error) {
	if devisID == "" {
		return retenuInfo{}, nil
	}

	proposition, err := h.devis.DevisRetenu(ctx, devis.ID(devisID))
	switch {
	case errors.Is(err, devis.ErrUnknownDevis):
		return retenuInfo{}, fmt.Errorf("%w : %s", errDevisRattachementInconnu, devisID)
	case errors.Is(err, devis.ErrDevisNotRetenu):
		return retenuInfo{}, fmt.Errorf("%w : %s", errDevisNonRetenu, devisID)
	case err != nil:
		return retenuInfo{}, fmt.Errorf("résolution du devis de rattachement : %w", err)
	}

	return retenuInfo{id: proposition.ID.String(), montant: proposition.Montant}, nil
}

// financeActeur traduit l'identité du jeton en valeur pour le domaine finance.
func financeActeur(actor identity.Actor) finance.ActeurID {
	return finance.ActeurID(actor.UserID().String())
}
