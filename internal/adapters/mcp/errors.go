package mcp

import (
	"context"
	"errors"
	"log/slog"

	"github.com/Romain-Badino/Avanti/internal/devis"
	"github.com/Romain-Badino/Avanti/internal/finance"
	"github.com/Romain-Badino/Avanti/internal/planning"
)

// errInternal est la seule chose qu'une panne technique laisse voir au client
// MCP. Le détail — requête SQL, chemin de fichier — est journalisé côté
// serveur : il ne renseigne que qui exploite l'instance.
var errInternal = errors.New("erreur interne du serveur")

// Refus propres à cet adapter : la résolution du devis de rattachement d'une
// facture ou d'un acompte, que le domaine finance ne sait pas faire (R2 —
// c'est l'adapter appelant qui assemble les vues transverses, comme le fait
// l'adapter web).
var (
	errDevisRattachementInconnu = errors.New("le devis de rattachement est inconnu")
	errDevisNonRetenu           = errors.New("le devis de rattachement n'est pas retenu — " +
		"une facture ou un acompte se rattache à un lot engagé")
)

// businessErrors énumère les erreurs métier des domaines que les tools
// remontent au client MCP avec leur message français : ce sont des refus
// explicables, que l'agent peut comprendre et corriger.
//
// C'est le modèle des tables *ErrorMessages de l'adapter web, en plus simple :
// pas de catalogue i18n ici, les messages des domaines sont déjà en français et
// c'est ce que ce canal parle. Une erreur ABSENTE de cette liste est une panne :
// elle se journalise et devient [errInternal].
var businessErrors = []error{
	// devis
	devis.ErrEmptyLot,
	devis.ErrTextTooLong,
	devis.ErrEmptyEntreprise,
	devis.ErrInvalidArtisanEmail,
	devis.ErrInvalidMontant,
	devis.ErrMissingDate,
	devis.ErrNegativeValidity,
	devis.ErrMissingDemande,
	devis.ErrUnknownDemande,
	devis.ErrUnknownDevis,
	devis.ErrForbiddenTransition,
	devis.ErrDemandeClosed,
	devis.ErrDevisAlreadyDecided,

	// finance
	finance.ErrEmptyEntreprise,
	finance.ErrTextTooLong,
	finance.ErrInvalidMontant,
	finance.ErrMissingDate,
	finance.ErrInvalidDevisID,
	finance.ErrUnknownMoyenPaiement,
	finance.ErrUnknownFacture,
	finance.ErrUnknownAcompte,
	finance.ErrConcurrentUpdate,
	finance.ErrFactureAlreadyPaid,
	finance.ErrForbiddenAssuranceTransition,
	finance.ErrInvalidRemboursement,
	finance.ErrMissingEngagement,
	finance.ErrAcomptesExceedEngagement,

	// planning
	planning.ErrEmptyName,
	planning.ErrTextTooLong,
	planning.ErrMissingDate,
	planning.ErrInvalidPlannedRange,
	planning.ErrUnknownEtape,
	planning.ErrUnknownJalon,
	planning.ErrConcurrentUpdate,
	planning.ErrEtapeAlreadyStarted,
	planning.ErrEtapeNotStarted,
	planning.ErrEtapeAlreadyFinished,
	planning.ErrFinishBeforeStart,
	planning.ErrPrerequisitesNotDone,

	// adapter
	errDevisRattachementInconnu,
	errDevisNonRetenu,
}

// failTool trie ce qu'un tool rend au client : un refus métier repart tel quel,
// message français compris ; tout le reste est une panne qui se journalise et
// devient [errInternal].
func (h *Handler) failTool(ctx context.Context, tool string, err error) error {
	for _, known := range businessErrors {
		if errors.Is(err, known) {
			return err
		}
	}

	h.logger.ErrorContext(ctx, "panne d'un tool MCP",
		slog.String("tool", tool),
		slog.String("error", err.Error()))

	return errInternal
}
