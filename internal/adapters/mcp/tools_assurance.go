package mcp

import (
	"context"
	"fmt"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Romain-Badino/Avanti/internal/document"
	"github.com/Romain-Badino/Avanti/internal/finance"
	"github.com/Romain-Badino/Avanti/internal/identity"
)

// avertissementAssurance dit, dans la réponse elle-même, ce que ce tool ne fait
// pas. C'est la décision de cadrage de la feuille de route : toute transmission
// reste confirmée par un humain, Avanti n'a aucun port d'envoi en V1.
const avertissementAssurance = "Préparation seulement — aucun envoi n'est effectué par Avanti ; " +
	"la transmission à l'assurance reste un geste humain."

// pieceJointeJSON est un justificatif rattaché à une ligne du dossier.
type pieceJointeJSON struct {
	NomFichier string `json:"nom_fichier" jsonschema:"nom de fichier de la pièce"`
	Categorie  string `json:"categorie" jsonschema:"classement de la pièce dans le dossier"`
}

// ligneFactureJSON est une facture du dossier d'assurance.
type ligneFactureJSON struct {
	DevisLibelle string            `json:"devis_libelle,omitempty" jsonschema:"lot et entreprise du devis rattaché ; vide pour une dépense hors devis"`
	Facture      factureJSON       `json:"facture"`
	Pieces       []pieceJointeJSON `json:"pieces,omitempty" jsonschema:"justificatifs rattachés à la facture"`
}

// ligneAcompteJSON est un acompte du dossier d'assurance.
type ligneAcompteJSON struct {
	DevisLibelle string      `json:"devis_libelle,omitempty" jsonschema:"lot et entreprise du devis rattaché ; vide pour un versement hors devis"`
	Acompte      acompteJSON `json:"acompte"`
}

// totauxDossierJSON sont les cumuls du chantier, en centimes.
type totauxDossierJSON struct {
	EngageCentimes    int64 `json:"engage_centimes" jsonschema:"cumul des montants des devis retenus"`
	FactureCentimes   int64 `json:"facture_centimes" jsonschema:"cumul des factures"`
	PayeCentimes      int64 `json:"paye_centimes" jsonschema:"cumul de ce qui est sorti"`
	RembourseCentimes int64 `json:"rembourse_centimes" jsonschema:"cumul des indemnités reçues"`
}

// dossierAssuranceResult est la sortie de assurance_preparer_envoi.
type dossierAssuranceResult struct {
	Avertissement string             `json:"avertissement" jsonschema:"rappel que rien n'est envoyé : la transmission reste un geste humain"`
	Intitule      string             `json:"intitule" jsonschema:"nom du dossier, portant l'hôte de l'instance"`
	GenereLe      string             `json:"genere_le" jsonschema:"date de génération, AAAA-MM-JJ"`
	Factures      []ligneFactureJSON `json:"factures"`
	Acomptes      []ligneAcompteJSON `json:"acomptes"`
	Totaux        totauxDossierJSON  `json:"totaux"`
}

// handleAssurancePreparerEnvoi assemble le dossier d'assurance — les MÊMES
// données que l'export web (buildDossierAssurance de l'adapter web) : lignes,
// totaux, justificatifs des factures — et le rend en JSON, sans rien envoyer.
//
// L'assemblage est transverse (R2) : les libellés et montants engagés viennent
// du domaine devis, les pièces du domaine finance, les justificatifs du domaine
// document — lus seulement si le jeton porte document:read : les scopes
// décident de ce qui est construit, pas seulement de ce qui s'affiche.
func (h *Handler) handleAssurancePreparerEnvoi(ctx context.Context, req *sdk.CallToolRequest, _ struct{}) (*sdk.CallToolResult, dossierAssuranceResult, error) {
	var zero dossierAssuranceResult

	actor, err := h.requireScope(ctx, req, identity.ScopeFinanceRead)
	if err != nil {
		return nil, zero, err
	}

	dossier, err := h.buildDossier(ctx, actor)
	if err != nil {
		return nil, zero, h.failTool(ctx, "assurance_preparer_envoi", err)
	}

	return nil, dossier, nil
}

// buildDossier lit les quatre sources et compose le dossier.
func (h *Handler) buildDossier(ctx context.Context, actor identity.Actor) (dossierAssuranceResult, error) {
	retenus, err := h.devisRetenus(ctx)
	if err != nil {
		return dossierAssuranceResult{}, err
	}
	totaux, err := h.finance.Totaux(ctx)
	if err != nil {
		return dossierAssuranceResult{}, err
	}
	factures, err := h.finance.Factures(ctx)
	if err != nil {
		return dossierAssuranceResult{}, err
	}
	acomptes, err := h.finance.Acomptes(ctx)
	if err != nil {
		return dossierAssuranceResult{}, err
	}

	labels := make(map[string]string, len(retenus))
	var engage int64
	for _, retenu := range retenus {
		labels[retenu.id] = retenu.label
		engage += int64(retenu.montant)
	}

	dossier := dossierAssuranceResult{
		Avertissement: avertissementAssurance,
		Intitule:      "Dossier d'assurance — " + h.baseHost,
		GenereLe:      formatDate(h.clock().UTC()),
		Factures:      make([]ligneFactureJSON, 0, len(factures)),
		Acomptes:      make([]ligneAcompteJSON, 0, len(acomptes)),
		Totaux: totauxDossierJSON{
			EngageCentimes:    engage,
			FactureCentimes:   int64(totaux.Chantier.Facture),
			PayeCentimes:      int64(totaux.Chantier.Paye),
			RembourseCentimes: int64(totaux.Chantier.Rembourse),
		},
	}

	for _, facture := range factures {
		ligne := ligneFactureJSON{
			DevisLibelle: labels[facture.DevisID],
			Facture:      newFactureJSON(facture),
		}
		ligne.Pieces, err = h.factureJustificatifs(ctx, actor, facture.ID)
		if err != nil {
			return dossierAssuranceResult{}, err
		}
		dossier.Factures = append(dossier.Factures, ligne)
	}

	// Les acomptes n'ont pas de justificatifs : le domaine document ne connaît
	// pas de cible « acompte » — même limite que l'export web.
	for _, acompte := range acomptes {
		dossier.Acomptes = append(dossier.Acomptes, ligneAcompteJSON{
			DevisLibelle: labels[acompte.DevisID],
			Acompte:      newAcompteJSON(acompte),
		})
	}

	return dossier, nil
}

// factureJustificatifs liste les pièces jointes d'une facture, seulement si le
// jeton porte document:read : un dossier préparé sans ce scope liste les pièces
// financières sans leurs justificatifs — c'est ce que le jeton autorise, la
// réponse le reflète.
func (h *Handler) factureJustificatifs(ctx context.Context, actor identity.Actor, factureID finance.ID) ([]pieceJointeJSON, error) {
	if !actor.Allows(identity.ScopeDocumentRead) {
		return nil, nil
	}

	docs, err := h.documents.DocumentsByTarget(ctx, document.Target{
		Type: document.TargetFacture,
		ID:   factureID.String(),
	})
	if err != nil {
		return nil, fmt.Errorf("lecture des justificatifs de la facture %s : %w", factureID, err)
	}

	pieces := make([]pieceJointeJSON, 0, len(docs))
	for _, doc := range docs {
		pieces = append(pieces, pieceJointeJSON{
			NomFichier: doc.FileName,
			Categorie:  doc.Category.String(),
		})
	}

	return pieces, nil
}
