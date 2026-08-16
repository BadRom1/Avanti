package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/Romain-Badino/Avanti/internal/identity"
	"github.com/Romain-Badino/Avanti/internal/planning"
)

// Chemins du domaine planning. En français, comme toutes les URLs visibles
// d'Avanti. La page est unique — jalons, Gantt, étapes, formulaires — et les
// actions postent vers des sous-chemins de l'élément qu'elles touchent.
const (
	planningPath       = "/planning"
	planningEtapesPath = "/planning/etapes"
	planningJalonsPath = "/planning/jalons"

	// Suffixes des actions. « modifier » est la page GET du formulaire ; la
	// soumission poste sur le chemin de l'étape elle-même.
	suffixDemarrer  = "/demarrer"
	suffixTerminer  = "/terminer"
	suffixModifier  = "/modifier"
	suffixAtteindre = "/atteindre"
)

// Noms des champs des formulaires du domaine. En français : visibles dans le
// HTML et dans ce qu'une personne soumet. nom, description et devis_id
// réutilisent les champs déjà déclarés ailleurs.
const (
	fieldNom         = "nom"
	fieldDebutPrevu  = "debut_prevu"
	fieldFinPrevue   = "fin_prevue"
	fieldDependances = "dependances"
	fieldDatePrevue  = "date_prevue"
	// fieldModifieLe est la garde optimiste des formulaires : l'horodatage de
	// modification que la page affichée connaissait, en RFC 3339. Une
	// soumission dont l'horodatage ne correspond plus à la ligne est refusée
	// par le domaine (ErrConcurrentUpdate) plutôt que d'écraser ce que
	// quelqu'un d'autre vient de poser.
	fieldModifieLe = "modifie_le"
)

// Codes d'avis du domaine (voir avisCatalog dans devis.go).
const (
	avisEtapeCreee    = "etape_creee"
	avisEtapeModifiee = "etape_modifiee"
	avisEtapeDemarree = "etape_demarree"
	avisEtapeTerminee = "etape_terminee"
	avisJalonCree     = "jalon_cree"
	avisJalonAtteint  = "jalon_atteint"
	// avisPlanningModifie suit un conflit d'écriture : quelqu'un a modifié
	// l'élément entre-temps, la page rechargée montre l'état réel — le modèle
	// de piece_modifiee côté finance.
	avisPlanningModifie = "planning_modifie"
)

// errHorodatageIllisible signale un champ modifie_le forgé ou corrompu. Les
// formulaires en posent toujours un valide : le voir illisible est une
// soumission trafiquée, refusée comme une saisie et non comme une panne.
var errHorodatageIllisible = errors.New("web : horodatage de modification illisible")

// planningErrorMessages traduit les erreurs métier en messages du catalogue.
//
// Même modèle que devisErrorMessages : une erreur absente de la table n'est
// pas un refus que l'utilisateur peut corriger, c'est une panne — elle se
// journalise et s'affiche comme telle plutôt que de se déguiser en faute de
// saisie.
var planningErrorMessages = []struct {
	err       error
	messageID string
}{
	{planning.ErrEmptyName, "planning.erreur.nom_vide"},
	{planning.ErrTextTooLong, "devis.erreur.texte_trop_long"},
	{planning.ErrMissingDate, "devis.erreur.date_manquante"},
	{planning.ErrInvalidPlannedRange, "planning.erreur.plage_invalide"},
	{planning.ErrInvalidDevisID, "finance.erreur.devis_invalide"},
	{planning.ErrTooManyDependencies, "planning.erreur.dependances_trop_nombreuses"},
	{planning.ErrSelfDependency, "planning.erreur.dependance_invalide"},
	{planning.ErrDuplicateDependency, "planning.erreur.dependance_invalide"},
	{planning.ErrUnknownDependency, "planning.erreur.dependance_inconnue"},
	{planning.ErrDependencyCycle, "planning.erreur.cycle"},
	{planning.ErrDependenciesLocked, "planning.erreur.dependances_verrouillees"},
	{planning.ErrPrerequisitesNotDone, "planning.erreur.prerequis_non_termines"},
	{planning.ErrEtapeAlreadyStarted, "planning.erreur.deja_demarree"},
	{planning.ErrEtapeNotStarted, "planning.erreur.non_demarree"},
	{planning.ErrEtapeAlreadyFinished, "planning.erreur.deja_terminee"},
	{planning.ErrFinishBeforeStart, "planning.erreur.fin_avant_debut"},
	{planning.ErrJalonAlreadyReached, "planning.erreur.jalon_deja_atteint"},
	{errFinanceDevisInconnu, "finance.erreur.devis_invalide"},
	{errDevisNonRetenu, "finance.erreur.devis_non_retenu"},
	{errDateIllisible, "devis.erreur.date_illisible"},
	{errHorodatageIllisible, "planning.erreur.horodatage"},
}

// planningMessageID rend l'identifiant de message correspondant à une erreur,
// ou la chaîne vide si l'erreur n'est pas un refus prévu.
func planningMessageID(err error) string {
	for _, entry := range planningErrorMessages {
		if errors.Is(err, entry.err) {
			return entry.messageID
		}
	}
	return ""
}

// mountPlanning branche les routes du domaine.
//
// Chaque route est gardée par un scope : lecture pour la page, écriture pour
// tout ce qui change quelque chose — la table des rôles donne les deux au
// collaborateur, c'est le domaine que l'architecte travaille.
func (h *Handler) mountPlanning() {
	h.mux.HandleFunc("GET "+planningPath, h.requireScope(identity.ScopePlanningRead, h.handlePlanningIndex))

	h.mux.HandleFunc("POST "+planningEtapesPath, h.requireScope(identity.ScopePlanningWrite, h.handleCreateEtape))
	h.mux.HandleFunc("GET "+planningEtapesPath+"/{id}"+suffixModifier,
		h.requireScope(identity.ScopePlanningWrite, h.handleEtapeModifierForm))
	h.mux.HandleFunc("POST "+planningEtapesPath+"/{id}",
		h.requireScope(identity.ScopePlanningWrite, h.handleUpdateEtape))
	h.mux.HandleFunc("POST "+planningEtapesPath+"/{id}"+suffixDemarrer,
		h.requireScope(identity.ScopePlanningWrite, h.handleEtapeDemarrer))
	h.mux.HandleFunc("POST "+planningEtapesPath+"/{id}"+suffixTerminer,
		h.requireScope(identity.ScopePlanningWrite, h.handleEtapeTerminer))

	h.mux.HandleFunc("POST "+planningJalonsPath, h.requireScope(identity.ScopePlanningWrite, h.handleCreateJalon))
	h.mux.HandleFunc("POST "+planningJalonsPath+"/{id}"+suffixAtteindre,
		h.requireScope(identity.ScopePlanningWrite, h.handleJalonAtteindre))
}

// --- Le Gantt en colonnes -------------------------------------------------------

// ganttColumns est la résolution du Gantt rendu : une grille de 60 colonnes de
// tableau, soit environ 1,7 % de la plage par colonne.
//
// Pourquoi des colspan et pas des largeurs CSS : la CSP d'Avanti est
// `style-src 'self'` SANS 'unsafe-inline' (voir setSecurityHeaders), donc un
// attribut style="width:…" serait bloqué par le navigateur — et l'assouplir
// pour un diagramme serait payer en surface d'attaque ce qu'un attribut HTML
// donne gratuitement. colspan est un attribut de structure, pas du style : la
// table à layout fixe répartit ses 60 colonnes également, et une barre de
// n colonnes occupe exactement n/60 de la largeur. Les positions du domaine
// sont en millièmes entiers ; la conversion vers les colonnes reste en
// arithmétique entière (millièmes × 60 / 1000) — aucun flottant nulle part.
const ganttColumns = 60

// ganttAxisTicks est le nombre de graduations de l'axe des dates. 60 colonnes
// se divisent exactement en 4 graduations de 15.
const ganttAxisTicks = 4

// ganttCell est une cellule de la grille : sa largeur en colonnes et sa classe.
type ganttCell struct {
	Span  int
	Class string
}

// ganttBarCells découpe une ligne de la grille en trois cellules au plus :
// le vide avant la barre, la barre, le vide après. Tout en entier.
func ganttBarCells(fromMille, toMille int, class string) []ganttCell {
	from := fromMille * ganttColumns / 1000
	to := toMille * ganttColumns / 1000

	// Une barre occupe toujours au moins une colonne, et reste dans la grille.
	if from > ganttColumns-1 {
		from = ganttColumns - 1
	}
	if to <= from {
		to = from + 1
	}
	if to > ganttColumns {
		to = ganttColumns
	}

	cells := make([]ganttCell, 0, 3)
	if from > 0 {
		cells = append(cells, ganttCell{Span: from, Class: "gantt__vide"})
	}
	cells = append(cells, ganttCell{Span: to - from, Class: class})
	if to < ganttColumns {
		cells = append(cells, ganttCell{Span: ganttColumns - to, Class: "gantt__vide"})
	}

	return cells
}

// ganttData est le diagramme prêt à rendre.
type ganttData struct {
	// Present distingue un Gantt dessinable d'un planning encore vide.
	Present bool
	// Du et Au bornent la plage affichée, déjà mis en forme. Au est le dernier
	// jour couvert — la borne du domaine est exclusive, l'affichage la ramène
	// au jour que les gens lisent.
	Du string
	Au string
	// Cols est parcouru par le gabarit pour poser le colgroup : autant
	// d'entrées que de colonnes de grille.
	Cols []struct{}
	// Axis est la ligne des graduations de dates.
	Axis []ganttAxisTick
	// Lignes sont les étapes, dans l'ordre du domaine.
	Lignes []ganttLigne
	// Jalons sont les marqueurs, une ligne fine chacun.
	Jalons []ganttJalonLigne
}

// ganttAxisTick est une graduation de l'axe : une date posée au bord gauche
// d'un bloc de colonnes.
type ganttAxisTick struct {
	Span  int
	Label string
}

// ganttLigne est une étape sur la grille.
type ganttLigne struct {
	Nom    string
	Statut string
	// StatutClass est le modificateur CSS du statut (voir avanti.css).
	StatutClass string
	// Retard est le libellé « en retard de N j », vide sinon.
	Retard string
	// Cells est la ligne de la barre prévue.
	Cells []ganttCell
	// Reelle dit qu'une seconde ligne fine porte la barre réelle.
	Reelle      bool
	ReelleCells []ganttCell
}

// ganttJalonLigne est un jalon sur la grille.
type ganttJalonLigne struct {
	Nom    string
	Date   string
	Statut string
	Retard string
	Cells  []ganttCell
}

// newGanttData met la vue du domaine en colonnes.
func (h *Handler) newGanttData(r *http.Request, view planning.GanttView) ganttData {
	if view.Empty {
		return ganttData{}
	}

	data := ganttData{
		Present: true,
		Du:      formatDate(view.From),
		// La borne haute du domaine est exclusive (lendemain 00:00) : le
		// dernier jour couvert est la veille.
		Au:   formatDate(view.To.Add(-24 * time.Hour)),
		Cols: make([]struct{}, ganttColumns),
		Axis: newGanttAxis(view),
	}

	for _, row := range view.Rows {
		ligne := ganttLigne{
			Nom:         row.Name,
			Statut:      h.translate(r, "planning.statut."+row.Statut.String()),
			StatutClass: "statut--" + row.Statut.String(),
			Cells:       ganttBarCells(row.PlannedFrom, row.PlannedTo, ganttPlannedClass(row)),
		}
		if row.EnRetard {
			ligne.Retard = h.translate(r, "planning.retard", "Jours", strconv.Itoa(row.RetardJours))
		}
		if row.Started {
			ligne.Reelle = true
			ligne.ReelleCells = ganttBarCells(row.ActualFrom, row.ActualTo, "gantt__barre gantt__barre--reelle")
		}
		data.Lignes = append(data.Lignes, ligne)
	}

	for _, jalon := range view.Jalons {
		ligne := ganttJalonLigne{
			Nom:    jalon.Name,
			Date:   formatDate(jalon.Date),
			Statut: h.jalonStatut(r, jalon.Atteint),
			Cells:  ganttBarCells(jalon.Position, jalon.Position+1, ganttJalonClass(jalon)),
		}
		if jalon.EnRetard {
			ligne.Retard = h.translate(r, "planning.retard", "Jours", strconv.Itoa(jalon.RetardJours))
		}
		data.Jalons = append(data.Jalons, ligne)
	}

	return data
}

// ganttPlannedClass choisit la classe de la barre prévue : en retard prime, le
// terminé s'éteint, le reste est neutre.
func ganttPlannedClass(row planning.GanttRow) string {
	switch {
	case row.EnRetard:
		return "gantt__barre gantt__barre--retard"
	case row.Statut == planning.StatutTerminee:
		return "gantt__barre gantt__barre--terminee"
	default:
		return "gantt__barre gantt__barre--prevue"
	}
}

// ganttJalonClass choisit la classe du marqueur d'un jalon.
func ganttJalonClass(jalon planning.GanttJalon) string {
	switch {
	case jalon.EnRetard:
		return "gantt__jalon gantt__jalon--retard"
	case jalon.Atteint:
		return "gantt__jalon gantt__jalon--atteint"
	default:
		return "gantt__jalon"
	}
}

// newGanttAxis pose les graduations : la date au bord gauche de chaque quart
// de la plage, calculée en secondes entières.
func newGanttAxis(view planning.GanttView) []ganttAxisTick {
	rangeSeconds := int64(view.To.Sub(view.From) / time.Second)

	ticks := make([]ganttAxisTick, 0, ganttAxisTicks)
	for i := range ganttAxisTicks {
		offset := rangeSeconds * int64(i) / ganttAxisTicks
		ticks = append(ticks, ganttAxisTick{
			Span:  ganttColumns / ganttAxisTicks,
			Label: formatDate(view.From.Add(time.Duration(offset) * time.Second)),
		})
	}

	return ticks
}

// --- La page --------------------------------------------------------------------

// planningIndexData est la charge utile de la page du planning.
type planningIndexData struct {
	// Jalons liste les échéances, retards en évidence.
	Jalons []jalonRow
	// Gantt est le diagramme dérivé, prêt à rendre.
	Gantt ganttData
	// Pretes liste les étapes prêtes à démarrer : prévues, tous prérequis
	// terminés — les candidates à la parallélisation.
	Pretes []preteRow
	// Etapes liste les lots, dans l'ordre du planning.
	Etapes []etapeRow
	// EtapeForm et JalonForm sont les formulaires de création, réaffichés tels
	// quels après un refus. Vides si la personne n'a pas planning:write — le
	// gabarit ne les montre alors pas.
	EtapeForm etapeFormData
	JalonForm jalonFormData
	// Avis est le message qui suit une action, s'il y en a un.
	Avis avisView
}

// jalonRow est une ligne du tableau des jalons.
type jalonRow struct {
	Nom       string
	Date      string
	Statut    string
	Atteint   bool
	AtteintLe string
	EnRetard  bool
	Retard    string

	PeutAtteindre bool
	AtteindreURL  string
	// ModifieLe est la garde optimiste, recopiée en champ caché.
	ModifieLe string
}

// preteRow est une étape prête à démarrer.
type preteRow struct {
	Nom   string
	Dates string
}

// etapeRow est une ligne du tableau des étapes.
type etapeRow struct {
	Nom         string
	Description string
	Devis       string
	DebutPrevu  string
	FinPrevue   string
	DebutReel   string
	FinReelle   string
	Statut      string
	StatutClass string
	EnRetard    bool
	Retard      string
	Prete       bool

	PeutDemarrer bool
	PeutTerminer bool
	DemarrerURL  string
	TerminerURL  string
	ModifierURL  string
	ModifieLe    string
}

// etapeFormData est le formulaire de création ou de modification d'une étape.
type etapeFormData struct {
	Action      string
	Nom         string
	Description string
	DebutPrevu  string
	FinPrevue   string
	Devis       []devisOption
	DevisID     string
	// Dependances propose les étapes existantes en cases à cocher.
	Dependances []dependanceOption
	// DependancesLocked grise les cases : l'étape est démarrée, ses prérequis
	// ne se modifient plus (le domaine refuse de toute façon).
	DependancesLocked bool
	// ModifieLe est la garde optimiste de la modification ; vide à la création.
	ModifieLe string
	Error     string
}

// dependanceOption est une étape offerte comme prérequis.
type dependanceOption struct {
	ID      string
	Label   string
	Checked bool
}

// etapeModifierData est la charge utile de la page de modification. Le
// formulaire y vit sous le même nom que sur la page d'index (EtapeForm), et ce
// n'est pas un hasard : le gabarit partiel « champs_etape » — partagé entre
// les deux pages via templates/partials/ — lit .Data.EtapeForm quel que soit
// le gabarit qui l'invoque.
type etapeModifierData struct {
	EtapeForm etapeFormData
}

// jalonFormData est le formulaire de création d'un jalon.
type jalonFormData struct {
	Action string
	Nom    string
	Date   string
	Error  string
}

// handlePlanningIndex sert la page du planning : jalons, Gantt, étapes et
// formulaires.
func (h *Handler) handlePlanningIndex(w http.ResponseWriter, r *http.Request) {
	h.renderPlanningIndex(w, r, http.StatusOK, nil, nil, "")
}

// renderPlanningIndex assemble et rend la page, avec les formulaires dans
// l'état où l'appelant les donne — nil pour un formulaire vierge — et,
// lorsqu'une action vient d'être refusée, le message du refus en avis
// d'erreur (refusal vide sinon : l'avis vient alors de l'URL).
//
// C'est l'assemblage transverse que R2 prévoit : le domaine planning donne
// étapes, jalons et Gantt dérivé, le domaine devis les libellés des lots
// retenus, et la composition se fait ici, dans l'adapter web.
func (h *Handler) renderPlanningIndex(
	w http.ResponseWriter, r *http.Request, status int,
	etapeForm *etapeFormData, jalonForm *jalonFormData, refusal string,
) {
	etapes, err := h.planning.Etapes(r.Context())
	if err != nil {
		h.failPage(w, r, fmt.Errorf("lecture des étapes : %w", err))
		return
	}

	jalons, err := h.planning.Jalons(r.Context())
	if err != nil {
		h.failPage(w, r, fmt.Errorf("lecture des jalons : %w", err))
		return
	}

	retenus, err := h.devisRetenus(r.Context())
	if err != nil {
		h.failPage(w, r, fmt.Errorf("lecture des devis retenus : %w", err))
		return
	}

	today := h.now().UTC()
	view := planning.BuildGantt(etapes, jalons, today)

	avis := h.avisFor(r)
	if refusal != "" {
		avis = avisView{Message: refusal, Erreur: true}
	}

	data := planningIndexData{
		Jalons: h.newJalonRows(r, jalons, today),
		Gantt:  h.newGanttData(r, view),
		Pretes: newPreteRows(etapes, view),
		Etapes: h.newEtapeRows(r, etapes, view, retenus),
		Avis:   avis,
	}

	// Les scopes décident de ce qui est construit, pas seulement de ce qui
	// s'affiche : sans planning:write, les formulaires restent vides et le
	// gabarit ne les montre pas — même partage que les finances.
	if ActorFromContext(r.Context()).Allows(identity.ScopePlanningWrite) {
		if etapeForm == nil {
			form := h.emptyEtapeForm(etapes, retenus)
			etapeForm = &form
		}
		if jalonForm == nil {
			form := h.emptyJalonForm()
			jalonForm = &form
		}
		data.EtapeForm = *etapeForm
		data.JalonForm = *jalonForm
	}

	h.render(w, r, pagePlanningIndex, status, data)
}

// newJalonRows met les jalons sous leur forme d'affichage.
func (h *Handler) newJalonRows(r *http.Request, jalons []planning.Jalon, today time.Time) []jalonRow {
	canWrite := ActorFromContext(r.Context()).Allows(identity.ScopePlanningWrite)

	rows := make([]jalonRow, 0, len(jalons))
	for _, jalon := range jalons {
		row := jalonRow{
			Nom:       jalon.Name,
			Date:      formatDate(jalon.Date),
			Statut:    h.jalonStatut(r, jalon.Atteint()),
			Atteint:   jalon.Atteint(),
			AtteintLe: formatInstant(jalon.ReachedAt),
			EnRetard:  jalon.EnRetard(today),

			PeutAtteindre: canWrite && !jalon.Atteint(),
			AtteindreURL:  planningElementPath(planningJalonsPath, jalon.ID, suffixAtteindre),
			ModifieLe:     jalon.UpdatedAt.Format(time.RFC3339Nano),
		}
		if row.EnRetard {
			row.Retard = h.translate(r, "planning.retard", "Jours", strconv.Itoa(jalon.RetardConstate(today)))
		}
		rows = append(rows, row)
	}

	return rows
}

// jalonStatut rend le libellé traduit de l'état d'un jalon.
func (h *Handler) jalonStatut(r *http.Request, atteint bool) string {
	if atteint {
		return h.translate(r, "planning.jalon.atteint")
	}
	return h.translate(r, "planning.jalon.attendu")
}

// newPreteRows extrait du Gantt les étapes prêtes à démarrer, avec leurs dates
// prévues : la section devient actionnable — on voit quoi lancer, et quand
// c'était prévu.
func newPreteRows(etapes []planning.Etape, view planning.GanttView) []preteRow {
	ready := make(map[planning.ID]bool, len(view.Rows))
	for _, row := range view.Rows {
		if row.PreteADemarrer {
			ready[row.ID] = true
		}
	}

	var rows []preteRow
	for _, etape := range etapes {
		if ready[etape.ID] {
			rows = append(rows, preteRow{
				Nom:   etape.Name,
				Dates: formatDate(etape.PlannedStart) + " → " + formatDate(etape.PlannedEnd),
			})
		}
	}

	return rows
}

// newEtapeRows met les étapes sous leur forme d'affichage. La vue Gantt porte
// déjà statuts, retards et « prêtes » : les recalculer ici créerait deux
// vérités.
func (h *Handler) newEtapeRows(r *http.Request, etapes []planning.Etape, view planning.GanttView, retenus []retenuInfo) []etapeRow {
	labels := retenuLabels(retenus)
	canWrite := ActorFromContext(r.Context()).Allows(identity.ScopePlanningWrite)

	byID := make(map[planning.ID]planning.GanttRow, len(view.Rows))
	for _, row := range view.Rows {
		byID[row.ID] = row
	}

	rows := make([]etapeRow, 0, len(etapes))
	for _, etape := range etapes {
		derived := byID[etape.ID]
		row := etapeRow{
			Nom:         etape.Name,
			Description: etape.Description,
			Devis:       h.financePieceLabel(r, etape.DevisID, labels),
			DebutPrevu:  formatDate(etape.PlannedStart),
			FinPrevue:   formatDate(etape.PlannedEnd),
			DebutReel:   formatInstant(etape.ActualStart),
			FinReelle:   formatInstant(etape.ActualEnd),
			Statut:      h.translate(r, "planning.statut."+derived.Statut.String()),
			StatutClass: "statut--" + derived.Statut.String(),
			EnRetard:    derived.EnRetard,
			Prete:       derived.PreteADemarrer,

			PeutDemarrer: canWrite && derived.Statut == planning.StatutPrevue,
			PeutTerminer: canWrite && derived.Statut == planning.StatutEnCours,
			DemarrerURL:  planningElementPath(planningEtapesPath, etape.ID, suffixDemarrer),
			TerminerURL:  planningElementPath(planningEtapesPath, etape.ID, suffixTerminer),
			ModifierURL:  planningElementPath(planningEtapesPath, etape.ID, suffixModifier),
			ModifieLe:    etape.UpdatedAt.Format(time.RFC3339Nano),
		}
		if derived.EnRetard {
			row.Retard = h.translate(r, "planning.retard", "Jours", strconv.Itoa(derived.RetardJours))
		}
		rows = append(rows, row)
	}

	return rows
}

// emptyEtapeForm rend un formulaire d'étape vierge, daté d'aujourd'hui — en
// UTC, comme tout ce que le domaine manipule.
func (h *Handler) emptyEtapeForm(etapes []planning.Etape, retenus []retenuInfo) etapeFormData {
	return etapeFormData{
		Action:      planningEtapesPath,
		DebutPrevu:  formatDateInput(civilDay(h.now())),
		FinPrevue:   formatDateInput(civilDay(h.now())),
		Devis:       devisOptions(retenus, ""),
		Dependances: dependanceOptions(etapes, "", nil),
	}
}

// emptyJalonForm rend un formulaire de jalon vierge.
func (h *Handler) emptyJalonForm() jalonFormData {
	return jalonFormData{
		Action: planningJalonsPath,
		Date:   formatDateInput(civilDay(h.now())),
	}
}

// dependanceOptions rend les cases à cocher des prérequis : toutes les étapes
// sauf celle qu'on modifie, cochées selon la sélection.
func dependanceOptions(etapes []planning.Etape, self planning.ID, checked []planning.ID) []dependanceOption {
	isChecked := make(map[planning.ID]bool, len(checked))
	for _, id := range checked {
		isChecked[id] = true
	}

	options := make([]dependanceOption, 0, len(etapes))
	for _, etape := range etapes {
		if etape.ID == self {
			continue
		}
		options = append(options, dependanceOption{
			ID:      etape.ID.String(),
			Label:   etape.Name,
			Checked: isChecked[etape.ID],
		})
	}

	return options
}

// --- Création et modification des étapes -----------------------------------------

// handleCreateEtape crée une étape.
func (h *Handler) handleCreateEtape(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.failPage(w, r, fmt.Errorf("lecture du formulaire d'étape : %w", err))
		return
	}

	err := h.createEtapeFromForm(r)
	if err == nil {
		h.redirectAfterPost(w, r, planningPath+"?"+paramAvis+"="+avisEtapeCreee)
		return
	}

	h.rejectEtapeForm(w, r, err)
}

// createEtapeFromForm traduit le formulaire en entrée de cas d'usage et
// l'exécute. Seules les conversions de format et la résolution du devis sont
// faites ici : la validation métier appartient au domaine.
func (h *Handler) createEtapeFromForm(r *http.Request) error {
	retenu, dates, err := h.parseEtapeCommonFields(r)
	if err != nil {
		return err
	}

	_, err = h.planning.CreateEtape(r.Context(), planning.EtapeInput{
		Name:         r.PostFormValue(fieldNom),
		Description:  r.PostFormValue(fieldDescription),
		PlannedStart: dates[0],
		PlannedEnd:   dates[1],
		DependsOn:    dependancesFrom(r),
		DevisID:      retenu.id,
		By:           planningActeurFrom(r),
	})

	return err
}

// parseEtapeCommonFields lit ce que création et modification partagent : le
// devis (résolu et vérifié RETENU — le domaine planning ne sait pas lire un
// devis, R2) et les deux dates prévues.
func (h *Handler) parseEtapeCommonFields(r *http.Request) (retenuInfo, [2]time.Time, error) {
	retenu, err := h.resolveRetenu(r, r.PostFormValue(fieldDevisID))
	if err != nil {
		return retenuInfo{}, [2]time.Time{}, err
	}
	debut, err := parseDate(r.PostFormValue(fieldDebutPrevu))
	if err != nil {
		return retenuInfo{}, [2]time.Time{}, err
	}
	fin, err := parseDate(r.PostFormValue(fieldFinPrevue))
	if err != nil {
		return retenuInfo{}, [2]time.Time{}, err
	}

	return retenu, [2]time.Time{debut, fin}, nil
}

// dependancesFrom lit les prérequis cochés.
func dependancesFrom(r *http.Request) []planning.ID {
	values := r.PostForm[fieldDependances]
	deps := make([]planning.ID, 0, len(values))
	for _, value := range values {
		deps = append(deps, planning.ID(value))
	}
	return deps
}

// rejectEtapeForm réaffiche la page avec le formulaire d'étape tel qu'il a été
// soumis et le message d'échec.
func (h *Handler) rejectEtapeForm(w http.ResponseWriter, r *http.Request, cause error) {
	messageID := planningMessageID(cause)
	if messageID == "" {
		h.failPage(w, r, fmt.Errorf("création d'une étape : %w", cause))
		return
	}

	etapes, retenus, err := h.planningFormSources(r)
	if err != nil {
		h.failPage(w, r, err)
		return
	}

	form := h.etapeFormFromRequest(r, etapes, retenus, "")
	form.Action = planningEtapesPath
	form.Error = h.translate(r, messageID)

	h.renderPlanningIndex(w, r, http.StatusUnprocessableEntity, &form, nil, "")
}

// planningFormSources relit ce qu'il faut pour reconstruire les formulaires.
func (h *Handler) planningFormSources(r *http.Request) ([]planning.Etape, []retenuInfo, error) {
	etapes, err := h.planning.Etapes(r.Context())
	if err != nil {
		return nil, nil, fmt.Errorf("lecture des étapes : %w", err)
	}
	retenus, err := h.devisRetenus(r.Context())
	if err != nil {
		return nil, nil, fmt.Errorf("lecture des devis retenus : %w", err)
	}

	return etapes, retenus, nil
}

// etapeFormFromRequest reconstruit le formulaire depuis la soumission, pour le
// réafficher tel quel.
func (h *Handler) etapeFormFromRequest(r *http.Request, etapes []planning.Etape, retenus []retenuInfo, self planning.ID) etapeFormData {
	return etapeFormData{
		Nom:         r.PostFormValue(fieldNom),
		Description: r.PostFormValue(fieldDescription),
		DebutPrevu:  r.PostFormValue(fieldDebutPrevu),
		FinPrevue:   r.PostFormValue(fieldFinPrevue),
		Devis:       devisOptions(retenus, r.PostFormValue(fieldDevisID)),
		DevisID:     r.PostFormValue(fieldDevisID),
		Dependances: dependanceOptions(etapes, self, dependancesFrom(r)),
		ModifieLe:   r.PostFormValue(fieldModifieLe),
	}
}

// handleEtapeModifierForm sert la page de modification d'une étape, formulaire
// pré-rempli.
func (h *Handler) handleEtapeModifierForm(w http.ResponseWriter, r *http.Request) {
	etape, err := h.planning.Etape(r.Context(), planning.ID(r.PathValue("id")))
	if errors.Is(err, planning.ErrUnknownEtape) {
		h.handleNotFound(w, r)
		return
	}
	if err != nil {
		h.failPage(w, r, fmt.Errorf("lecture d'une étape : %w", err))
		return
	}

	etapes, retenus, err := h.planningFormSources(r)
	if err != nil {
		h.failPage(w, r, err)
		return
	}

	form := etapeFormData{
		Action:            planningElementPath(planningEtapesPath, etape.ID, ""),
		Nom:               etape.Name,
		Description:       etape.Description,
		DebutPrevu:        formatDateInput(etape.PlannedStart),
		FinPrevue:         formatDateInput(etape.PlannedEnd),
		Devis:             devisOptions(retenus, etape.DevisID),
		DevisID:           etape.DevisID,
		Dependances:       dependanceOptions(etapes, etape.ID, etape.DependsOn),
		DependancesLocked: etape.Statut() != planning.StatutPrevue,
		ModifieLe:         etape.UpdatedAt.Format(time.RFC3339Nano),
	}

	h.render(w, r, pagePlanningEtapeModifier, http.StatusOK, etapeModifierData{EtapeForm: form})
}

// handleUpdateEtape modifie une étape.
//
// L'étape est relue AVANT de parser le formulaire : une étape inconnue est un
// 404, même quand la saisie soumise est par ailleurs invalide — l'URL prime
// sur le contenu.
func (h *Handler) handleUpdateEtape(w http.ResponseWriter, r *http.Request) {
	id := planning.ID(r.PathValue("id"))

	current, err := h.planning.Etape(r.Context(), id)
	if errors.Is(err, planning.ErrUnknownEtape) {
		h.handleNotFound(w, r)
		return
	}
	if err != nil {
		h.failPage(w, r, fmt.Errorf("lecture d'une étape : %w", err))
		return
	}

	if parseErr := r.ParseForm(); parseErr != nil {
		h.failPage(w, r, fmt.Errorf("lecture du formulaire de modification : %w", parseErr))
		return
	}

	err = h.updateEtapeFromForm(r, id)
	switch {
	case err == nil:
		h.redirectAfterPost(w, r, planningPath+"?"+paramAvis+"="+avisEtapeModifiee)
	case errors.Is(err, planning.ErrUnknownEtape):
		// Supprimée entre la relecture et l'écriture : le 404 reste le mot juste.
		h.handleNotFound(w, r)
	case errors.Is(err, planning.ErrConcurrentUpdate):
		// Quelqu'un a modifié l'étape entre-temps. La redirection recharge la
		// page avec l'état réel et l'avis qui l'explique.
		h.redirectAfterPost(w, r, planningPath+"?"+paramAvis+"="+avisPlanningModifie)
	default:
		h.rejectUpdateEtapeForm(w, r, current, err)
	}
}

// updateEtapeFromForm traduit la soumission en entrée de cas d'usage.
func (h *Handler) updateEtapeFromForm(r *http.Request, id planning.ID) error {
	retenu, dates, err := h.parseEtapeCommonFields(r)
	if err != nil {
		return err
	}
	expected, err := parseModifieLe(r)
	if err != nil {
		return err
	}

	_, err = h.planning.UpdateEtape(r.Context(), id, planning.UpdateEtapeInput{
		Name:         r.PostFormValue(fieldNom),
		Description:  r.PostFormValue(fieldDescription),
		PlannedStart: dates[0],
		PlannedEnd:   dates[1],
		DependsOn:    dependancesFrom(r),
		DevisID:      retenu.id,
		Expected:     expected,
		By:           planningActeurFrom(r),
	})

	return err
}

// rejectUpdateEtapeForm réaffiche la page de modification avec la soumission
// et le message d'échec. L'étape relue par l'appelant repose l'état de
// verrouillage des prérequis : sans lui, le réaffichage après un refus sur une
// étape démarrée montrerait des cases actives que le domaine refuserait.
func (h *Handler) rejectUpdateEtapeForm(w http.ResponseWriter, r *http.Request, current planning.Etape, cause error) {
	messageID := planningMessageID(cause)
	if messageID == "" {
		h.failPage(w, r, fmt.Errorf("modification d'une étape : %w", cause))
		return
	}

	etapes, retenus, err := h.planningFormSources(r)
	if err != nil {
		h.failPage(w, r, err)
		return
	}

	form := h.etapeFormFromRequest(r, etapes, retenus, current.ID)
	form.Action = planningElementPath(planningEtapesPath, current.ID, "")
	form.DependancesLocked = current.Statut() != planning.StatutPrevue
	form.Error = h.translate(r, messageID)

	h.render(w, r, pagePlanningEtapeModifier, http.StatusUnprocessableEntity, etapeModifierData{EtapeForm: form})
}

// --- Transitions ------------------------------------------------------------------

// handleEtapeDemarrer démarre une étape.
func (h *Handler) handleEtapeDemarrer(w http.ResponseWriter, r *http.Request) {
	h.applyPlanningTransition(w, r, planning.ErrUnknownEtape, avisEtapeDemarree,
		h.lookupEtape,
		func(ctx context.Context, id planning.ID, expected time.Time) error {
			_, err := h.planning.StartEtape(ctx, id, expected, planningActeurFrom(r))
			return err
		})
}

// handleEtapeTerminer termine une étape.
func (h *Handler) handleEtapeTerminer(w http.ResponseWriter, r *http.Request) {
	h.applyPlanningTransition(w, r, planning.ErrUnknownEtape, avisEtapeTerminee,
		h.lookupEtape,
		func(ctx context.Context, id planning.ID, expected time.Time) error {
			_, err := h.planning.FinishEtape(ctx, id, expected, planningActeurFrom(r))
			return err
		})
}

// handleJalonAtteindre marque un jalon comme atteint.
func (h *Handler) handleJalonAtteindre(w http.ResponseWriter, r *http.Request) {
	h.applyPlanningTransition(w, r, planning.ErrUnknownJalon, avisJalonAtteint,
		h.lookupJalon,
		func(ctx context.Context, id planning.ID, expected time.Time) error {
			_, err := h.planning.ReachJalon(ctx, id, expected, planningActeurFrom(r))
			return err
		})
}

// lookupEtape et lookupJalon ne vérifient que l'existence, pour que les
// transitions rendent 404 avant de juger le formulaire.
func (h *Handler) lookupEtape(ctx context.Context, id planning.ID) error {
	_, err := h.planning.Etape(ctx, id)
	return err
}

func (h *Handler) lookupJalon(ctx context.Context, id planning.ID) error {
	_, err := h.planning.Jalon(ctx, id)
	return err
}

// applyPlanningTransition exécute une transition portée par un formulaire à
// garde optimiste et ramène à la page du planning.
//
// Les transitions partagent tout sauf le cas d'usage appelé et le message
// d'issue : les écrire trois fois ferait trois endroits où se tromper de code
// de retour ou oublier une redirection. L'existence de l'élément se vérifie
// AVANT le formulaire : un élément inconnu est un 404, même quand la
// soumission est par ailleurs invalide.
func (h *Handler) applyPlanningTransition(
	w http.ResponseWriter, r *http.Request,
	notFound error, avis string,
	lookup func(context.Context, planning.ID) error,
	action func(context.Context, planning.ID, time.Time) error,
) {
	id := planning.ID(r.PathValue("id"))

	switch lookupErr := lookup(r.Context(), id); {
	case errors.Is(lookupErr, notFound):
		h.handleNotFound(w, r)
		return
	case lookupErr != nil:
		h.failPage(w, r, fmt.Errorf("lecture avant transition : %w", lookupErr))
		return
	}

	if err := r.ParseForm(); err != nil {
		h.failPage(w, r, fmt.Errorf("lecture du formulaire de transition : %w", err))
		return
	}

	expected, err := parseModifieLe(r)
	if err == nil {
		err = action(r.Context(), id, expected)
	}

	switch {
	case err == nil:
		h.redirectAfterPost(w, r, planningPath+"?"+paramAvis+"="+avis)
	case errors.Is(err, notFound):
		// Supprimé entre la relecture et l'écriture : le 404 reste le mot juste.
		h.handleNotFound(w, r)
	case errors.Is(err, planning.ErrConcurrentUpdate):
		h.redirectAfterPost(w, r, planningPath+"?"+paramAvis+"="+avisPlanningModifie)
	case planningMessageID(err) != "":
		// Le refus est de ceux que l'utilisateur comprend — prérequis non
		// terminés, étape déjà démarrée. La page se réaffiche avec le message
		// et l'état RELU : ce qui est montré est le vrai.
		h.renderPlanningIndex(w, r, http.StatusUnprocessableEntity, nil, nil,
			h.translate(r, planningMessageID(err)))
	default:
		h.failPage(w, r, fmt.Errorf("transition du planning : %w", err))
	}
}

// parseModifieLe lit la garde optimiste d'un formulaire.
func parseModifieLe(r *http.Request) (time.Time, error) {
	raw := r.PostFormValue(fieldModifieLe)
	expected, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w : %q", errHorodatageIllisible, raw)
	}

	return expected, nil
}

// --- Jalons ---------------------------------------------------------------------

// handleCreateJalon crée un jalon.
func (h *Handler) handleCreateJalon(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.failPage(w, r, fmt.Errorf("lecture du formulaire de jalon : %w", err))
		return
	}

	err := h.createJalonFromForm(r)
	if err == nil {
		h.redirectAfterPost(w, r, planningPath+"?"+paramAvis+"="+avisJalonCree)
		return
	}

	h.rejectJalonForm(w, r, err)
}

// createJalonFromForm traduit le formulaire en entrée de cas d'usage.
func (h *Handler) createJalonFromForm(r *http.Request) error {
	date, err := parseDate(r.PostFormValue(fieldDatePrevue))
	if err != nil {
		return err
	}

	_, err = h.planning.CreateJalon(r.Context(), planning.JalonInput{
		Name: r.PostFormValue(fieldNom),
		Date: date,
		By:   planningActeurFrom(r),
	})

	return err
}

// rejectJalonForm réaffiche la page avec le formulaire de jalon tel qu'il a
// été soumis et le message d'échec.
func (h *Handler) rejectJalonForm(w http.ResponseWriter, r *http.Request, cause error) {
	messageID := planningMessageID(cause)
	if messageID == "" {
		h.failPage(w, r, fmt.Errorf("création d'un jalon : %w", cause))
		return
	}

	form := h.emptyJalonForm()
	form.Nom = r.PostFormValue(fieldNom)
	form.Date = r.PostFormValue(fieldDatePrevue)
	form.Error = h.translate(r, messageID)

	h.renderPlanningIndex(w, r, http.StatusUnprocessableEntity, nil, &form, "")
}

// --- Divers ---------------------------------------------------------------------

// planningActeurFrom traduit l'identité de la requête en valeur pour le
// domaine planning — même partage que pour les devis : le domaine reçoit un
// identifiant d'acteur en simple valeur, jamais l'acteur lui-même (R1).
func planningActeurFrom(r *http.Request) planning.ActeurID {
	return planning.ActeurID(ActorFromContext(r.Context()).UserID().String())
}

// planningElementPath rend l'adresse d'une action sur un élément du planning.
func planningElementPath(base string, id planning.ID, suffix string) string {
	return base + "/" + url.PathEscape(id.String()) + suffix
}
