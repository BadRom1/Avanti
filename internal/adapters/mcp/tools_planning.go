package mcp

import (
	"context"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Romain-Badino/Avanti/internal/identity"
	"github.com/Romain-Badino/Avanti/internal/planning"
)

// etapeJSON est une étape du planning, telle que les tools la rendent. Le
// statut et le retard sont DÉRIVÉS des dates réelles au moment de la lecture,
// jamais stockés — la décision structurante du domaine planning.
type etapeJSON struct {
	ID          string   `json:"id" jsonschema:"identifiant de l'étape"`
	Nom         string   `json:"nom" jsonschema:"nom du lot de travaux"`
	Description string   `json:"description,omitempty"`
	DebutPrevu  string   `json:"debut_prevu" jsonschema:"début prévu, AAAA-MM-JJ"`
	FinPrevue   string   `json:"fin_prevue" jsonschema:"fin prévue, AAAA-MM-JJ"`
	DebutReel   string   `json:"debut_reel,omitempty" jsonschema:"début réel, vide tant que l'étape n'a pas démarré"`
	FinReelle   string   `json:"fin_reelle,omitempty" jsonschema:"fin réelle, vide tant que l'étape n'est pas terminée"`
	Statut      string   `json:"statut" jsonschema:"prevue, en_cours ou terminee — dérivé des dates réelles"`
	EnRetard    bool     `json:"en_retard" jsonschema:"vrai si l'étape est en retard sur son prévu au jour de la lecture"`
	RetardJours int      `json:"retard_jours,omitempty" jsonschema:"jours de retard constatés ; zéro quand en_retard est faux"`
	DependDe    []string `json:"depend_de,omitempty" jsonschema:"identifiants des étapes prérequises, à terminer avant de démarrer celle-ci"`
	DevisID     string   `json:"devis_id,omitempty" jsonschema:"devis retenu qui finance l'étape ; vide pour un lot sans financement rattaché"`
}

func newEtapeJSON(etape planning.Etape, today time.Time) etapeJSON {
	out := etapeJSON{
		ID:          etape.ID.String(),
		Nom:         etape.Name,
		Description: etape.Description,
		DebutPrevu:  formatDate(etape.PlannedStart),
		FinPrevue:   formatDate(etape.PlannedEnd),
		DebutReel:   formatInstant(etape.ActualStart),
		FinReelle:   formatInstant(etape.ActualEnd),
		Statut:      etape.Statut().String(),
		EnRetard:    etape.EnRetard(today),
		RetardJours: etape.RetardConstate(today),
		DevisID:     etape.DevisID,
	}
	for _, dep := range etape.DependsOn {
		out.DependDe = append(out.DependDe, dep.String())
	}
	return out
}

// planningEtapesResult est la sortie de planning_etapes.
type planningEtapesResult struct {
	Etapes []etapeJSON `json:"etapes" jsonschema:"toutes les étapes, triées par début prévu"`
}

func (h *Handler) handlePlanningEtapes(ctx context.Context, req *sdk.CallToolRequest, _ struct{}) (*sdk.CallToolResult, planningEtapesResult, error) {
	// Le jour de lecture est fixé avant l'appel et capturé par la mise en
	// forme : [readList] ne passe que l'élément, et toutes les étapes d'une même
	// réponse doivent dériver leur statut du MÊME jour.
	today := h.clock().UTC()

	etapes, err := readList(ctx, h, req, "planning_etapes", identity.ScopePlanningRead,
		h.planning.Etapes, func(etape planning.Etape) etapeJSON {
			return newEtapeJSON(etape, today)
		})
	if err != nil {
		return nil, planningEtapesResult{}, err
	}

	return nil, planningEtapesResult{Etapes: etapes}, nil
}

// jalonJSON est un jalon contractuel, tel que les tools le rendent.
type jalonJSON struct {
	ID          string `json:"id" jsonschema:"identifiant du jalon"`
	Nom         string `json:"nom" jsonschema:"intitulé du jalon"`
	Date        string `json:"date" jsonschema:"échéance prévue, AAAA-MM-JJ"`
	Atteint     bool   `json:"atteint" jsonschema:"vrai quand le jalon a été atteint"`
	AtteintLe   string `json:"atteint_le,omitempty" jsonschema:"date d'atteinte, vide tant que le jalon ne l'est pas"`
	EnRetard    bool   `json:"en_retard" jsonschema:"vrai si le jalon n'est pas atteint alors que son échéance est passée"`
	RetardJours int    `json:"retard_jours,omitempty" jsonschema:"jours de retard constatés ; zéro quand en_retard est faux"`
}

func newJalonJSON(jalon planning.Jalon, today time.Time) jalonJSON {
	return jalonJSON{
		ID:          jalon.ID.String(),
		Nom:         jalon.Name,
		Date:        formatDate(jalon.Date),
		Atteint:     jalon.Atteint(),
		AtteintLe:   formatInstant(jalon.ReachedAt),
		EnRetard:    jalon.EnRetard(today),
		RetardJours: jalon.RetardConstate(today),
	}
}

// planningJalonsResult est la sortie de planning_jalons.
type planningJalonsResult struct {
	Jalons []jalonJSON `json:"jalons" jsonschema:"tous les jalons, triés par échéance"`
}

func (h *Handler) handlePlanningJalons(ctx context.Context, req *sdk.CallToolRequest, _ struct{}) (*sdk.CallToolResult, planningJalonsResult, error) {
	// Même capture du jour de lecture que planning_etapes.
	today := h.clock().UTC()

	jalons, err := readList(ctx, h, req, "planning_jalons", identity.ScopePlanningRead,
		h.planning.Jalons, func(jalon planning.Jalon) jalonJSON {
			return newJalonJSON(jalon, today)
		})
	if err != nil {
		return nil, planningJalonsResult{}, err
	}

	return nil, planningJalonsResult{Jalons: jalons}, nil
}

// etapeTransitionInput est l'entrée de etape_demarrer et etape_terminer.
type etapeTransitionInput struct {
	EtapeID string `json:"etape_id" jsonschema:"identifiant de l'étape"`
}

func (h *Handler) handleEtapeDemarrer(ctx context.Context, req *sdk.CallToolRequest, in etapeTransitionInput) (*sdk.CallToolResult, etapeJSON, error) {
	return h.applyEtapeTransition(ctx, req, "etape_demarrer", in.EtapeID, h.planning.StartEtape)
}

func (h *Handler) handleEtapeTerminer(ctx context.Context, req *sdk.CallToolRequest, in etapeTransitionInput) (*sdk.CallToolResult, etapeJSON, error) {
	return h.applyEtapeTransition(ctx, req, "etape_terminer", in.EtapeID, h.planning.FinishEtape)
}

// applyEtapeTransition exécute une transition d'étape : relecture, puis cas
// d'usage sous garde optimiste.
//
// Le tool n'exige pas d'horodatage du client : un agent n'a pas de formulaire à
// état, et lui demander de recopier un UpdatedAt reviendrait à lui faire jouer
// la garde sans en avoir la mémoire. L'étape est donc RELUE ici et son
// UpdatedAt relu passé en « expected » : la garde ne protège plus le trajet
// formulaire→soumission — il n'existe pas sur ce canal — mais reste entière
// entre cette relecture et l'écriture, où deux transitions simultanées se
// départagent comme sur le web (la seconde reçoit un refus explicable).
func (h *Handler) applyEtapeTransition(
	ctx context.Context, req *sdk.CallToolRequest, tool, etapeID string,
	action func(context.Context, planning.ID, time.Time, planning.ActeurID) (planning.Etape, error),
) (*sdk.CallToolResult, etapeJSON, error) {
	var zero etapeJSON

	actor, err := h.requireScope(ctx, req, identity.ScopePlanningWrite)
	if err != nil {
		return nil, zero, err
	}

	current, err := h.planning.Etape(ctx, planning.ID(etapeID))
	if err != nil {
		return nil, zero, h.failTool(ctx, tool, err)
	}

	updated, err := action(ctx, current.ID, current.UpdatedAt, planningActeur(actor))
	if err != nil {
		return nil, zero, h.failTool(ctx, tool, err)
	}

	return nil, newEtapeJSON(updated, h.clock().UTC()), nil
}

// planningActeur traduit l'identité du jeton en valeur pour le domaine
// planning.
func planningActeur(actor identity.Actor) planning.ActeurID {
	return planning.ActeurID(actor.UserID().String())
}
