package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/Romain-Badino/Avanti/internal/devis"
	"github.com/Romain-Badino/Avanti/internal/document"
	"github.com/Romain-Badino/Avanti/internal/identity"
)

// Chemins du domaine devis. Ils sont en français, comme toutes les URLs
// visibles d'Avanti.
//
// Les décisions vivent sous /devis/propositions/ et non sous la demande : un
// devis se désigne par son seul identifiant, et imbriquer les deux
// (/devis/demandes/{id}/devis/{id}/retenir) donnerait une URL où deux
// identifiants du même type se suivent, avec l'occasion de les intervertir.
const (
	devisPath             = "/devis"
	devisDemandesPath     = "/devis/demandes"
	devisNouvelleDemande  = "/devis/demandes/nouvelle"
	devisPropositionsPath = "/devis/propositions"
)

// Noms des champs des formulaires du domaine. En français : ils sont visibles
// dans le HTML et dans ce qu'une personne soumet.
const (
	fieldLot               = "lot"
	fieldDescription       = "description"
	fieldDateEnvoi         = "date_envoi"
	fieldArtisanEntreprise = "artisan_entreprise"
	fieldArtisanEmail      = "artisan_email"
	fieldArtisanTelephone  = "artisan_telephone"

	fieldEntreprise    = "entreprise"
	fieldTelephone     = "telephone"
	fieldMontant       = "montant"
	fieldDateReception = "date_reception"
	fieldValidite      = "validite_jours"
	fieldNotes         = "notes"
)

// artisanLines est le nombre de lignes d'artisan qu'offre le formulaire de
// demande. Trois suffisent à la consultation habituelle, et les lignes vides
// sont ignorées à l'enregistrement — mieux vaut en proposer une de trop qu'une
// de moins, puisqu'il n'y a pas de JavaScript pour en ajouter.
const artisanLines = 3

// paramAvis porte, après une redirection, le message à afficher.
//
// La forme est un code court et non le message lui-même : le catalogue reste la
// seule source des textes, et une URL ne peut donc pas faire afficher une phrase
// arbitraire à quelqu'un qu'on y aurait envoyé.
const paramAvis = "avis"

// Codes d'avis reconnus.
const (
	avisDemandeCreee = "demande_creee"
	avisDevisAjoute  = "devis_ajoute"
	avisDevisRetenu  = "devis_retenu"
	avisDevisRefuse  = "devis_refuse"
	avisDejaTranche  = "deja_tranche"
)

// avisCatalog associe chaque code au message du catalogue et à sa tonalité. Un
// code absent n'affiche rien.
var avisCatalog = map[string]struct {
	messageID string
	erreur    bool
}{
	avisDemandeCreee: {messageID: "devis.avis.demande_creee"},
	avisDevisAjoute:  {messageID: "devis.avis.devis_ajoute"},
	avisDevisRetenu:  {messageID: "devis.avis.devis_retenu"},
	avisDevisRefuse:  {messageID: "devis.avis.devis_refuse"},
	avisDejaTranche:  {messageID: "devis.avis.deja_tranche", erreur: true},
	// L'avis du domaine document vit dans la même table : un dépôt rattaché à
	// un devis redirige vers la page de comparaison, qui doit savoir le lire.
	avisDocumentAjoute: {messageID: "document.avis.ajoute"},
	// Les avis du domaine finance (codes déclarés dans finance.go) : une seule
	// table pour tout le site, chaque page sait lire tous les codes.
	avisFactureAjoutee:      {messageID: "finance.avis.facture_ajoutee"},
	avisAcompteAjoute:       {messageID: "finance.avis.acompte_ajoute"},
	avisFacturePayee:        {messageID: "finance.avis.facture_payee"},
	avisAssuranceEnvoyee:    {messageID: "finance.avis.assurance_envoyee"},
	avisAssuranceRemboursee: {messageID: "finance.avis.assurance_remboursee"},
	avisPieceModifiee:       {messageID: "finance.avis.piece_modifiee", erreur: true},
	// Les avis du domaine planning (codes déclarés dans planning.go).
	avisEtapeCreee:      {messageID: "planning.avis.etape_creee"},
	avisEtapeModifiee:   {messageID: "planning.avis.etape_modifiee"},
	avisEtapeDemarree:   {messageID: "planning.avis.etape_demarree"},
	avisEtapeTerminee:   {messageID: "planning.avis.etape_terminee"},
	avisJalonCree:       {messageID: "planning.avis.jalon_cree"},
	avisJalonAtteint:    {messageID: "planning.avis.jalon_atteint"},
	avisPlanningModifie: {messageID: "planning.avis.planning_modifie", erreur: true},
}

// devisErrorMessages traduit les erreurs métier en messages du catalogue.
//
// La table est ordonnée du plus précis au plus général, et son absence de
// correspondance est significative : une erreur qui n'y figure pas n'est pas un
// refus que l'utilisateur peut corriger, c'est une panne — elle se journalise et
// s'affiche comme telle plutôt que de se déguiser en faute de saisie.
var devisErrorMessages = []struct {
	err       error
	messageID string
}{
	{devis.ErrEmptyLot, "devis.erreur.lot_vide"},
	{devis.ErrEmptyEntreprise, "devis.erreur.entreprise_vide"},
	{devis.ErrInvalidArtisanEmail, "devis.erreur.email_invalide"},
	{devis.ErrTextTooLong, "devis.erreur.texte_trop_long"},
	{devis.ErrInvalidMontant, "devis.erreur.montant_invalide"},
	{devis.ErrNegativeValidity, "devis.erreur.validite_invalide"},
	{devis.ErrMissingDate, "devis.erreur.date_manquante"},
	{devis.ErrMissingDemande, "devis.erreur.demande_inconnue"},
	{devis.ErrUnknownDemande, "devis.erreur.demande_inconnue"},
	{devis.ErrUnknownDevis, "devis.erreur.devis_inconnu"},
	{devis.ErrDemandeClosed, "devis.erreur.demande_close"},
	{devis.ErrForbiddenTransition, "devis.erreur.deja_tranche"},
	{devis.ErrDevisAlreadyDecided, "devis.erreur.deja_tranche"},
	{errMontantVide, "devis.erreur.montant_vide"},
	{errMontantIllisible, "devis.erreur.montant_illisible"},
	{errMontantHorsBornes, "devis.erreur.montant_invalide"},
	{errDateIllisible, "devis.erreur.date_illisible"},
}

// devisMessageID rend l'identifiant de message correspondant à une erreur, ou
// la chaîne vide si l'erreur n'est pas un refus prévu.
func devisMessageID(err error) string {
	for _, entry := range devisErrorMessages {
		if errors.Is(err, entry.err) {
			return entry.messageID
		}
	}
	return ""
}

// mountDevis branche les routes du domaine.
//
// Chaque route est gardée par un scope : lecture pour ce qui s'affiche,
// écriture pour ce qui change quelque chose. La garde est posée ici, à
// l'enregistrement, plutôt qu'au début de chaque gestionnaire — une route qui
// arriverait sans elle se verrait en relisant ces sept lignes.
func (h *Handler) mountDevis() {
	h.mux.HandleFunc("GET "+devisPath, h.requireScope(identity.ScopeDevisRead, h.handleDevisIndex))
	h.mux.HandleFunc("GET "+devisNouvelleDemande, h.requireScope(identity.ScopeDevisWrite, h.handleNewDemandeForm))
	h.mux.HandleFunc("POST "+devisDemandesPath, h.requireScope(identity.ScopeDevisWrite, h.handleCreateDemande))
	h.mux.HandleFunc("GET "+devisDemandesPath+"/{id}", h.requireScope(identity.ScopeDevisRead, h.handleDemande))
	h.mux.HandleFunc("POST "+devisDemandesPath+"/{id}/devis", h.requireScope(identity.ScopeDevisWrite, h.handleRecordDevis))
	h.mux.HandleFunc("POST "+devisPropositionsPath+"/{id}/retenir", h.requireScope(identity.ScopeDevisWrite, h.handleRetain))
	h.mux.HandleFunc("POST "+devisPropositionsPath+"/{id}/refuser", h.requireScope(identity.ScopeDevisWrite, h.handleReject))
}

// devisIndexData est la charge utile de la liste des consultations.
type devisIndexData struct {
	// Demandes sont les consultations, de la plus récente à la plus ancienne.
	Demandes []demandeCard
	// NouvellePath est l'adresse du formulaire de création.
	NouvellePath string
	// Avis est le message qui suit une action, s'il y en a un.
	Avis avisView
}

// demandeCard résume une consultation dans la liste.
type demandeCard struct {
	Lot         string
	Description string
	Path        string
	EnvoyeeLe   string
	// NbDevis et NbArtisans sont des chaînes : ce sont des substitutions de
	// message, et le catalogue est le seul endroit où un nombre se met en phrase.
	NbDevis     string
	NbArtisans  string
	Close       bool
	Retenu      string
	MontantBas  string
	MontantHaut string
}

// handleDevisIndex sert la liste des consultations.
func (h *Handler) handleDevisIndex(w http.ResponseWriter, r *http.Request) {
	comparaisons, err := h.devis.Comparaisons(r.Context())
	if err != nil {
		h.failPage(w, r, fmt.Errorf("lecture des demandes de devis : %w", err))
		return
	}

	cards := make([]demandeCard, 0, len(comparaisons))
	for _, comparaison := range comparaisons {
		cards = append(cards, newDemandeCard(comparaison))
	}

	h.render(w, r, pageDevisIndex, http.StatusOK, devisIndexData{
		Demandes:     cards,
		NouvellePath: devisNouvelleDemande,
		Avis:         h.avisFor(r),
	})
}

// newDemandeCard met une comparaison sous la forme que la liste affiche.
func newDemandeCard(comparaison devis.Comparaison) demandeCard {
	card := demandeCard{
		Lot:         comparaison.Demande.Lot,
		Description: comparaison.Demande.Description,
		Path:        demandePath(comparaison.Demande.ID),
		EnvoyeeLe:   formatDate(comparaison.Demande.SentAt),
		NbDevis:     strconv.Itoa(len(comparaison.Devis)),
		NbArtisans:  strconv.Itoa(len(comparaison.Demande.Artisans)),
		Close:       comparaison.Closed(),
	}

	if retenu, ok := comparaison.Retenu(); ok {
		card.Retenu = retenu.Artisan.Entreprise
	}
	// La fourchette n'a de sens qu'avec de quoi comparer : un seul devis n'a pas
	// de fourchette, il a un prix.
	if bas, ok := comparaison.MoinsDisant(); ok {
		card.MontantBas = formatMontant(bas.Montant)
		if len(comparaison.Devis) > 1 {
			card.MontantHaut = formatMontant(comparaison.Devis[len(comparaison.Devis)-1].Montant)
		}
	}

	return card
}

// demandeFormData est la charge utile du formulaire de nouvelle consultation.
type demandeFormData struct {
	Action      string
	RetourURL   string
	Lot         string
	Description string
	DateEnvoi   string
	Artisans    []artisanFormLine
	Error       string
}

// artisanFormLine est une ligne du formulaire de saisie des artisans sollicités.
type artisanFormLine struct {
	Entreprise string
	Email      string
	Telephone  string
}

// handleNewDemandeForm affiche le formulaire de nouvelle consultation.
func (h *Handler) handleNewDemandeForm(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, pageDevisNouvelleDemande, http.StatusOK, h.emptyDemandeForm())
}

// emptyDemandeForm rend un formulaire vierge, daté d'aujourd'hui : une
// consultation se saisit le jour où on l'envoie, dans la grande majorité des
// cas.
func (h *Handler) emptyDemandeForm() demandeFormData {
	return demandeFormData{
		Action:    devisDemandesPath,
		RetourURL: devisPath,
		DateEnvoi: formatDateInput(civilDay(h.now())),
		Artisans:  make([]artisanFormLine, artisanLines),
	}
}

// handleCreateDemande enregistre une nouvelle consultation.
func (h *Handler) handleCreateDemande(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.failPage(w, r, fmt.Errorf("lecture du formulaire de demande : %w", err))
		return
	}

	form := h.demandeFormFromRequest(r)

	envoi, err := parseDate(r.PostFormValue(fieldDateEnvoi))
	if err != nil {
		h.rejectDemandeForm(w, r, form, err)
		return
	}

	demande, err := h.devis.CreateDemande(r.Context(), devis.DemandeInput{
		Lot:         r.PostFormValue(fieldLot),
		Description: r.PostFormValue(fieldDescription),
		Artisans:    artisansFromRequest(r),
		SentAt:      envoi,
		By:          acteurFrom(r),
	})
	if err != nil {
		h.rejectDemandeForm(w, r, form, err)
		return
	}

	h.redirectAfterPost(w, r, demandePath(demande.ID)+"?"+paramAvis+"="+avisDemandeCreee)
}

// demandeFormFromRequest reconstruit le formulaire tel qu'il a été soumis, pour
// le réafficher sans faire retaper la saisie après un refus.
func (h *Handler) demandeFormFromRequest(r *http.Request) demandeFormData {
	form := h.emptyDemandeForm()
	form.Lot = r.PostFormValue(fieldLot)
	form.Description = r.PostFormValue(fieldDescription)
	form.DateEnvoi = r.PostFormValue(fieldDateEnvoi)

	entreprises := r.PostForm[fieldArtisanEntreprise]
	emails := r.PostForm[fieldArtisanEmail]
	telephones := r.PostForm[fieldArtisanTelephone]

	lines := max(len(entreprises), artisanLines)
	form.Artisans = make([]artisanFormLine, lines)
	for i := range lines {
		form.Artisans[i] = artisanFormLine{
			Entreprise: valueAt(entreprises, i),
			Email:      valueAt(emails, i),
			Telephone:  valueAt(telephones, i),
		}
	}

	return form
}

// artisansFromRequest lit les lignes d'artisan du formulaire. Les lignes vides
// et les doublons sont l'affaire du domaine, qui les écarte.
func artisansFromRequest(r *http.Request) []devis.Artisan {
	entreprises := r.PostForm[fieldArtisanEntreprise]
	emails := r.PostForm[fieldArtisanEmail]
	telephones := r.PostForm[fieldArtisanTelephone]

	artisans := make([]devis.Artisan, 0, len(entreprises))
	for i := range entreprises {
		artisans = append(artisans, devis.Artisan{
			Entreprise: entreprises[i],
			Email:      valueAt(emails, i),
			Telephone:  valueAt(telephones, i),
		})
	}

	return artisans
}

// rejectDemandeForm réaffiche le formulaire de consultation avec son message
// d'échec.
func (h *Handler) rejectDemandeForm(w http.ResponseWriter, r *http.Request, form demandeFormData, err error) {
	messageID := devisMessageID(err)
	if messageID == "" {
		h.failPage(w, r, fmt.Errorf("création d'une demande de devis : %w", err))
		return
	}

	form.Error = h.translate(r, messageID)

	h.render(w, r, pageDevisNouvelleDemande, http.StatusUnprocessableEntity, form)
}

// comparaisonData est la charge utile de la page de comparaison.
type comparaisonData struct {
	Demande demandeHeader
	Devis   []devisRow
	// Form est le formulaire d'ajout d'un devis reçu, réaffiché tel quel après un
	// refus de saisie.
	Form devisFormData
	// Pieces sont les pièces rattachées aux propositions, avec le formulaire
	// de dépôt qui poste vers le domaine document.
	Pieces piecesData
	// Avis est le message qui suit une action, s'il y en a un.
	Avis avisView
}

// piecesData est la section « Pièces » de la page de comparaison.
type piecesData struct {
	// Groupes rassemble les pièces par proposition ; une proposition sans
	// pièce n'y figure pas.
	Groupes []pieceGroup
	// Form est le formulaire de dépôt rattaché à une proposition. Le gabarit
	// ne l'affiche que s'il y a au moins une proposition à rattacher.
	Form pieceFormData
}

// pieceGroup est la liste des pièces d'une proposition.
type pieceGroup struct {
	Entreprise string
	Pieces     []pieceRow
}

// pieceRow est une pièce affichée, déjà traduite.
type pieceRow struct {
	Nom       string
	URL       string
	Categorie string
	Taille    string
}

// pieceFormData est le formulaire de dépôt d'une pièce sur une proposition.
// Il poste vers POST /documents avec un cible_type figé à « devis » et un
// cible_id choisi parmi les propositions.
type pieceFormData struct {
	Action       string
	CibleType    string
	Propositions []pieceCible
	Categories   []categorieOption
}

// pieceCible est une proposition offerte au rattachement.
type pieceCible struct {
	ID         string
	Entreprise string
	Montant    string
}

// demandeHeader décrit la consultation en tête de la page de comparaison.
type demandeHeader struct {
	Lot         string
	Description string
	EnvoyeeLe   string
	Artisans    []artisanFormLine
	NbDevis     string
	Close       bool
	Retenu      string
	Ecart       string
}

// devisRow est une ligne du tableau de comparaison.
type devisRow struct {
	Entreprise string
	Email      string
	Telephone  string
	Montant    string
	// Ecart est la différence avec le moins-disant, vide pour le moins-disant
	// lui-même : afficher « + 0,00 » sur la meilleure offre brouillerait la seule
	// colonne qu'on lit en diagonale.
	Ecart      string
	RecuLe     string
	ValideAu   string
	Expire     bool
	Notes      string
	Statut     string
	Libelle    string
	EnAttente  bool
	RetenirURL string
	RefuserURL string
}

// devisFormData est le formulaire d'enregistrement d'un devis reçu.
type devisFormData struct {
	Action        string
	Entreprise    string
	Email         string
	Telephone     string
	Montant       string
	DateReception string
	Validite      string
	Notes         string
	Error         string
	// Artisans propose les entreprises déjà sollicitées, pour saisir un devis
	// sans retaper une raison sociale — et sans en inventer une variante qui
	// ferait apparaître deux fois la même entreprise dans la comparaison.
	Artisans []string
}

// avisView est un message d'issue d'action, déjà traduit.
type avisView struct {
	Message string
	Erreur  bool
}

// handleDemande sert la page de comparaison d'une consultation.
func (h *Handler) handleDemande(w http.ResponseWriter, r *http.Request) {
	comparaison, err := h.devis.Compare(r.Context(), devis.ID(r.PathValue("id")))
	if errors.Is(err, devis.ErrUnknownDemande) {
		h.handleNotFound(w, r)
		return
	}
	if err != nil {
		h.failPage(w, r, fmt.Errorf("lecture d'une demande de devis : %w", err))
		return
	}

	h.renderComparaison(w, r, http.StatusOK, comparaison, h.emptyDevisForm(comparaison))
}

// renderComparaison rend la page de comparaison, avec le formulaire d'ajout
// dans l'état où l'appelant le lui donne.
func (h *Handler) renderComparaison(w http.ResponseWriter, r *http.Request, status int, comparaison devis.Comparaison, form devisFormData) {
	pieces, err := h.piecesForComparaison(r, comparaison)
	if err != nil {
		h.failPage(w, r, fmt.Errorf("lecture des pièces des propositions : %w", err))
		return
	}

	h.render(w, r, pageDevisComparaison, status, comparaisonData{
		Demande: newDemandeHeader(comparaison),
		Devis:   h.newDevisRows(r, comparaison),
		Form:    form,
		Pieces:  pieces,
		Avis:    h.avisFor(r),
	})
}

// piecesForComparaison assemble la section « Pièces » : les pièces rattachées
// à chaque proposition, et le formulaire de dépôt.
//
// Une lecture par proposition, et c'est assumé : une comparaison porte
// quelques devis, jamais des centaines — c'est l'assemblage de vue transverse
// que R2 prévoit, dans l'adapter web, en interrogeant chaque domaine.
//
// Les scopes décident de ce qui est construit, pas seulement de ce qui
// s'affiche : sans document:read, les pièces ne sont pas lues — le gabarit ne
// serait pas seul à jeter les données, elles n'auraient jamais dû sortir du
// domaine ; sans document:write, le formulaire n'est pas assemblé. Les gates
// Can du gabarit restent en place, et les deux couches disent la même chose.
func (h *Handler) piecesForComparaison(r *http.Request, comparaison devis.Comparaison) (piecesData, error) {
	actor := ActorFromContext(r.Context())
	canRead := actor.Allows(identity.ScopeDocumentRead)
	canWrite := actor.Allows(identity.ScopeDocumentWrite)

	data := piecesData{Form: pieceFormData{
		Action:    documentsPath,
		CibleType: document.TargetDevis.String(),
	}}
	if canWrite {
		data.Form.Categories = h.categorieOptions(r, "")
	}

	for _, proposition := range comparaison.Devis {
		if canWrite {
			data.Form.Propositions = append(data.Form.Propositions, pieceCible{
				ID:         proposition.ID.String(),
				Entreprise: proposition.Artisan.Entreprise,
				Montant:    formatMontant(proposition.Montant),
			})
		}
		if !canRead {
			continue
		}

		docs, err := h.documents.DocumentsByTarget(r.Context(), document.Target{
			Type: document.TargetDevis,
			ID:   proposition.ID.String(),
		})
		if err != nil {
			return piecesData{}, err
		}
		if len(docs) == 0 {
			continue
		}

		group := pieceGroup{Entreprise: proposition.Artisan.Entreprise}
		for _, doc := range docs {
			group.Pieces = append(group.Pieces, pieceRow{
				Nom:       doc.FileName,
				URL:       documentDownloadPath(doc.ID),
				Categorie: h.translate(r, "document.categorie."+doc.Category.String()),
				Taille:    h.formatTaille(r, doc.SizeBytes),
			})
		}
		data.Groupes = append(data.Groupes, group)
	}

	return data, nil
}

// newDemandeHeader met l'en-tête de la comparaison sous sa forme d'affichage.
func newDemandeHeader(comparaison devis.Comparaison) demandeHeader {
	header := demandeHeader{
		Lot:         comparaison.Demande.Lot,
		Description: comparaison.Demande.Description,
		EnvoyeeLe:   formatDate(comparaison.Demande.SentAt),
		NbDevis:     strconv.Itoa(len(comparaison.Devis)),
		Close:       comparaison.Closed(),
	}

	for _, artisan := range comparaison.Demande.Artisans {
		header.Artisans = append(header.Artisans, artisanFormLine{
			Entreprise: artisan.Entreprise,
			Email:      artisan.Email,
			Telephone:  artisan.Telephone,
		})
	}

	if retenu, ok := comparaison.Retenu(); ok {
		header.Retenu = retenu.Artisan.Entreprise
	}
	if ecart := comparaison.Ecart(); ecart > 0 {
		header.Ecart = formatMontant(ecart)
	}

	return header
}

// newDevisRows met les devis sous la forme du tableau de comparaison.
func (h *Handler) newDevisRows(r *http.Request, comparaison devis.Comparaison) []devisRow {
	translator := h.catalog.Translator(r.Header.Get("Accept-Language"))
	now := h.now()

	var reference devis.Montant
	if bas, ok := comparaison.MoinsDisant(); ok {
		reference = bas.Montant
	}

	rows := make([]devisRow, 0, len(comparaison.Devis))
	for _, proposition := range comparaison.Devis {
		row := devisRow{
			Entreprise: proposition.Artisan.Entreprise,
			Email:      proposition.Artisan.Email,
			Telephone:  proposition.Artisan.Telephone,
			Montant:    formatMontant(proposition.Montant),
			RecuLe:     formatDate(proposition.ReceivedAt),
			Expire:     proposition.Expired(now),
			Notes:      proposition.Notes,
			Statut:     proposition.Statut.String(),
			Libelle:    translator.T("devis.statut." + proposition.Statut.String()),
			EnAttente:  proposition.Statut.Pending(),
			RetenirURL: decisionPath(proposition.ID, "retenir"),
			RefuserURL: decisionPath(proposition.ID, "refuser"),
		}
		if ecart := proposition.Montant - reference; ecart > 0 {
			row.Ecart = formatMontant(ecart)
		}
		if limite, known := proposition.ValidUntil(); known {
			row.ValideAu = formatDate(limite)
		}

		rows = append(rows, row)
	}

	return rows
}

// emptyDevisForm prépare le formulaire d'ajout : daté d'aujourd'hui, et
// proposant les entreprises déjà sollicitées.
func (h *Handler) emptyDevisForm(comparaison devis.Comparaison) devisFormData {
	form := devisFormData{
		Action:        demandePath(comparaison.Demande.ID) + "/devis",
		DateReception: formatDateInput(civilDay(h.now())),
	}

	for _, artisan := range comparaison.Demande.Artisans {
		form.Artisans = append(form.Artisans, artisan.Entreprise)
	}

	return form
}

// handleRecordDevis enregistre un devis reçu.
func (h *Handler) handleRecordDevis(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.failPage(w, r, fmt.Errorf("lecture du formulaire de devis : %w", err))
		return
	}

	demandeID := devis.ID(r.PathValue("id"))

	input, err := devisInputFromRequest(r, demandeID)
	if err == nil {
		var enregistre devis.Devis
		enregistre, err = h.devis.RecordDevis(r.Context(), input)
		if err == nil {
			h.redirectAfterPost(w, r, demandePath(enregistre.DemandeID)+"?"+paramAvis+"="+avisDevisAjoute)
			return
		}
	}

	h.rejectDevisForm(w, r, demandeID, err)
}

// devisInputFromRequest traduit le formulaire en entrée de cas d'usage. Seules
// les conversions de format sont faites ici : la validation métier appartient au
// domaine, qui la fait pour tous ses appelants.
func devisInputFromRequest(r *http.Request, demandeID devis.ID) (devis.DevisInput, error) {
	montant, err := parseMontant(r.PostFormValue(fieldMontant))
	if err != nil {
		return devis.DevisInput{}, err
	}

	reception, err := parseDate(r.PostFormValue(fieldDateReception))
	if err != nil {
		return devis.DevisInput{}, err
	}

	validite, err := parseValidityDays(r.PostFormValue(fieldValidite))
	if err != nil {
		return devis.DevisInput{}, err
	}

	return devis.DevisInput{
		DemandeID: demandeID,
		Artisan: devis.Artisan{
			Entreprise: r.PostFormValue(fieldEntreprise),
			Email:      r.PostFormValue(fieldEmail),
			Telephone:  r.PostFormValue(fieldTelephone),
		},
		Montant:    montant,
		ReceivedAt: reception,
		Validity:   validite,
		Notes:      r.PostFormValue(fieldNotes),
		By:         acteurFrom(r),
	}, nil
}

// rejectDevisForm réaffiche la comparaison avec le formulaire d'ajout tel qu'il
// a été soumis et le message d'échec.
//
// La page entière est relue plutôt que le seul formulaire : le refus vient
// parfois de l'état de la consultation — un devis retenu entre-temps — et
// réafficher le tableau périmé donnerait un message incompréhensible.
func (h *Handler) rejectDevisForm(w http.ResponseWriter, r *http.Request, demandeID devis.ID, cause error) {
	messageID := devisMessageID(cause)
	if messageID == "" {
		h.failPage(w, r, fmt.Errorf("enregistrement d'un devis reçu : %w", cause))
		return
	}

	comparaison, err := h.devis.Compare(r.Context(), demandeID)
	if errors.Is(err, devis.ErrUnknownDemande) {
		h.handleNotFound(w, r)
		return
	}
	if err != nil {
		h.failPage(w, r, fmt.Errorf("lecture d'une demande de devis : %w", err))
		return
	}

	form := h.emptyDevisForm(comparaison)
	form.Entreprise = r.PostFormValue(fieldEntreprise)
	form.Email = r.PostFormValue(fieldEmail)
	form.Telephone = r.PostFormValue(fieldTelephone)
	form.Montant = r.PostFormValue(fieldMontant)
	form.DateReception = r.PostFormValue(fieldDateReception)
	form.Validite = r.PostFormValue(fieldValidite)
	form.Notes = r.PostFormValue(fieldNotes)
	form.Error = h.translate(r, messageID)

	h.renderComparaison(w, r, http.StatusUnprocessableEntity, comparaison, form)
}

// handleRetain retient un devis, ce qui refuse ses concurrents.
func (h *Handler) handleRetain(w http.ResponseWriter, r *http.Request) {
	h.decide(w, r, h.devis.Retain, avisDevisRetenu)
}

// handleReject refuse un devis sans rien retenir.
func (h *Handler) handleReject(w http.ResponseWriter, r *http.Request) {
	h.decide(w, r, h.devis.Reject, avisDevisRefuse)
}

// decide exécute une décision et ramène à la comparaison.
//
// Les deux décisions partagent tout sauf le cas d'usage appelé et le message
// d'issue : les écrire deux fois ferait deux endroits où se tromper de code de
// retour ou oublier une redirection.
func (h *Handler) decide(
	w http.ResponseWriter,
	r *http.Request,
	action func(context.Context, devis.ID, devis.ActeurID) (devis.Devis, error),
	avis string,
) {
	devisID := devis.ID(r.PathValue("id"))

	tranche, err := action(r.Context(), devisID, acteurFrom(r))
	switch {
	case err == nil:
		h.redirectAfterPost(w, r, demandePath(tranche.DemandeID)+"?"+paramAvis+"="+avis)
	case errors.Is(err, devis.ErrUnknownDevis):
		h.handleNotFound(w, r)
	case devisMessageID(err) != "":
		// La décision a échoué pour une raison que l'utilisateur peut
		// comprendre — quelqu'un a tranché avant lui, le plus souvent. La page se
		// relit avec l'avis correspondant, et l'état affiché est alors le vrai.
		h.redirectToComparaisonAfterConflict(w, r, devisID)
	default:
		h.failPage(w, r, fmt.Errorf("décision sur un devis : %w", err))
	}
}

// redirectToComparaisonAfterConflict ramène à la comparaison du devis visé
// après un refus de décision.
//
// Le devis est relu pour connaître sa demande : sans cette lecture, il faudrait
// renvoyer à la liste, où la personne perdrait de vue la comparaison qu'elle
// était en train de trancher.
func (h *Handler) redirectToComparaisonAfterConflict(w http.ResponseWriter, r *http.Request, devisID devis.ID) {
	proposition, err := h.devis.Devis(r.Context(), devisID)
	if err != nil {
		h.redirectAfterPost(w, r, devisPath+"?"+paramAvis+"="+avisDejaTranche)
		return
	}

	h.redirectAfterPost(w, r, demandePath(proposition.DemandeID)+"?"+paramAvis+"="+avisDejaTranche)
}

// redirectAfterPost ramène le navigateur sur une page après une écriture.
//
// Deux façons de le faire, pour un seul comportement visible. Sans JavaScript,
// c'est la redirection HTTP habituelle, celle qui évite qu'un rafraîchissement
// rejoue le formulaire. Avec HTMX, c'est l'en-tête HX-Redirect : la bibliothèque
// intercepte la soumission et ne suivrait pas un 303 vers une page entière — elle
// en insérerait le corps dans le fragment qu'elle attendait.
//
// Aucun des deux chemins n'est privilégié : la page fonctionne à l'identique
// sans JavaScript, HTMX ne fait qu'épargner un rechargement complet.
func (h *Handler) redirectAfterPost(w http.ResponseWriter, r *http.Request, target string) {
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", target)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	http.Redirect(w, r, target, http.StatusSeeOther)
}

// avisFor lit le code d'avis de l'URL et rend le message correspondant.
func (h *Handler) avisFor(r *http.Request) avisView {
	entry, known := avisCatalog[r.URL.Query().Get(paramAvis)]
	if !known {
		return avisView{}
	}

	return avisView{Message: h.translate(r, entry.messageID), Erreur: entry.erreur}
}

// translate rend un message du catalogue dans la langue de la requête.
func (h *Handler) translate(r *http.Request, messageID string, pairs ...string) string {
	return h.catalog.Translator(r.Header.Get("Accept-Language")).T(messageID, pairs...)
}

// acteurFrom traduit l'identité de la requête en valeur pour le domaine.
//
// C'est ici, et nulle part ailleurs, que les deux mondes se touchent : le
// domaine devis n'importe pas identity (R1 de docs/ARCHITECTURE.md), il reçoit
// un identifiant d'acteur en simple valeur, pour la traçabilité.
func acteurFrom(r *http.Request) devis.ActeurID {
	return devis.ActeurID(ActorFromContext(r.Context()).UserID().String())
}

// demandePath rend l'adresse de la page de comparaison d'une demande.
func demandePath(id devis.ID) string {
	return devisDemandesPath + "/" + url.PathEscape(id.String())
}

// decisionPath rend l'adresse d'une décision sur un devis.
func decisionPath(id devis.ID, action string) string {
	return devisPropositionsPath + "/" + url.PathEscape(id.String()) + "/" + action
}

// valueAt lit la i-ème valeur d'une liste de champs de formulaire, ou la chaîne
// vide. Les navigateurs n'envoient pas toujours autant de valeurs que de
// colonnes — un champ vide en fin de formulaire peut manquer.
func valueAt(values []string, i int) string {
	if i >= len(values) {
		return ""
	}
	return values[i]
}

// now rend l'heure courante selon l'horloge du gestionnaire.
func (h *Handler) now() time.Time {
	if h.clock == nil {
		return time.Now()
	}
	return h.clock()
}
