package web

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"sort"
	"strconv"

	"github.com/Romain-Badino/Avanti/internal/devis"
	"github.com/Romain-Badino/Avanti/internal/document"
	"github.com/Romain-Badino/Avanti/internal/finance"
	"github.com/Romain-Badino/Avanti/internal/identity"
)

// Chemins du domaine finance. En français, comme toutes les URLs visibles
// d'Avanti. La page est unique — synthèse, factures, acomptes — et les actions
// postent vers des sous-chemins de la pièce qu'elles touchent.
const (
	financesPath         = "/finances"
	financesFacturesPath = "/finances/factures"
	financesAcomptesPath = "/finances/acomptes"
	financesExportPath   = "/finances/export"

	// Suffixes des transitions. « payer » n'existe que pour les factures : un
	// acompte est un règlement par nature, il n'a pas d'état de paiement.
	suffixPayer      = "/payer"
	suffixEnvoyer    = "/assurance/envoyer"
	suffixRembourser = "/assurance/rembourser"
)

// Noms des champs des formulaires du domaine. En français : visibles dans le
// HTML et dans ce qu'une personne soumet. Entreprise, montant et notes
// réutilisent les champs déjà déclarés pour les devis.
const (
	fieldDevisID          = "devis_id"
	fieldDatePiece        = "date_piece"
	fieldNumero           = "numero"
	fieldMoyen            = "moyen"
	fieldMontantRembourse = "montant_rembourse"
)

// Codes d'avis du domaine (voir avisCatalog dans devis.go).
const (
	avisFactureAjoutee      = "facture_ajoutee"
	avisAcompteAjoute       = "acompte_ajoute"
	avisFacturePayee        = "facture_payee"
	avisAssuranceEnvoyee    = "assurance_envoyee"
	avisAssuranceRemboursee = "assurance_remboursee"
	// avisPieceModifiee suit un conflit d'écriture : quelqu'un a modifié la
	// pièce entre-temps, la page rechargée montre l'état réel — le modèle de
	// deja_tranche côté devis.
	avisPieceModifiee = "piece_modifiee"
)

// Erreurs de saisie propres à l'interface des finances. Comme celles des
// montants, elles ne remontent jamais telles quelles à l'écran : chacune est
// associée à un message du catalogue.
var (
	// errFinanceDevisInconnu signale un rattachement vers un devis qui
	// n'existe pas. La vérification est celle de l'adapter web — c'est lui qui
	// assemble les vues transverses, le domaine finance ne connaît pas les
	// devis (R2).
	errFinanceDevisInconnu = errors.New("web : devis de rattachement inconnu")
	// errDevisNonRetenu signale un rattachement vers un devis qui n'est pas
	// retenu. Une facture comme un acompte se rattachent à un lot ENGAGÉ :
	// rattacher une dépense à une offre encore en comparaison — ou écartée —
	// fausserait la synthèse engagé/payé, qui n'a de sens que sur le retenu.
	errDevisNonRetenu = errors.New("web : le devis choisi n'est pas retenu")
)

// financeErrorMessages traduit les erreurs métier en messages du catalogue.
//
// Même modèle que devisErrorMessages : une erreur absente de la table n'est
// pas un refus que l'utilisateur peut corriger, c'est une panne — elle se
// journalise et s'affiche comme telle plutôt que de se déguiser en faute de
// saisie. Les erreurs de saisie de montant et de date réutilisent les messages
// du domaine devis : ce sont les mêmes textes génériques, un doublon de
// catalogue n'apprendrait rien.
var financeErrorMessages = []struct {
	err       error
	messageID string
}{
	{finance.ErrEmptyEntreprise, "devis.erreur.entreprise_vide"},
	{finance.ErrTextTooLong, "devis.erreur.texte_trop_long"},
	{finance.ErrInvalidMontant, "devis.erreur.montant_invalide"},
	{finance.ErrMissingDate, "devis.erreur.date_manquante"},
	{finance.ErrUnknownMoyenPaiement, "finance.erreur.moyen_inconnu"},
	{finance.ErrInvalidDevisID, "finance.erreur.devis_invalide"},
	{finance.ErrUnknownFacture, "finance.erreur.facture_inconnue"},
	{finance.ErrUnknownAcompte, "finance.erreur.acompte_inconnu"},
	{finance.ErrFactureAlreadyPaid, "finance.erreur.deja_payee"},
	{finance.ErrForbiddenAssuranceTransition, "finance.erreur.transition_interdite"},
	{finance.ErrInvalidRemboursement, "finance.erreur.remboursement_invalide"},
	{finance.ErrAcomptesExceedEngagement, "finance.erreur.depassement_engagement"},
	{errFinanceDevisInconnu, "finance.erreur.devis_invalide"},
	{errDevisNonRetenu, "finance.erreur.devis_non_retenu"},
	{errMontantVide, "devis.erreur.montant_vide"},
	{errMontantIllisible, "devis.erreur.montant_illisible"},
	{errMontantHorsBornes, "devis.erreur.montant_invalide"},
	{errDateIllisible, "devis.erreur.date_illisible"},
}

// financeMessageID rend l'identifiant de message correspondant à une erreur,
// ou la chaîne vide si l'erreur n'est pas un refus prévu.
func financeMessageID(err error) string {
	for _, entry := range financeErrorMessages {
		if errors.Is(err, entry.err) {
			return entry.messageID
		}
	}
	return ""
}

// mountFinance branche les routes du domaine.
//
// Chaque route est gardée par un scope, export compris : le dossier financier
// est ce que l'application a de plus confidentiel après les documents, et le
// collaborateur — dont le rôle ne porte pas finance:read — n'en voit rien.
func (h *Handler) mountFinance() {
	h.mux.HandleFunc("GET "+financesPath, h.requireScope(identity.ScopeFinanceRead, h.handleFinanceIndex))
	h.mux.HandleFunc("GET "+financesExportPath+"/{format}", h.requireScope(identity.ScopeFinanceRead, h.handleFinanceExport))

	h.mux.HandleFunc("POST "+financesFacturesPath, h.requireScope(identity.ScopeFinanceWrite, h.handleRecordFacture))
	h.mux.HandleFunc("POST "+financesAcomptesPath, h.requireScope(identity.ScopeFinanceWrite, h.handleRecordAcompte))

	h.mux.HandleFunc("POST "+financesFacturesPath+"/{id}"+suffixPayer,
		h.requireScope(identity.ScopeFinanceWrite, h.handleFacturePayer))
	h.mux.HandleFunc("POST "+financesFacturesPath+"/{id}"+suffixEnvoyer,
		h.requireScope(identity.ScopeFinanceWrite, h.handleFactureEnvoyer))
	h.mux.HandleFunc("POST "+financesFacturesPath+"/{id}"+suffixRembourser,
		h.requireScope(identity.ScopeFinanceWrite, h.handleFactureRembourser))
	h.mux.HandleFunc("POST "+financesAcomptesPath+"/{id}"+suffixEnvoyer,
		h.requireScope(identity.ScopeFinanceWrite, h.handleAcompteEnvoyer))
	h.mux.HandleFunc("POST "+financesAcomptesPath+"/{id}"+suffixRembourser,
		h.requireScope(identity.ScopeFinanceWrite, h.handleAcompteRembourser))
}

// --- Conversions de montants -------------------------------------------------

// parseMontantFinance traduit une saisie en euros vers les centimes du domaine
// finance. La mécanique est celle de parseMontant — arithmétique entière,
// jamais de flottant — et la conversion de type est sûre : les deux Montant
// sont des int64 de centimes avec la même borne.
func parseMontantFinance(raw string) (finance.Montant, error) {
	montant, err := parseMontant(raw)
	if err != nil {
		return 0, err
	}
	return finance.Montant(montant), nil
}

// formatMontantFinance écrit un montant du domaine finance en notation
// française, par le même chemin entier que les devis.
func formatMontantFinance(montant finance.Montant) string {
	return formatMontant(devis.Montant(montant))
}

// --- La page ------------------------------------------------------------------

// financeIndexData est la charge utile de la page des finances.
type financeIndexData struct {
	// Synthese est le rapprochement par devis retenu : engagé, facturé, payé,
	// remboursé, reste à payer.
	Synthese syntheseData
	// Factures et Acomptes listent les pièces, de la plus récente à la plus
	// ancienne.
	Factures []factureRow
	Acomptes []acompteRow
	// FactureForm et AcompteForm sont les formulaires de saisie, réaffichés
	// tels quels après un refus. Vides de leurs options si la personne n'a pas
	// finance:write — le gabarit ne les montre alors pas.
	FactureForm factureFormData
	AcompteForm acompteFormData
	// PieceForm est le formulaire de dépôt d'un justificatif sur une facture.
	// Construit seulement pour qui détient document:write, et seulement s'il
	// y a une facture à justifier.
	PieceForm financePieceFormData
	// Exports sont les téléchargements offerts, dans un ordre stable.
	Exports []exportLink
	// Avis est le message qui suit une action, s'il y en a un.
	Avis avisView
}

// syntheseData est le tableau de rapprochement.
type syntheseData struct {
	Lignes []syntheseLigne
	// HorsDevis est la ligne des dépenses sans devis ; absente s'il n'y en a
	// aucune.
	HorsDevis *syntheseLigne
	// Total est le total chantier, toutes pièces confondues.
	Total syntheseLigne
}

// syntheseLigne est une ligne du rapprochement, déjà mise en forme.
type syntheseLigne struct {
	Libelle   string
	Engage    string
	Facture   string
	Paye      string
	Rembourse string
	// ResteAPayer est engagé − payé ; vide quand rien n'est engagé (hors
	// devis), où la soustraction ne veut rien dire.
	ResteAPayer string
}

// factureRow est une ligne du tableau des factures.
type factureRow struct {
	Entreprise string
	Devis      string
	Numero     string
	Date       string
	Montant    string
	Notes      string
	// Pieces sont les justificatifs rattachés à la facture (cible
	// facture/{id} du domaine document), avec leur lien de téléchargement.
	// Vide sans document:read : les scopes décident de ce qui est construit.
	Pieces []pieceRow
	// Paiement et Assurance sont les libellés traduits ; les booléens disent au
	// gabarit quelles actions offrir.
	Paiement    string
	PayeeLe     string
	Assurance   string
	EnvoyeeLe   string
	Rembourse   string
	RembourseLe string

	PeutPayer      bool
	PeutEnvoyer    bool
	PeutRembourser bool
	PayerURL       string
	EnvoyerURL     string
	RembourserURL  string
}

// acompteRow est une ligne du tableau des acomptes.
type acompteRow struct {
	Entreprise  string
	Devis       string
	Date        string
	Montant     string
	Moyen       string
	Notes       string
	Assurance   string
	EnvoyeeLe   string
	Rembourse   string
	RembourseLe string

	PeutEnvoyer    bool
	PeutRembourser bool
	EnvoyerURL     string
	RembourserURL  string
}

// devisOption est une entrée du sélecteur de devis retenu des formulaires.
type devisOption struct {
	ID string
	// Label est le libellé « lot — entreprise », déjà assemblé.
	Label    string
	Selected bool
}

// moyenOption est une entrée du sélecteur de moyen de paiement.
type moyenOption struct {
	Value    string
	Label    string
	Selected bool
}

// factureFormData est le formulaire d'enregistrement d'une facture.
type factureFormData struct {
	Action     string
	Devis      []devisOption
	DevisID    string
	Entreprise string
	Montant    string
	DatePiece  string
	Numero     string
	Notes      string
	Error      string
}

// acompteFormData est le formulaire d'enregistrement d'un acompte.
type acompteFormData struct {
	Action     string
	Devis      []devisOption
	DevisID    string
	Entreprise string
	Montant    string
	DatePiece  string
	Moyens     []moyenOption
	Notes      string
	Error      string
}

// exportLink est un téléchargement offert.
type exportLink struct {
	URL   string
	Label string
}

// financePieceFormData est le formulaire de dépôt d'un justificatif sur une
// facture. Il poste vers le domaine document (POST /documents) avec un
// cible_type figé à « facture » et un cible_id choisi parmi les factures —
// le modèle exact de la section Pièces de la comparaison des devis.
type financePieceFormData struct {
	Action     string
	CibleType  string
	Factures   []factureOption
	Categories []categorieOption
}

// factureOption est une facture offerte au rattachement.
type factureOption struct {
	ID    string
	Label string
}

// retenuInfo est ce que la page et les formulaires savent d'un devis retenu :
// son libellé et son montant engagé.
type retenuInfo struct {
	id      string
	label   string
	montant devis.Montant
}

// handleFinanceIndex sert la page des finances : synthèse, factures, acomptes
// et formulaires.
func (h *Handler) handleFinanceIndex(w http.ResponseWriter, r *http.Request) {
	h.renderFinanceIndex(w, r, http.StatusOK, nil, nil, "")
}

// renderFinanceIndex assemble et rend la page, avec les formulaires dans
// l'état où l'appelant les donne — nil pour un formulaire vierge — et,
// lorsqu'une action vient d'être refusée, le message du refus en avis
// d'erreur (refusal vide sinon : l'avis vient alors de l'URL).
//
// C'est l'assemblage transverse que R2 prévoit : le domaine devis donne les
// lots retenus et leurs montants engagés, le domaine finance ses pièces et ses
// cumuls, et la composition se fait ici, dans l'adapter web.
func (h *Handler) renderFinanceIndex(
	w http.ResponseWriter, r *http.Request, status int,
	factureForm *factureFormData, acompteForm *acompteFormData, refusal string,
) {
	retenus, err := h.devisRetenus(r.Context())
	if err != nil {
		h.failFinance(w, r, fmt.Errorf("lecture des devis retenus : %w", err))
		return
	}

	totaux, err := h.finance.Totaux(r.Context())
	if err != nil {
		h.failFinance(w, r, fmt.Errorf("lecture des totaux financiers : %w", err))
		return
	}

	factures, err := h.finance.Factures(r.Context())
	if err != nil {
		h.failFinance(w, r, fmt.Errorf("lecture des factures : %w", err))
		return
	}

	acomptes, err := h.finance.Acomptes(r.Context())
	if err != nil {
		h.failFinance(w, r, fmt.Errorf("lecture des acomptes : %w", err))
		return
	}

	avis := h.avisFor(r)
	if refusal != "" {
		avis = avisView{Message: refusal, Erreur: true}
	}

	factureRows, err := h.newFactureRows(r, factures, retenus)
	if err != nil {
		h.failFinance(w, r, fmt.Errorf("lecture des justificatifs des factures : %w", err))
		return
	}

	data := financeIndexData{
		Synthese: h.newSynthese(r, retenus, totaux),
		Factures: factureRows,
		Acomptes: h.newAcompteRows(r, acomptes, retenus),
		Exports:  h.exportLinks(r),
		Avis:     avis,
	}

	// Les scopes décident de ce qui est construit, pas seulement de ce qui
	// s'affiche : sans finance:write, les formulaires restent vides et le
	// gabarit ne les montre pas — même partage que les pièces des devis. Le
	// dépôt de justificatif relève du domaine document : c'est document:write
	// qui l'ouvre, indépendamment des droits finance.
	if ActorFromContext(r.Context()).Allows(identity.ScopeFinanceWrite) {
		if factureForm == nil {
			form := h.emptyFactureForm(retenus)
			factureForm = &form
		}
		if acompteForm == nil {
			form := h.emptyAcompteForm(r, retenus)
			acompteForm = &form
		}
		data.FactureForm = *factureForm
		data.AcompteForm = *acompteForm
	}
	if ActorFromContext(r.Context()).Allows(identity.ScopeDocumentWrite) {
		data.PieceForm = h.financePieceForm(r, factures)
	}

	h.render(w, r, pageFinanceIndex, status, data)
}

// financePieceForm assemble le formulaire de dépôt d'un justificatif : les
// factures à justifier, désignées par entreprise, numéro et date.
func (h *Handler) financePieceForm(r *http.Request, factures []finance.Facture) financePieceFormData {
	form := financePieceFormData{
		Action:     documentsPath,
		CibleType:  document.TargetFacture.String(),
		Categories: h.categorieOptions(r, ""),
	}

	for _, facture := range factures {
		label := facture.Entreprise
		if facture.Numero != "" {
			label += " — " + facture.Numero
		}
		label += " — " + formatDate(facture.Date)

		form.Factures = append(form.Factures, factureOption{
			ID:    facture.ID.String(),
			Label: label,
		})
	}

	return form
}

// devisRetenus rend les devis retenus, dans l'ordre des consultations, avec le
// libellé que la page et l'export partagent.
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

// retenuLabels indexe les libellés par identifiant de devis.
func retenuLabels(retenus []retenuInfo) map[string]string {
	labels := make(map[string]string, len(retenus))
	for _, retenu := range retenus {
		labels[retenu.id] = retenu.label
	}
	return labels
}

// newSynthese assemble le rapprochement par devis retenu.
//
// Les pièces peuvent référencer un devis qui n'est plus résoluble — la
// référence est faible (R2). Ces montants-là gardent leur ligne, sous un
// libellé qui dit que le devis a disparu : les faire taire fausserait le total
// que la ligne chantier affiche.
func (h *Handler) newSynthese(r *http.Request, retenus []retenuInfo, totaux finance.Totaux) syntheseData {
	synthese := syntheseData{}
	var engageTotal devis.Montant

	seen := make(map[string]bool, len(retenus))
	for _, retenu := range retenus {
		seen[retenu.id] = true
		engageTotal += retenu.montant

		total := totaux.ParDevis[retenu.id]
		synthese.Lignes = append(synthese.Lignes, syntheseLigne{
			Libelle:   retenu.label,
			Engage:    formatMontant(retenu.montant),
			Facture:   formatMontantFinance(total.Facture),
			Paye:      formatMontantFinance(total.Paye),
			Rembourse: formatMontantFinance(total.Rembourse),
			// Le reste à payer peut être négatif — l'invariant ne borne que
			// les acomptes, pas les factures payées. C'est assumé : un lot
			// payé au-delà de l'engagé est un dépassement réel, et c'est
			// précisément ce que la synthèse doit montrer.
			ResteAPayer: formatMontantFinance(finance.Montant(retenu.montant) - total.Paye),
		})
	}

	// Les références mortes, dans un ordre stable pour que la page ne bouge
	// pas d'un rechargement à l'autre.
	orphans := make([]string, 0)
	for devisID := range totaux.ParDevis {
		if !seen[devisID] {
			orphans = append(orphans, devisID)
		}
	}
	sort.Strings(orphans)
	for _, devisID := range orphans {
		total := totaux.ParDevis[devisID]
		synthese.Lignes = append(synthese.Lignes, syntheseLigne{
			Libelle:   h.translate(r, "finance.synthese.devis_disparu"),
			Engage:    formatMontantFinance(0),
			Facture:   formatMontantFinance(total.Facture),
			Paye:      formatMontantFinance(total.Paye),
			Rembourse: formatMontantFinance(total.Rembourse),
		})
	}

	if totaux.HorsDevis != (finance.TotalFinance{}) {
		synthese.HorsDevis = &syntheseLigne{
			Libelle:   h.translate(r, "finance.synthese.hors_devis"),
			Engage:    formatMontantFinance(0),
			Facture:   formatMontantFinance(totaux.HorsDevis.Facture),
			Paye:      formatMontantFinance(totaux.HorsDevis.Paye),
			Rembourse: formatMontantFinance(totaux.HorsDevis.Rembourse),
		}
	}

	synthese.Total = syntheseLigne{
		Libelle:     h.translate(r, "finance.synthese.total"),
		Engage:      formatMontant(engageTotal),
		Facture:     formatMontantFinance(totaux.Chantier.Facture),
		Paye:        formatMontantFinance(totaux.Chantier.Paye),
		Rembourse:   formatMontantFinance(totaux.Chantier.Rembourse),
		ResteAPayer: formatMontantFinance(finance.Montant(engageTotal) - totaux.Chantier.Paye),
	}

	return synthese
}

// financePieceLabel rend le libellé du devis d'une pièce : celui du retenu
// quand il se résout, un libellé de disparition sinon, rien pour une pièce
// hors devis.
func (h *Handler) financePieceLabel(r *http.Request, devisID string, labels map[string]string) string {
	if devisID == "" {
		return ""
	}
	if label, ok := labels[devisID]; ok {
		return label
	}
	return h.translate(r, "finance.synthese.devis_disparu")
}

// newFactureRows met les factures sous leur forme d'affichage, justificatifs
// compris — une lecture par facture, sur un volume minuscule, gardée par
// document:read comme sur la page des devis.
func (h *Handler) newFactureRows(r *http.Request, factures []finance.Facture, retenus []retenuInfo) ([]factureRow, error) {
	labels := retenuLabels(retenus)
	actor := ActorFromContext(r.Context())
	canWrite := actor.Allows(identity.ScopeFinanceWrite)
	canReadDocs := actor.Allows(identity.ScopeDocumentRead)

	rows := make([]factureRow, 0, len(factures))
	for _, facture := range factures {
		suivi := facture.Assurance
		row := factureRow{
			Entreprise:  facture.Entreprise,
			Devis:       h.financePieceLabel(r, facture.DevisID, labels),
			Numero:      facture.Numero,
			Date:        formatDate(facture.Date),
			Montant:     formatMontantFinance(facture.Montant),
			Notes:       facture.Notes,
			Paiement:    h.translate(r, "finance.paiement."+facture.Paiement.String()),
			PayeeLe:     formatDate(facture.PaidAt),
			Assurance:   h.translate(r, "finance.assurance."+suivi.Statut.String()),
			EnvoyeeLe:   formatDate(suivi.SentAt),
			RembourseLe: formatDate(suivi.RefundedAt),

			PeutPayer:      canWrite && facture.Paiement == finance.PaiementImpayee,
			PeutEnvoyer:    canWrite && suivi.Statut == finance.AssuranceNonEnvoyee,
			PeutRembourser: canWrite && suivi.Statut == finance.AssuranceEnvoyee,
			PayerURL:       financePiecePath(financesFacturesPath, facture.ID, suffixPayer),
			EnvoyerURL:     financePiecePath(financesFacturesPath, facture.ID, suffixEnvoyer),
			RembourserURL:  financePiecePath(financesFacturesPath, facture.ID, suffixRembourser),
		}
		if suivi.Statut == finance.AssuranceRemboursee {
			row.Rembourse = formatMontantFinance(suivi.MontantRembourse)
		}

		if canReadDocs {
			docs, err := h.documents.DocumentsByTarget(r.Context(), document.Target{
				Type: document.TargetFacture,
				ID:   facture.ID.String(),
			})
			if err != nil {
				return nil, err
			}
			for _, doc := range docs {
				row.Pieces = append(row.Pieces, pieceRow{
					Nom:       doc.FileName,
					URL:       documentDownloadPath(doc.ID),
					Categorie: h.translate(r, "document.categorie."+doc.Category.String()),
					Taille:    h.formatTaille(r, doc.SizeBytes),
				})
			}
		}

		rows = append(rows, row)
	}

	return rows, nil
}

// newAcompteRows met les acomptes sous leur forme d'affichage.
func (h *Handler) newAcompteRows(r *http.Request, acomptes []finance.Acompte, retenus []retenuInfo) []acompteRow {
	labels := retenuLabels(retenus)
	canWrite := ActorFromContext(r.Context()).Allows(identity.ScopeFinanceWrite)

	rows := make([]acompteRow, 0, len(acomptes))
	for _, acompte := range acomptes {
		suivi := acompte.Assurance
		row := acompteRow{
			Entreprise: acompte.Entreprise,
			Devis:      h.financePieceLabel(r, acompte.DevisID, labels),
			Date:       formatDate(acompte.Date),
			Montant:    formatMontantFinance(acompte.Montant),
			Moyen:      h.translate(r, "finance.moyen."+acompte.Moyen.String()),
			Notes:      acompte.Notes,
			// L'accord suit la nature de la pièce : un acompte est envoyé,
			// une facture est envoyée — d'où un préfixe de clés distinct.
			Assurance:   h.translate(r, "finance.assurance_acompte."+suivi.Statut.String()),
			EnvoyeeLe:   formatDate(suivi.SentAt),
			RembourseLe: formatDate(suivi.RefundedAt),

			PeutEnvoyer:    canWrite && suivi.Statut == finance.AssuranceNonEnvoyee,
			PeutRembourser: canWrite && suivi.Statut == finance.AssuranceEnvoyee,
			EnvoyerURL:     financePiecePath(financesAcomptesPath, acompte.ID, suffixEnvoyer),
			RembourserURL:  financePiecePath(financesAcomptesPath, acompte.ID, suffixRembourser),
		}
		if suivi.Statut == finance.AssuranceRemboursee {
			row.Rembourse = formatMontantFinance(suivi.MontantRembourse)
		}

		rows = append(rows, row)
	}

	return rows
}

// exportLinks rend les téléchargements offerts, triés par segment d'URL pour
// un ordre d'affichage stable — une map Go n'en a pas.
func (h *Handler) exportLinks(r *http.Request) []exportLink {
	segments := make([]string, 0, len(h.exports))
	for segment := range h.exports {
		segments = append(segments, segment)
	}
	sort.Strings(segments)

	links := make([]exportLink, 0, len(segments))
	for _, segment := range segments {
		links = append(links, exportLink{
			URL:   financesExportPath + "/" + url.PathEscape(segment),
			Label: h.translate(r, "finance.export.lien", "Format", h.exports[segment].FileExtension()),
		})
	}

	return links
}

// emptyFactureForm rend un formulaire de facture vierge, daté d'aujourd'hui.
func (h *Handler) emptyFactureForm(retenus []retenuInfo) factureFormData {
	return factureFormData{
		Action:    financesFacturesPath,
		Devis:     devisOptions(retenus, ""),
		DatePiece: formatDateInput(h.now()),
	}
}

// emptyAcompteForm rend un formulaire d'acompte vierge.
func (h *Handler) emptyAcompteForm(r *http.Request, retenus []retenuInfo) acompteFormData {
	return acompteFormData{
		Action:    financesAcomptesPath,
		Devis:     devisOptions(retenus, ""),
		DatePiece: formatDateInput(h.now()),
		Moyens:    h.moyenOptions(r, ""),
	}
}

// devisOptions rend le sélecteur des devis retenus, avec le choix soumis
// resélectionné le cas échéant. Le choix « hors devis » est la première
// option, valeur vide — c'est le gabarit qui porte son libellé.
func devisOptions(retenus []retenuInfo, selected string) []devisOption {
	options := make([]devisOption, 0, len(retenus))
	for _, retenu := range retenus {
		options = append(options, devisOption{
			ID:       retenu.id,
			Label:    retenu.label,
			Selected: retenu.id == selected,
		})
	}
	return options
}

// moyenOptions rend le sélecteur de moyen de paiement.
func (h *Handler) moyenOptions(r *http.Request, selected string) []moyenOption {
	options := make([]moyenOption, 0, len(finance.AllMoyensPaiement()))
	for _, moyen := range finance.AllMoyensPaiement() {
		options = append(options, moyenOption{
			Value:    moyen.String(),
			Label:    h.translate(r, "finance.moyen."+moyen.String()),
			Selected: moyen.String() == selected,
		})
	}
	return options
}

// --- Enregistrement des pièces -----------------------------------------------

// resolveRetenu relit le devis choisi dans un formulaire et vérifie qu'il est
// bien RETENU. C'est la vérification de l'adapter — le domaine finance ne sait
// pas lire un devis (R2) — et elle rend le montant engagé que l'invariant des
// acomptes exige.
//
// Un identifiant vide est le choix « hors devis » : rien à résoudre, rien à
// reprocher, aucun montant engagé.
func (h *Handler) resolveRetenu(r *http.Request, devisID string) (retenuInfo, error) {
	if devisID == "" {
		return retenuInfo{}, nil
	}

	proposition, err := h.devis.Devis(r.Context(), devis.ID(devisID))
	if errors.Is(err, devis.ErrUnknownDevis) {
		return retenuInfo{}, fmt.Errorf("%w : %s", errFinanceDevisInconnu, devisID)
	}
	if err != nil {
		return retenuInfo{}, fmt.Errorf("résolution du devis de rattachement : %w", err)
	}
	if proposition.Statut != devis.StatutRetenu {
		return retenuInfo{}, fmt.Errorf("%w : %s", errDevisNonRetenu, devisID)
	}

	return retenuInfo{id: proposition.ID.String(), montant: proposition.Montant}, nil
}

// handleRecordFacture enregistre une facture.
func (h *Handler) handleRecordFacture(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.failFinance(w, r, fmt.Errorf("lecture du formulaire de facture : %w", err))
		return
	}

	err := h.recordFactureFromForm(r)
	if err == nil {
		h.redirectAfterPost(w, r, financesPath+"?"+paramAvis+"="+avisFactureAjoutee)
		return
	}

	h.rejectFactureForm(w, r, err)
}

// recordFactureFromForm traduit le formulaire en entrée de cas d'usage et
// l'exécute. Seules les conversions de format et la résolution du devis sont
// faites ici : la validation métier appartient au domaine.
func (h *Handler) recordFactureFromForm(r *http.Request) error {
	retenu, err := h.resolveRetenu(r, r.PostFormValue(fieldDevisID))
	if err != nil {
		return err
	}
	montant, err := parseMontantFinance(r.PostFormValue(fieldMontant))
	if err != nil {
		return err
	}
	date, err := parseDate(r.PostFormValue(fieldDatePiece))
	if err != nil {
		return err
	}

	_, err = h.finance.RecordFacture(r.Context(), finance.FactureInput{
		DevisID:    retenu.id,
		Entreprise: r.PostFormValue(fieldEntreprise),
		Montant:    montant,
		Date:       date,
		Numero:     r.PostFormValue(fieldNumero),
		Notes:      r.PostFormValue(fieldNotes),
		By:         financeActeurFrom(r),
	})

	return err
}

// handleRecordAcompte enregistre un acompte versé.
func (h *Handler) handleRecordAcompte(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.failFinance(w, r, fmt.Errorf("lecture du formulaire d'acompte : %w", err))
		return
	}

	err := h.recordAcompteFromForm(r)
	if err == nil {
		h.redirectAfterPost(w, r, financesPath+"?"+paramAvis+"="+avisAcompteAjoute)
		return
	}

	h.rejectAcompteForm(w, r, err)
}

// recordAcompteFromForm traduit le formulaire en entrée de cas d'usage. C'est
// ici que le montant engagé passe du domaine devis au domaine finance, EN
// VALEUR : l'adapter relit le devis retenu et transmet son montant, le domaine
// finance n'ira jamais le chercher lui-même (R1/R2).
func (h *Handler) recordAcompteFromForm(r *http.Request) error {
	retenu, err := h.resolveRetenu(r, r.PostFormValue(fieldDevisID))
	if err != nil {
		return err
	}
	montant, err := parseMontantFinance(r.PostFormValue(fieldMontant))
	if err != nil {
		return err
	}
	date, err := parseDate(r.PostFormValue(fieldDatePiece))
	if err != nil {
		return err
	}
	// La normalisation du domaine (casse, blancs) plutôt qu'un cast brut : la
	// saisie vient d'un select, mais un POST forgé n'en vient pas forcément.
	moyen, err := finance.NormalizeMoyenPaiement(r.PostFormValue(fieldMoyen))
	if err != nil {
		return err
	}

	_, err = h.finance.RecordAcompte(r.Context(), finance.AcompteInput{
		DevisID:       retenu.id,
		Entreprise:    r.PostFormValue(fieldEntreprise),
		Montant:       montant,
		Date:          date,
		Moyen:         moyen,
		Notes:         r.PostFormValue(fieldNotes),
		MontantEngage: finance.Montant(retenu.montant),
		By:            financeActeurFrom(r),
	})

	return err
}

// rejectFactureForm réaffiche la page avec le formulaire de facture tel qu'il
// a été soumis et le message d'échec.
func (h *Handler) rejectFactureForm(w http.ResponseWriter, r *http.Request, cause error) {
	messageID := financeMessageID(cause)
	if messageID == "" {
		h.failFinance(w, r, fmt.Errorf("enregistrement d'une facture : %w", cause))
		return
	}

	retenus, err := h.devisRetenus(r.Context())
	if err != nil {
		h.failFinance(w, r, fmt.Errorf("lecture des devis retenus : %w", err))
		return
	}

	form := h.emptyFactureForm(retenus)
	form.Devis = devisOptions(retenus, r.PostFormValue(fieldDevisID))
	form.DevisID = r.PostFormValue(fieldDevisID)
	form.Entreprise = r.PostFormValue(fieldEntreprise)
	form.Montant = r.PostFormValue(fieldMontant)
	form.DatePiece = r.PostFormValue(fieldDatePiece)
	form.Numero = r.PostFormValue(fieldNumero)
	form.Notes = r.PostFormValue(fieldNotes)
	form.Error = h.translate(r, messageID)

	h.renderFinanceIndex(w, r, http.StatusUnprocessableEntity, &form, nil, "")
}

// rejectAcompteForm réaffiche la page avec le formulaire d'acompte tel qu'il a
// été soumis et le message d'échec.
func (h *Handler) rejectAcompteForm(w http.ResponseWriter, r *http.Request, cause error) {
	messageID := financeMessageID(cause)
	if messageID == "" {
		h.failFinance(w, r, fmt.Errorf("enregistrement d'un acompte : %w", cause))
		return
	}

	retenus, err := h.devisRetenus(r.Context())
	if err != nil {
		h.failFinance(w, r, fmt.Errorf("lecture des devis retenus : %w", err))
		return
	}

	form := h.emptyAcompteForm(r, retenus)
	form.Devis = devisOptions(retenus, r.PostFormValue(fieldDevisID))
	form.DevisID = r.PostFormValue(fieldDevisID)
	form.Entreprise = r.PostFormValue(fieldEntreprise)
	form.Montant = r.PostFormValue(fieldMontant)
	form.DatePiece = r.PostFormValue(fieldDatePiece)
	form.Moyens = h.moyenOptions(r, r.PostFormValue(fieldMoyen))
	form.Notes = r.PostFormValue(fieldNotes)
	form.Error = h.translate(r, messageID)

	h.renderFinanceIndex(w, r, http.StatusUnprocessableEntity, nil, &form, "")
}

// --- Transitions ----------------------------------------------------------------

// handleFacturePayer marque une facture comme réglée.
func (h *Handler) handleFacturePayer(w http.ResponseWriter, r *http.Request) {
	h.applyFinanceTransition(w, r, finance.ErrUnknownFacture, avisFacturePayee,
		func(ctx context.Context, id finance.ID, by finance.ActeurID) error {
			_, err := h.finance.MarkFacturePayee(ctx, id, by)
			return err
		})
}

// handleFactureEnvoyer marque une facture comme transmise à l'assurance.
func (h *Handler) handleFactureEnvoyer(w http.ResponseWriter, r *http.Request) {
	h.applyFinanceTransition(w, r, finance.ErrUnknownFacture, avisAssuranceEnvoyee,
		func(ctx context.Context, id finance.ID, by finance.ActeurID) error {
			_, err := h.finance.MarkFactureEnvoyeeAssurance(ctx, id, by)
			return err
		})
}

// handleFactureRembourser marque une facture comme indemnisée du montant saisi.
func (h *Handler) handleFactureRembourser(w http.ResponseWriter, r *http.Request) {
	h.applyFinanceRemboursement(w, r, func(ctx context.Context, id finance.ID, montant finance.Montant, by finance.ActeurID) error {
		_, err := h.finance.MarkFactureRemboursee(ctx, id, montant, by)
		return err
	}, finance.ErrUnknownFacture)
}

// handleAcompteEnvoyer marque un acompte comme transmis à l'assurance.
func (h *Handler) handleAcompteEnvoyer(w http.ResponseWriter, r *http.Request) {
	h.applyFinanceTransition(w, r, finance.ErrUnknownAcompte, avisAssuranceEnvoyee,
		func(ctx context.Context, id finance.ID, by finance.ActeurID) error {
			_, err := h.finance.MarkAcompteEnvoyeAssurance(ctx, id, by)
			return err
		})
}

// handleAcompteRembourser marque un acompte comme indemnisé du montant saisi.
func (h *Handler) handleAcompteRembourser(w http.ResponseWriter, r *http.Request) {
	h.applyFinanceRemboursement(w, r, func(ctx context.Context, id finance.ID, montant finance.Montant, by finance.ActeurID) error {
		_, err := h.finance.MarkAcompteRembourse(ctx, id, montant, by)
		return err
	}, finance.ErrUnknownAcompte)
}

// applyFinanceTransition exécute une transition sans paramètre et ramène à la
// page des finances.
//
// Les transitions partagent tout sauf le cas d'usage appelé et le message
// d'issue : les écrire cinq fois ferait cinq endroits où se tromper de code de
// retour ou oublier une redirection.
func (h *Handler) applyFinanceTransition(
	w http.ResponseWriter, r *http.Request,
	notFound error, avis string,
	action func(context.Context, finance.ID, finance.ActeurID) error,
) {
	err := action(r.Context(), finance.ID(r.PathValue("id")), financeActeurFrom(r))
	h.concludeFinanceAction(w, r, err, notFound, avis)
}

// applyFinanceRemboursement exécute un remboursement : la seule transition qui
// porte une saisie — le montant remboursé.
func (h *Handler) applyFinanceRemboursement(
	w http.ResponseWriter, r *http.Request,
	action func(context.Context, finance.ID, finance.Montant, finance.ActeurID) error,
	notFound error,
) {
	if err := r.ParseForm(); err != nil {
		h.failFinance(w, r, fmt.Errorf("lecture du formulaire de remboursement : %w", err))
		return
	}

	montant, err := parseMontantFinance(r.PostFormValue(fieldMontantRembourse))
	if err == nil {
		err = action(r.Context(), finance.ID(r.PathValue("id")), montant, financeActeurFrom(r))
	}

	h.concludeFinanceAction(w, r, err, notFound, avisAssuranceRemboursee)
}

// concludeFinanceAction termine une action sur une pièce : redirection avec
// avis en cas de succès, 404 pour une pièce inconnue, page réaffichée en 422
// avec le message pour un refus que l'utilisateur peut comprendre — l'état
// montré est alors le vrai, relu après le refus.
func (h *Handler) concludeFinanceAction(w http.ResponseWriter, r *http.Request, err, notFound error, avis string) {
	switch {
	case err == nil:
		h.redirectAfterPost(w, r, financesPath+"?"+paramAvis+"="+avis)
	case errors.Is(err, notFound):
		h.handleNotFound(w, r)
	case errors.Is(err, finance.ErrConcurrentUpdate):
		// Quelqu'un a modifié la pièce entre-temps. La redirection recharge la
		// page avec l'état réel et l'avis qui l'explique — même modèle que
		// « déjà tranché » côté devis.
		h.redirectAfterPost(w, r, financesPath+"?"+paramAvis+"="+avisPieceModifiee)
	case financeMessageID(err) != "":
		// Le refus est de ceux que l'utilisateur comprend — pièce déjà payée,
		// remboursement trop grand. La page se réaffiche avec le message et
		// l'état RELU : ce qui est montré est le vrai.
		h.renderFinanceIndex(w, r, http.StatusUnprocessableEntity, nil, nil,
			h.translate(r, financeMessageID(err)))
	default:
		h.failFinance(w, r, fmt.Errorf("action sur une pièce financière : %w", err))
	}
}

// --- Export ---------------------------------------------------------------------

// handleFinanceExport rend le dossier d'assurance dans le format demandé.
//
// Le rendu passe par un tampon : un format qui échouerait à mi-écriture
// laisserait sinon un fichier tronqué servi en 200, sans moyen de le
// signaler — l'en-tête est parti. Le dossier d'un chantier tient sans peine en
// mémoire.
func (h *Handler) handleFinanceExport(w http.ResponseWriter, r *http.Request) {
	format, ok := h.exports[r.PathValue("format")]
	if !ok {
		h.handleNotFound(w, r)
		return
	}

	dossier, err := h.buildDossierAssurance(r)
	if err != nil {
		h.failFinance(w, r, fmt.Errorf("assemblage du dossier d'assurance : %w", err))
		return
	}

	var rendered bytes.Buffer
	if err := format.Write(&rendered, dossier); err != nil {
		h.failFinance(w, r, fmt.Errorf("rendu du dossier d'assurance : %w", err))
		return
	}

	// mime.FormatMediaType assemble le Content-Disposition : c'est lui qui
	// sait citer ou encoder un nom de fichier sans casser l'en-tête.
	filename := "dossier-assurance-" + h.now().UTC().Format(dateInputLayout) + "." + format.FileExtension()
	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": filename})
	if disposition == "" {
		disposition = "attachment"
	}

	w.Header().Set("Content-Type", format.ContentType())
	w.Header().Set("Content-Length", strconv.Itoa(rendered.Len()))
	w.Header().Set("Content-Disposition", disposition)
	// Le dossier est confidentiel, au même titre que les pages : aucun cache
	// intermédiaire n'a à en garder copie.
	w.Header().Set("Cache-Control", "no-store")

	if _, err := rendered.WriteTo(w); err != nil {
		// L'en-tête est parti : il ne reste que la trace au journal.
		h.fail(r, fmt.Errorf("envoi du dossier d'assurance : %w", err))
	}
}

// buildDossierAssurance assemble le dossier : les pièces du domaine finance,
// les libellés du domaine devis, les justificatifs du domaine document.
//
// C'est l'assemblage transverse que R2 prévoit, dans l'adapter web : le
// domaine finance fixe la forme du dossier (finance.DossierAssurance) et ne
// résout rien lui-même.
func (h *Handler) buildDossierAssurance(r *http.Request) (finance.DossierAssurance, error) {
	retenus, err := h.devisRetenus(r.Context())
	if err != nil {
		return finance.DossierAssurance{}, err
	}
	totaux, err := h.finance.Totaux(r.Context())
	if err != nil {
		return finance.DossierAssurance{}, err
	}
	factures, err := h.finance.Factures(r.Context())
	if err != nil {
		return finance.DossierAssurance{}, err
	}
	acomptes, err := h.finance.Acomptes(r.Context())
	if err != nil {
		return finance.DossierAssurance{}, err
	}

	labels := retenuLabels(retenus)

	var engage devis.Montant
	for _, retenu := range retenus {
		engage += retenu.montant
	}

	dossier := finance.DossierAssurance{
		GeneratedAt: h.now().UTC(),
		// L'intitulé porte l'hôte de l'instance : c'est la donnée qui
		// distingue deux chantiers quand deux dossiers se croisent.
		Intitule: h.translate(r, "finance.export.intitule", "Hote", h.baseHost),
		Totaux: finance.TotauxDossier{
			Engage:    finance.Montant(engage),
			Facture:   totaux.Chantier.Facture,
			Paye:      totaux.Chantier.Paye,
			Rembourse: totaux.Chantier.Rembourse,
		},
	}

	for _, facture := range factures {
		ligne := finance.LigneFacture{
			DevisLibelle: h.financePieceLabel(r, facture.DevisID, labels),
			Entreprise:   facture.Entreprise,
			Numero:       facture.Numero,
			Date:         facture.Date,
			Montant:      facture.Montant,
			Paiement:     facture.Paiement,
			PaidAt:       facture.PaidAt,
			Assurance:    facture.Assurance,
		}
		ligne.Pieces, err = h.factureJustificatifs(r, facture.ID)
		if err != nil {
			return finance.DossierAssurance{}, err
		}
		dossier.Factures = append(dossier.Factures, ligne)
	}

	// Les acomptes n'ont pas de justificatifs : le domaine document ne connaît
	// pas de cible « acompte » (document.TargetFacture existe, pas
	// TargetAcompte). Le jour où ce type de cible naîtra, la liste se
	// remplira ici, sans toucher au domaine finance.
	for _, acompte := range acomptes {
		dossier.Acomptes = append(dossier.Acomptes, finance.LigneAcompte{
			DevisLibelle: h.financePieceLabel(r, acompte.DevisID, labels),
			Entreprise:   acompte.Entreprise,
			Date:         acompte.Date,
			Montant:      acompte.Montant,
			Moyen:        acompte.Moyen,
			Assurance:    acompte.Assurance,
		})
	}

	return dossier, nil
}

// factureJustificatifs liste les pièces jointes d'une facture, par la cible
// facture/{id} du domaine document.
//
// La lecture n'a lieu que si la personne détient document:read : les scopes
// décident de ce qui est construit, pas seulement de ce qui s'affiche. Un
// dossier exporté sans ce scope liste les pièces financières sans leurs
// justificatifs — c'est ce que le rôle autorise, le document le reflète.
func (h *Handler) factureJustificatifs(r *http.Request, factureID finance.ID) ([]finance.PieceJointe, error) {
	if !ActorFromContext(r.Context()).Allows(identity.ScopeDocumentRead) {
		return nil, nil
	}

	docs, err := h.documents.DocumentsByTarget(r.Context(), document.Target{
		Type: document.TargetFacture,
		ID:   factureID.String(),
	})
	if err != nil {
		return nil, fmt.Errorf("lecture des justificatifs de la facture %s : %w", factureID, err)
	}

	pieces := make([]finance.PieceJointe, 0, len(docs))
	for _, doc := range docs {
		pieces = append(pieces, finance.PieceJointe{
			FileName: doc.FileName,
			Category: doc.Category.String(),
		})
	}

	return pieces, nil
}

// --- Divers ---------------------------------------------------------------------

// financeActeurFrom traduit l'identité de la requête en valeur pour le domaine
// finance — même partage que pour les devis : le domaine reçoit un identifiant
// d'acteur en simple valeur, jamais l'acteur lui-même (R1).
func financeActeurFrom(r *http.Request) finance.ActeurID {
	return finance.ActeurID(ActorFromContext(r.Context()).UserID().String())
}

// financePiecePath rend l'adresse d'une action sur une pièce.
func financePiecePath(base string, id finance.ID, suffix string) string {
	return base + "/" + url.PathEscape(id.String()) + suffix
}

// failFinance journalise une panne et sert la page d'erreur.
func (h *Handler) failFinance(w http.ResponseWriter, r *http.Request, err error) {
	h.fail(r, err)
	h.render(w, r, pageInternalError, http.StatusInternalServerError, nil)
}
