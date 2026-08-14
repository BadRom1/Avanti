package mcp

import (
	"context"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Romain-Badino/Avanti/internal/identity"
)

// addTools enregistre les tools du serveur, domaine par domaine.
//
// Les noms et descriptions sont en français : c'est l'user-visible de ce canal,
// lu par l'agent pour choisir quoi appeler — le pendant des routes et libellés
// de l'adapter web. Les schémas d'entrée et de sortie sont inférés des types Go
// par le SDK, descriptions de champs comprises (tags jsonschema).
func (h *Handler) addTools(server *sdk.Server) {
	// Consultation — scopes :read.
	sdk.AddTool(server, &sdk.Tool{
		Name: "devis_liste",
		Description: "Liste toutes les consultations d'artisans : chaque demande avec ses devis " +
			"reçus, leurs statuts (recu, retenu, refuse), leurs montants en centimes et l'écart " +
			"entre la plus haute et la plus basse offre. Scope requis : devis:read.",
	}, h.handleDevisListe)
	sdk.AddTool(server, &sdk.Tool{
		Name: "devis_detail",
		Description: "Rend une demande de devis et ses devis reçus, triés du moins-disant au " +
			"plus-disant. Scope requis : devis:read.",
	}, h.handleDevisDetail)
	sdk.AddTool(server, &sdk.Tool{
		Name: "finances_synthese",
		Description: "Rend le rapprochement financier du chantier : par devis retenu " +
			"(engagé, facturé, payé, remboursé, reste à payer), les pièces hors devis, et le " +
			"total chantier. Montants en centimes. Scope requis : finance:read.",
	}, h.handleFinancesSynthese)
	sdk.AddTool(server, &sdk.Tool{
		Name: "finances_factures",
		Description: "Liste les factures du chantier : montant, règlement, suivi d'assurance. " +
			"Scope requis : finance:read.",
	}, h.handleFinancesFactures)
	sdk.AddTool(server, &sdk.Tool{
		Name: "finances_acomptes",
		Description: "Liste les acomptes versés : montant, moyen de paiement, suivi d'assurance. " +
			"Scope requis : finance:read.",
	}, h.handleFinancesAcomptes)
	sdk.AddTool(server, &sdk.Tool{
		Name: "planning_etapes",
		Description: "Liste les étapes du chantier : dates prévues et réelles, statut dérivé " +
			"(prevue, en_cours, terminee), retard constaté en jours, prérequis. " +
			"Scope requis : planning:read.",
	}, h.handlePlanningEtapes)
	sdk.AddTool(server, &sdk.Tool{
		Name: "planning_jalons",
		Description: "Liste les jalons contractuels du chantier : échéance, atteinte, retard " +
			"constaté en jours. Scope requis : planning:read.",
	}, h.handlePlanningJalons)
	sdk.AddTool(server, &sdk.Tool{
		Name: "documents_liste",
		Description: "Liste les métadonnées des pièces du dossier : nom, type, taille, catégorie, " +
			"rattachement. Le contenu binaire ne passe jamais par MCP — il se télécharge par " +
			"l'interface web. Scope requis : document:read.",
	}, h.handleDocumentsListe)

	// Écriture — scopes :write.
	sdk.AddTool(server, &sdk.Tool{
		Name: "devis_enregistrer",
		Description: "Enregistre un devis reçu sur une demande existante et encore ouverte. " +
			"Scope requis : devis:write.",
	}, h.handleDevisEnregistrer)
	sdk.AddTool(server, &sdk.Tool{
		Name: "facture_enregistrer",
		Description: "Enregistre une facture reçue, rattachée à un devis RETENU (devis_id) ou " +
			"hors devis (devis_id vide). Scope requis : finance:write.",
	}, h.handleFactureEnregistrer)
	sdk.AddTool(server, &sdk.Tool{
		Name: "acompte_enregistrer",
		Description: "Enregistre un acompte versé, rattaché à un devis RETENU (devis_id) ou hors " +
			"devis (devis_id vide). Le cumul des acomptes d'un devis ne peut pas dépasser son " +
			"montant engagé. Scope requis : finance:write.",
	}, h.handleAcompteEnregistrer)
	sdk.AddTool(server, &sdk.Tool{
		Name: "etape_demarrer",
		Description: "Démarre une étape du planning, à condition que tous ses prérequis soient " +
			"terminés. Scope requis : planning:write.",
	}, h.handleEtapeDemarrer)
	sdk.AddTool(server, &sdk.Tool{
		Name:        "etape_terminer",
		Description: "Termine une étape démarrée du planning. Scope requis : planning:write.",
	}, h.handleEtapeTerminer)

	// Préparation d'envoi — consultation transverse.
	sdk.AddTool(server, &sdk.Tool{
		Name: "assurance_preparer_envoi",
		Description: "Assemble le dossier d'assurance (factures, acomptes, justificatifs, totaux) " +
			"— PRÉPARATION SEULEMENT : aucun envoi n'est effectué par Avanti, la transmission à " +
			"l'assurance reste un geste humain. Scope requis : finance:read.",
	}, h.handleAssurancePreparerEnvoi)
}

// readList factorise les tools de consultation en liste : garde par scope,
// lecture du domaine, mise en forme élément par élément. Les quatre tools qui
// l'utilisent ne diffèrent que par ces trois choses — les écrire quatre fois
// ferait quatre endroits où oublier la garde.
func readList[D, J any](
	ctx context.Context, h *Handler, req *sdk.CallToolRequest, tool string, scope identity.Scope,
	read func(context.Context) ([]D, error), shape func(D) J,
) ([]J, error) {
	if _, err := h.requireScope(ctx, req, scope); err != nil {
		return nil, err
	}

	items, err := read(ctx)
	if err != nil {
		return nil, h.failTool(ctx, tool, err)
	}

	out := make([]J, 0, len(items))
	for _, item := range items {
		out = append(out, shape(item))
	}

	return out, nil
}
