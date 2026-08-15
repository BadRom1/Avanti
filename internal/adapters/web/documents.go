package web

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"

	"github.com/Romain-Badino/Avanti/internal/devis"
	"github.com/Romain-Badino/Avanti/internal/document"
	"github.com/Romain-Badino/Avanti/internal/finance"
	"github.com/Romain-Badino/Avanti/internal/identity"
	"github.com/Romain-Badino/Avanti/internal/planning"
)

// Chemins du domaine document. En français, comme toutes les URLs visibles
// d'Avanti.
const (
	documentsPath = "/documents"
	// documentDownloadSuffix termine l'URL de téléchargement d'une pièce.
	documentDownloadSuffix = "/telecharger"
)

// Noms des champs du formulaire de téléversement. En français : ils sont
// visibles dans le HTML et dans ce qu'une personne soumet. Le champ
// description est partagé avec les formulaires de devis (fieldDescription).
const (
	fieldFichier   = "fichier"
	fieldCategorie = "categorie"
	fieldCibleType = "cible_type"
	fieldCibleID   = "cible_id"
)

// avisDocumentAjoute est le code d'avis d'un dépôt réussi (voir avisCatalog).
const avisDocumentAjoute = "document_ajoute"

// uploadOverheadBytes est la marge accordée à la requête de téléversement
// au-delà de [document.MaxFileSize] : le corps multipart transporte, en plus
// du fichier, les délimiteurs de parties, les en-têtes de chaque champ et les
// champs eux-mêmes (catégorie, description jusqu'à 2000 caractères, cible).
// Un mébioctet couvre tout cela avec une aisance ridicule, tout en gardant le
// plafond de la requête du même ordre que celui du fichier.
//
// La conséquence est un double seuil, assumé et testé : un fichier au-delà de
// 25 Mio mais dont la requête tient sous le plafond est refusé proprement par
// le domaine (422, message traduit) ; une requête qui dépasse le plafond est
// coupée par http.MaxBytesReader avant d'être lue en entier (413) — on ne
// dépense pas 100 Mo de bande passante pour formuler poliment un refus.
const uploadOverheadBytes = 1 << 20

// uploadMemoryBytes est le seuil au-delà duquel les parties du multipart
// débordent en fichiers temporaires plutôt qu'en mémoire.
const uploadMemoryBytes = 4 << 20

// Erreurs de saisie propres à l'interface de téléversement. Comme celles des
// montants, elles ne remontent jamais telles quelles à l'écran : chacune est
// associée à un message du catalogue.
var (
	// errFichierManquant signale un formulaire sans partie fichier.
	errFichierManquant = errors.New("web : fichier absent du téléversement")
	// errCibleDevisInconnue signale un rattachement vers un devis qui n'existe
	// pas. La vérification est celle de l'adapter web — c'est lui qui assemble
	// les vues transverses, le domaine document ne connaît pas les devis (R2).
	errCibleDevisInconnue = errors.New("web : devis de rattachement inconnu")
	// errCibleFactureInconnue est le pendant pour une facture : le domaine
	// document ne connaît pas plus le domaine finance (R2), c'est ici que
	// l'existence de la cible se vérifie.
	errCibleFactureInconnue = errors.New("web : facture de rattachement inconnue")
	// errCibleEtapeInconnue est le pendant pour une étape du planning.
	errCibleEtapeInconnue = errors.New("web : étape de rattachement inconnue")
)

// documentErrorMessages traduit les erreurs métier en messages du catalogue.
//
// Même modèle que devisErrorMessages : une erreur absente de la table n'est
// pas un refus que l'utilisateur peut corriger, c'est une panne — elle se
// journalise et s'affiche comme telle plutôt que de se déguiser en faute de
// saisie.
var documentErrorMessages = []struct {
	err       error
	messageID string
}{
	{document.ErrEmptyFileName, "document.erreur.nom_vide"},
	{document.ErrFileNameTooLong, "document.erreur.nom_trop_long"},
	{document.ErrDescriptionTooLong, "document.erreur.description_trop_longue"},
	{document.ErrFileTooLarge, "document.erreur.trop_gros"},
	{document.ErrEmptyContent, "document.erreur.contenu_vide"},
	{document.ErrUnsupportedMimeType, "document.erreur.type_interdit"},
	{document.ErrUnknownCategory, "document.erreur.categorie_inconnue"},
	{document.ErrInvalidTarget, "document.erreur.cible_invalide"},
	{errFichierManquant, "document.erreur.fichier_manquant"},
	{errCibleDevisInconnue, "document.erreur.cible_devis_inconnue"},
	{errCibleFactureInconnue, "document.erreur.cible_facture_inconnue"},
	{errCibleEtapeInconnue, "document.erreur.cible_etape_inconnue"},
}

// documentMessageID rend l'identifiant de message correspondant à une erreur,
// ou la chaîne vide si l'erreur n'est pas un refus prévu.
func documentMessageID(err error) string {
	for _, entry := range documentErrorMessages {
		if errors.Is(err, entry.err) {
			return entry.messageID
		}
	}
	return ""
}

// mountDocuments branche les routes du domaine.
//
// Chaque route est gardée par un scope, téléchargement compris : jamais de
// fichier servi sans contrôle — le contenu des pièces (devis, finances,
// assurance) est ce que l'application a de plus confidentiel, et un
// identifiant non devinable n'est pas une autorisation.
func (h *Handler) mountDocuments() {
	h.mux.HandleFunc("GET "+documentsPath, h.requireScope(identity.ScopeDocumentRead, h.handleDocumentsIndex))
	h.mux.HandleFunc("POST "+documentsPath, h.requireScope(identity.ScopeDocumentWrite, h.handleDocumentUpload))
	h.mux.HandleFunc("GET "+documentsPath+"/{id}"+documentDownloadSuffix,
		h.requireScope(identity.ScopeDocumentRead, h.handleDocumentDownload))
}

// documentsIndexData est la charge utile de la liste des pièces.
type documentsIndexData struct {
	// Documents sont les pièces, de la plus récente à la plus ancienne.
	Documents []documentRow
	// Form est le formulaire de téléversement, réaffiché tel quel après un
	// refus de saisie.
	Form documentFormData
	// Avis est le message qui suit une action, s'il y en a un.
	Avis avisView
}

// documentRow est une ligne du tableau des pièces.
type documentRow struct {
	Nom            string
	TelechargerURL string
	// Description est la note saisie au dépôt, vide le plus souvent.
	Description string
	// Categorie et Taille sont déjà traduites : le gabarit les affiche telles
	// quelles.
	Categorie string
	Taille    string
	DeposeeLe string
	// Rattachement décrit la cible, déjà traduit ; vide pour une pièce libre.
	Rattachement string
	// RattachementURL mène à la page de la cible quand elle se résout — la
	// comparaison de la demande, pour un devis.
	RattachementURL string
}

// documentFormData est le formulaire de téléversement.
type documentFormData struct {
	Action      string
	Categories  []categorieOption
	Description string
	// CibleType et CibleID voyagent en champs cachés : le formulaire de la
	// page de comparaison rattache à un devis, celui de la liste ne rattache à
	// rien.
	CibleType string
	CibleID   string
	Error     string
}

// categorieOption est une entrée du sélecteur de catégorie.
type categorieOption struct {
	Value string
	// Label est déjà traduit.
	Label string
	// Selected réaffiche le choix après un refus.
	Selected bool
}

// handleDocumentsIndex sert la liste des pièces.
func (h *Handler) handleDocumentsIndex(w http.ResponseWriter, r *http.Request) {
	h.renderDocumentsIndex(w, r, http.StatusOK, h.emptyDocumentForm(r))
}

// newDocumentRows met les pièces sous leur forme d'affichage. Le rattachement
// à un devis est résolu ici — c'est l'adapter web qui assemble les vues
// transverses (R2).
//
// La résolution est mémoïsée le temps de la requête, par (type, identifiant) de
// cible : plusieurs pièces pointent couramment la même étape ou le même devis —
// les photos d'un lot, les pièces jointes d'une proposition — et sans mémo la
// page relisait la même cible autant de fois qu'elle porte de pièces. Une cible
// disparue se mémorise comme les autres : c'est son libellé « disparu » qui est
// retenu, pas une erreur.
func (h *Handler) newDocumentRows(r *http.Request, documents []document.Document) ([]documentRow, error) {
	resolved := make(map[document.Target]targetView, len(documents))

	rows := make([]documentRow, 0, len(documents))
	for _, doc := range documents {
		row := documentRow{
			Nom:            doc.FileName,
			TelechargerURL: documentDownloadPath(doc.ID),
			Description:    doc.Description,
			Categorie:      h.translate(r, "document.categorie."+doc.Category.String()),
			Taille:         h.formatTaille(r, doc.SizeBytes),
			DeposeeLe:      formatDate(doc.CreatedAt),
		}

		rattachement, known := resolved[doc.Target]
		if !known {
			var err error
			if rattachement, err = h.describeTarget(r, doc.Target); err != nil {
				return nil, err
			}
			resolved[doc.Target] = rattachement
		}
		row.Rattachement, row.RattachementURL = rattachement.label, rattachement.url

		rows = append(rows, row)
	}

	return rows, nil
}

// targetView est la description affichable d'un rattachement.
type targetView struct {
	label string
	url   string
}

// describeTarget met une cible sous sa forme d'affichage.
//
// Une cible de devis qui ne se résout plus n'est pas une panne : la référence
// est faible par construction (R2), et une pièce doit rester lisible quand sa
// cible a disparu. Toute autre erreur de lecture, elle, en est une.
func (h *Handler) describeTarget(r *http.Request, target document.Target) (targetView, error) {
	switch target.Type {
	case "":
		return targetView{}, nil
	case document.TargetDevis:
		proposition, err := h.devis.Devis(r.Context(), devis.ID(target.ID))
		if errors.Is(err, devis.ErrUnknownDevis) {
			return targetView{label: h.translate(r, "document.rattachement.devis_disparu")}, nil
		}
		if err != nil {
			return targetView{}, fmt.Errorf("résolution du devis rattaché %s : %w", target.ID, err)
		}
		return targetView{
			label: h.translate(r, "document.rattachement.devis", "Entreprise", proposition.Artisan.Entreprise),
			url:   demandePath(proposition.DemandeID),
		}, nil
	case document.TargetFacture:
		facture, err := h.finance.Facture(r.Context(), finance.ID(target.ID))
		if errors.Is(err, finance.ErrUnknownFacture) {
			return targetView{label: h.translate(r, "document.rattachement.facture_disparue")}, nil
		}
		if err != nil {
			return targetView{}, fmt.Errorf("résolution de la facture rattachée %s : %w", target.ID, err)
		}
		if facture.Numero != "" {
			return targetView{
				label: h.translate(r, "document.rattachement.facture_numerotee",
					"Entreprise", facture.Entreprise, "Numero", facture.Numero),
				url: financesPath,
			}, nil
		}
		return targetView{
			label: h.translate(r, "document.rattachement.facture", "Entreprise", facture.Entreprise),
			url:   financesPath,
		}, nil
	case document.TargetEtape:
		etape, err := h.planning.Etape(r.Context(), planning.ID(target.ID))
		if errors.Is(err, planning.ErrUnknownEtape) {
			return targetView{label: h.translate(r, "document.rattachement.etape_disparue")}, nil
		}
		if err != nil {
			return targetView{}, fmt.Errorf("résolution de l'étape rattachée %s : %w", target.ID, err)
		}
		return targetView{
			label: h.translate(r, "document.rattachement.etape", "Nom", etape.Name),
			url:   planningPath,
		}, nil
	default:
		// Un type de cible que le domaine reconnaîtrait mais que cette vue ne
		// sait pas encore décrire : la pièce s'affiche avec sa cible nommée,
		// sans lien à suivre — jamais une panne.
		return targetView{label: h.translate(r, "document.rattachement.cible", "Type", target.Type.String(), "ID", target.ID)}, nil
	}
}

// emptyDocumentForm rend le formulaire de téléversement vierge de la liste.
func (h *Handler) emptyDocumentForm(r *http.Request) documentFormData {
	return documentFormData{
		Action:     documentsPath,
		Categories: h.categorieOptions(r, ""),
	}
}

// categorieOptions rend le sélecteur de catégorie, avec le choix soumis
// resélectionné le cas échéant.
func (h *Handler) categorieOptions(r *http.Request, selected string) []categorieOption {
	options := make([]categorieOption, 0, len(document.AllCategories()))
	for _, category := range document.AllCategories() {
		options = append(options, categorieOption{
			Value:    category.String(),
			Label:    h.translate(r, "document.categorie."+category.String()),
			Selected: category.String() == selected,
		})
	}
	return options
}

// handleDocumentUpload dépose une pièce.
//
// Le type de contenu retenu est celui que révèle le contenu lui-même
// (http.DetectContentType sur les premiers octets) : ni l'extension du nom, ni
// le Content-Type annoncé par le client ne font foi — les deux s'écrivent
// librement, le contenu non. La taille retenue est celle constatée du fichier
// reçu, pour la même raison.
func (h *Handler) handleDocumentUpload(w http.ResponseWriter, r *http.Request) {
	// Le plafond de la requête entière ; voir uploadOverheadBytes pour le
	// partage des rôles entre ce 413 et le 422 du domaine.
	r.Body = http.MaxBytesReader(w, r.Body, document.MaxFileSize+uploadOverheadBytes)

	if err := r.ParseMultipartForm(uploadMemoryBytes); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			h.rejectDocumentOversizedRequest(w, r)
			return
		}
		// Un corps qui n'est pas du multipart valide ne vient pas d'un
		// formulaire d'Avanti : le refus est brut, pas une page de formulaire.
		http.Error(w, h.translate(r, "document.erreur.televersement_invalide"), http.StatusBadRequest)
		return
	}

	// La cible est normalisée UNE fois, ici, et c'est la valeur normalisée qui
	// circule ensuite — vers la résolution du devis comme vers le domaine.
	// Comparer la saisie brute laisserait « Devis » ou « devis  » (casse,
	// blancs) contourner la vérification d'existence : le domaine normalise
	// avant de stocker, la vérification doit voir la même valeur que lui.
	target, err := document.NormalizeTarget(document.Target{
		Type: document.TargetType(r.PostFormValue(fieldCibleType)),
		ID:   r.PostFormValue(fieldCibleID),
	})
	if err != nil {
		h.rejectDocumentForm(w, r, err)
		return
	}

	proposition, err := h.resolveCibleDevis(r, target)
	if err == nil {
		err = h.resolveCibleFacture(r, target)
	}
	if err == nil {
		err = h.resolveCibleEtape(r, target)
	}
	if err == nil {
		err = h.uploadFromRequest(r, target)
	}
	if err != nil {
		h.rejectDocumentForm(w, r, err)
		return
	}

	// Le dépôt rattaché à un devis ramène à la comparaison d'où il est parti,
	// celui rattaché à une facture à la page des finances, celui rattaché à
	// une étape au planning ; le dépôt libre, à la liste des pièces.
	destination := documentsPath
	switch {
	case proposition != nil:
		destination = demandePath(proposition.DemandeID)
	case target.Type == document.TargetFacture:
		destination = financesPath
	case target.Type == document.TargetEtape:
		destination = planningPath
	}
	h.redirectAfterPost(w, r, destination+"?"+paramAvis+"="+avisDocumentAjoute)
}

// resolveCibleDevis vérifie, avant le dépôt, qu'un rattachement à un devis
// désigne un devis qui existe, et rend la proposition pour la redirection. La
// cible reçue est déjà normalisée. Sans rattachement de devis, rend nil sans
// erreur : la validation générale de la cible reste l'affaire du domaine.
func (h *Handler) resolveCibleDevis(r *http.Request, target document.Target) (*devis.Devis, error) {
	if target.Type != document.TargetDevis {
		return nil, nil //nolint:nilnil // absence de cible devis : rien à résoudre, rien à reprocher.
	}

	proposition, err := h.devis.Devis(r.Context(), devis.ID(target.ID))
	if errors.Is(err, devis.ErrUnknownDevis) {
		return nil, fmt.Errorf("%w : %s", errCibleDevisInconnue, target.ID)
	}
	if err != nil {
		return nil, fmt.Errorf("résolution du devis de rattachement : %w", err)
	}

	return &proposition, nil
}

// resolveCibleFacture vérifie, avant le dépôt, qu'un rattachement à une
// facture désigne une facture qui existe — le miroir de [resolveCibleDevis]
// pour le domaine finance. Sans rattachement de facture, rend nil sans
// erreur.
func (h *Handler) resolveCibleFacture(r *http.Request, target document.Target) error {
	if target.Type != document.TargetFacture {
		return nil
	}

	_, err := h.finance.Facture(r.Context(), finance.ID(target.ID))
	if errors.Is(err, finance.ErrUnknownFacture) {
		return fmt.Errorf("%w : %s", errCibleFactureInconnue, target.ID)
	}
	if err != nil {
		return fmt.Errorf("résolution de la facture de rattachement : %w", err)
	}

	return nil
}

// resolveCibleEtape vérifie, avant le dépôt, qu'un rattachement à une étape
// désigne une étape qui existe — le miroir de [resolveCibleFacture] pour le
// domaine planning. Sans rattachement d'étape, rend nil sans erreur.
func (h *Handler) resolveCibleEtape(r *http.Request, target document.Target) error {
	if target.Type != document.TargetEtape {
		return nil
	}

	_, err := h.planning.Etape(r.Context(), planning.ID(target.ID))
	if errors.Is(err, planning.ErrUnknownEtape) {
		return fmt.Errorf("%w : %s", errCibleEtapeInconnue, target.ID)
	}
	if err != nil {
		return fmt.Errorf("résolution de l'étape de rattachement : %w", err)
	}

	return nil
}

// uploadFromRequest lit la partie fichier, constate son type et sa taille, et
// confie le dépôt au domaine. La cible reçue est déjà normalisée — le domaine
// renormalisera, c'est idempotent.
func (h *Handler) uploadFromRequest(r *http.Request, target document.Target) error {
	file, header, err := r.FormFile(fieldFichier)
	if err != nil {
		return fmt.Errorf("%w : %w", errFichierManquant, err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			h.fail(r, fmt.Errorf("fermeture du fichier téléversé : %w", closeErr))
		}
	}()

	mimeType, err := sniffMimeType(file)
	if err != nil {
		return err
	}

	_, uploadErr := h.documents.Upload(r.Context(), document.UploadInput{
		FileName:    header.Filename,
		MimeType:    mimeType,
		SizeBytes:   header.Size,
		Content:     file,
		Category:    r.PostFormValue(fieldCategorie),
		Description: r.PostFormValue(fieldDescription),
		Target:      target,
		By:          document.ActeurID(ActorFromContext(r.Context()).UserID().String()),
	})

	return uploadErr
}

// sniffMimeType constate le type de contenu sur les premiers octets du
// fichier, puis remet le curseur au début pour que le dépôt reparte de zéro.
func sniffMimeType(file io.ReadSeeker) (string, error) {
	head := make([]byte, 512)
	n, err := io.ReadFull(file, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("lecture de l'entame du fichier téléversé : %w", err)
	}

	// DetectContentType peut suffixer des paramètres (« ; charset=utf-8 ») ;
	// le domaine ne compare que des types nus.
	mimeType, _, parseErr := mime.ParseMediaType(http.DetectContentType(head[:n]))
	if parseErr != nil {
		return "", fmt.Errorf("analyse du type de contenu constaté : %w", parseErr)
	}

	if _, seekErr := file.Seek(0, io.SeekStart); seekErr != nil {
		return "", fmt.Errorf("retour au début du fichier téléversé : %w", seekErr)
	}

	return mimeType, nil
}

// rejectDocumentForm réaffiche la liste des pièces avec le formulaire tel
// qu'il a été soumis et le message d'échec.
//
// C'est la page de la liste qui sert de point de retour, même pour un dépôt
// parti d'une page de comparaison : le fichier lui-même ne peut pas être
// réaffiché — un navigateur ne pré-remplit jamais un champ fichier — donc la
// saisie à reprendre est la même des deux côtés, et la liste porte le
// formulaire complet.
func (h *Handler) rejectDocumentForm(w http.ResponseWriter, r *http.Request, cause error) {
	messageID := documentMessageID(cause)
	if messageID == "" {
		h.failPage(w, r, fmt.Errorf("dépôt d'une pièce : %w", cause))
		return
	}

	form := documentFormData{
		Action:      documentsPath,
		Categories:  h.categorieOptions(r, r.PostFormValue(fieldCategorie)),
		Description: r.PostFormValue(fieldDescription),
		CibleType:   r.PostFormValue(fieldCibleType),
		CibleID:     r.PostFormValue(fieldCibleID),
		Error:       h.translate(r, messageID),
	}

	h.renderDocumentsIndex(w, r, http.StatusUnprocessableEntity, form)
}

// rejectDocumentOversizedRequest répond au dépassement du plafond de requête.
// Le formulaire soumis n'est plus lisible — le corps a été coupé en route —
// donc la page repart d'un formulaire vierge, avec le message qui explique.
func (h *Handler) rejectDocumentOversizedRequest(w http.ResponseWriter, r *http.Request) {
	form := h.emptyDocumentForm(r)
	form.Error = h.translate(r, "document.erreur.trop_gros")

	h.renderDocumentsIndex(w, r, http.StatusRequestEntityTooLarge, form)
}

// renderDocumentsIndex relit la liste et rend la page avec le formulaire dans
// l'état où l'appelant le lui donne.
func (h *Handler) renderDocumentsIndex(w http.ResponseWriter, r *http.Request, status int, form documentFormData) {
	documents, err := h.documents.Documents(r.Context())
	if err != nil {
		h.failPage(w, r, fmt.Errorf("lecture des pièces : %w", err))
		return
	}
	rows, err := h.newDocumentRows(r, documents)
	if err != nil {
		h.failPage(w, r, err)
		return
	}

	h.render(w, r, pageDocumentsIndex, status, documentsIndexData{
		Documents: rows,
		Form:      form,
		Avis:      h.avisFor(r),
	})
}

// handleDocumentDownload sert le contenu d'une pièce, en flux.
//
// Les métadonnées font foi sur toute la ligne : le Content-Type est le type
// constaté au dépôt, la taille celle enregistrée, le nom celui nettoyé par le
// domaine. mime.FormatMediaType assemble le Content-Disposition — c'est lui
// qui sait citer ou encoder un nom de fichier sans qu'un guillemet ou un
// caractère spécial ne casse l'en-tête ; les retours à la ligne, eux, ne
// peuvent pas s'y trouver, le domaine les a retirés au dépôt.
func (h *Handler) handleDocumentDownload(w http.ResponseWriter, r *http.Request) {
	doc, content, err := h.documents.Open(r.Context(), document.ID(r.PathValue("id")))
	if errors.Is(err, document.ErrUnknownDocument) {
		h.handleNotFound(w, r)
		return
	}
	if err != nil {
		// Un contenu introuvable pour des métadonnées connues est une
		// incohérence de stockage : une panne, pas un 404.
		h.failPage(w, r, fmt.Errorf("ouverture d'une pièce : %w", err))
		return
	}
	defer func() {
		if closeErr := content.Close(); closeErr != nil {
			h.fail(r, fmt.Errorf("fermeture du contenu d'une pièce : %w", closeErr))
		}
	}()

	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": doc.FileName})
	if disposition == "" {
		// Injoignable avec un nom nettoyé par le domaine ; le repli garde au
		// moins le comportement de pièce jointe.
		disposition = "attachment"
	}

	w.Header().Set("Content-Type", doc.MimeType)
	w.Header().Set("Content-Length", strconv.FormatInt(doc.SizeBytes, 10))
	w.Header().Set("Content-Disposition", disposition)
	// Le contenu est confidentiel, au même titre que les pages : aucun cache
	// intermédiaire n'a à en garder copie.
	w.Header().Set("Cache-Control", "no-store")

	if _, err := io.Copy(w, content); err != nil {
		// L'en-tête est parti : il ne reste que la trace au journal.
		h.fail(r, fmt.Errorf("envoi du contenu d'une pièce : %w", err))
	}
}

// formatTaille écrit une taille en octets sous une forme lisible, l'unité
// venant du catalogue. L'arithmétique est entière, au dixième près : « 1,4 Mo »
// se calcule sans flottant, comme tout le reste des nombres d'Avanti.
func (h *Handler) formatTaille(r *http.Request, size int64) string {
	const (
		kio = 1 << 10
		mio = 1 << 20
	)

	switch {
	case size >= mio:
		return h.translate(r, "document.taille.mio", "Valeur", tenthsOf(size, mio))
	case size >= kio:
		return h.translate(r, "document.taille.kio", "Valeur", tenthsOf(size, kio))
	default:
		return h.translate(r, "document.taille.octets", "Valeur", strconv.FormatInt(size, 10))
	}
}

// tenthsOf écrit size/unit avec au plus une décimale, en arithmétique entière.
func tenthsOf(size, unit int64) string {
	tenths := size * 10 / unit

	text := strconv.FormatInt(tenths/10, 10)
	if remainder := tenths % 10; remainder > 0 {
		text += decimalSeparator + strconv.FormatInt(remainder, 10)
	}

	return text
}

// documentDownloadPath rend l'adresse de téléchargement d'une pièce.
func documentDownloadPath(id document.ID) string {
	return documentsPath + "/" + url.PathEscape(id.String()) + documentDownloadSuffix
}
