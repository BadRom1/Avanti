package planning

import (
	"context"
	"time"
)

// milleScale est la résolution des positions du Gantt : des millièmes entiers
// de la plage affichée. L'adapter web en fait ce qu'il veut — des pourcentages
// à une décimale, des colonnes — sans qu'aucun flottant n'existe nulle part.
const milleScale = 1000

// GanttView est le diagramme de Gantt DÉRIVÉ des étapes et des jalons, calculé
// en mémoire au moment du rendu. Rien n'en est stocké : stocker un Gantt, ce
// serait stocker une deuxième vérité sur les dates, qui divergerait de la
// première à la première replanification (docs/ARCHITECTURE.md §4).
//
// Tout y est pré-calculé en valeurs : bornes de la plage, positions en
// millièmes entiers, statuts et retards par ligne. L'adapter web n'a plus
// qu'un travail de mise en forme.
//
// Le diagramme travaille au jour près, en UTC : une barre couvre son dernier
// jour en entier (la fin est exclusive, posée au lendemain 00:00), ce qui rend
// visible une étape d'une seule journée et donne à la plage une largeur
// toujours non nulle dès qu'il y a quelque chose à dessiner.
type GanttView struct {
	// Empty dit qu'il n'y a rien à dessiner : ni étape ni jalon. Les autres
	// champs sont alors à leur valeur zéro.
	Empty bool
	// From et To sont les bornes de la plage affichée : le premier jour et le
	// lendemain du dernier jour, toutes dates confondues — prévues, réelles,
	// jalons, et aujourd'hui pour une étape en cours. Un jalon hors de la
	// plage des étapes étend la plage.
	From time.Time
	To   time.Time
	// Rows sont les lignes d'étapes, triées par début prévu puis identifiant.
	// Le tri topologique a été écarté : les prérequis d'une étape commencent
	// en pratique avant elle — c'est le sens même du planning — et un ordre
	// purement chronologique reste lisible même quand quelqu'un planifie en
	// dépit du graphe, là où un ordre topologique mélangerait les dates sans
	// prévenir.
	Rows []GanttRow
	// Jalons sont les marqueurs de jalons, triés par date puis identifiant.
	Jalons []GanttJalon
}

// GanttRow est une ligne du diagramme : une étape et ses positions.
type GanttRow struct {
	// ID et Name identifient et nomment l'étape.
	ID   ID
	Name string
	// Statut est l'état dérivé de l'étape ([Etape.Statut]).
	Statut Statut
	// EnRetard et RetardJours portent la détection de retard au jour du rendu
	// ([Etape.EnRetard], [Etape.RetardConstate]).
	EnRetard    bool
	RetardJours int
	// PreteADemarrer dit que l'étape est prévue et que tous ses prérequis sont
	// terminés : c'est la matérialisation des « candidates à la
	// parallélisation » — tout ce qui est prêt peut avancer de front.
	PreteADemarrer bool
	// PlannedFrom et PlannedTo positionnent la barre prévue, en millièmes
	// entiers de la plage, bornés à [0, 1000], avec PlannedTo > PlannedFrom.
	PlannedFrom int
	PlannedTo   int
	// Started dit qu'une barre réelle existe. ActualFrom/ActualTo la
	// positionnent alors : du début réel à la fin réelle, ou à aujourd'hui
	// pour une étape encore en cours.
	Started    bool
	ActualFrom int
	ActualTo   int
}

// GanttJalon est un marqueur de jalon sur le diagramme.
type GanttJalon struct {
	// ID et Name identifient et nomment le jalon.
	ID   ID
	Name string
	// Date est l'échéance prévue.
	Date time.Time
	// Position place le marqueur, en millièmes entiers de la plage, au début
	// du jour de l'échéance.
	Position int
	// Atteint, EnRetard et RetardJours portent l'état du jalon au jour du
	// rendu.
	Atteint     bool
	EnRetard    bool
	RetardJours int
}

// Gantt calcule la vue Gantt au jour donné, depuis les étapes et les jalons
// relus à l'instant du rendu.
func (s *Service) Gantt(ctx context.Context, today time.Time) (GanttView, error) {
	etapes, err := s.repo.ListEtapes(ctx)
	if err != nil {
		return GanttView{}, err
	}

	jalons, err := s.repo.ListJalons(ctx)
	if err != nil {
		return GanttView{}, err
	}

	return BuildGantt(etapes, jalons, today), nil
}

// BuildGantt dérive la vue Gantt d'un jeu d'étapes et de jalons. La fonction
// est pure et exportée : elle se teste sur des cas calculés à la main, sans
// dépôt ni horloge.
func BuildGantt(etapes []Etape, jalons []Jalon, today time.Time) GanttView {
	if len(etapes) == 0 && len(jalons) == 0 {
		return GanttView{Empty: true}
	}

	scale := newGanttScale(etapes, jalons, today)

	view := GanttView{From: scale.from, To: scale.to}

	done := make(map[ID]bool, len(etapes))
	for _, etape := range etapes {
		done[etape.ID] = etape.Statut() == StatutTerminee
	}

	view.Rows = make([]GanttRow, 0, len(etapes))
	for _, etape := range etapes {
		view.Rows = append(view.Rows, newGanttRow(etape, scale, done, today))
	}

	view.Jalons = make([]GanttJalon, 0, len(jalons))
	for _, jalon := range jalons {
		view.Jalons = append(view.Jalons, GanttJalon{
			ID:          jalon.ID,
			Name:        jalon.Name,
			Date:        jalon.Date,
			Position:    scale.position(dayOf(jalon.Date)),
			Atteint:     jalon.Atteint(),
			EnRetard:    jalon.EnRetard(today),
			RetardJours: jalon.RetardConstate(today),
		})
	}

	return view
}

// newGanttRow dérive la ligne d'une étape.
func newGanttRow(etape Etape, scale ganttScale, done map[ID]bool, today time.Time) GanttRow {
	row := GanttRow{
		ID:          etape.ID,
		Name:        etape.Name,
		Statut:      etape.Statut(),
		EnRetard:    etape.EnRetard(today),
		RetardJours: etape.RetardConstate(today),
	}
	row.PlannedFrom, row.PlannedTo = barSpan(
		scale.position(dayOf(etape.PlannedStart)),
		scale.position(dayAfter(etape.PlannedEnd)))

	if row.Statut == StatutPrevue {
		row.PreteADemarrer = true
		for _, dep := range etape.DependsOn {
			if !done[dep] {
				row.PreteADemarrer = false
				break
			}
		}
	}

	if !etape.ActualStart.IsZero() {
		row.Started = true
		// Une étape en cours court jusqu'à aujourd'hui : c'est ce que la barre
		// réelle raconte — le temps déjà consommé, pas une prédiction de fin.
		end := etape.ActualEnd
		if end.IsZero() {
			end = today
		}
		row.ActualFrom, row.ActualTo = barSpan(
			scale.position(dayOf(etape.ActualStart)),
			scale.position(dayAfter(end)))
	}

	return row
}

// barSpan garantit qu'une barre occupe au moins un millième de la plage, sans
// sortir de [0, 1000] : sur une plage de plus de mille jours, la journée d'une
// étape courte s'arrondit à zéro millième, et l'invariant « To > From » que la
// doc de [GanttRow] promet — et que les adapters consomment — serait rompu.
func barSpan(rawFrom, rawTo int) (from, to int) {
	from, to = rawFrom, rawTo
	if to <= from {
		from = min(from, milleScale-1)
		to = from + 1
	}

	return from, to
}

// ganttScale traduit un instant en millièmes de la plage [from, to].
type ganttScale struct {
	from time.Time
	to   time.Time
	// seconds est la largeur de la plage en secondes entières. Toujours
	// strictement positive dès qu'il y a quelque chose à dessiner, la fin
	// exclusive garantissant au moins un jour.
	seconds int64
}

// newGanttScale calcule les bornes de la plage : le plus tôt et le plus tard
// de toutes les dates concernées, la fin poussée au lendemain de son jour.
func newGanttScale(etapes []Etape, jalons []Jalon, today time.Time) ganttScale {
	var earliest, latest time.Time

	observe := func(instant time.Time) {
		if instant.IsZero() {
			return
		}
		day := dayOf(instant)
		if earliest.IsZero() || day.Before(earliest) {
			earliest = day
		}
		if latest.IsZero() || day.After(latest) {
			latest = day
		}
	}

	for _, etape := range etapes {
		observe(etape.PlannedStart)
		observe(etape.PlannedEnd)
		observe(etape.ActualStart)
		observe(etape.ActualEnd)
		if etape.Statut() == StatutEnCours {
			observe(today)
		}
	}
	for _, jalon := range jalons {
		observe(jalon.Date)
		observe(jalon.ReachedAt)
	}

	from := earliest
	to := latest.Add(24 * time.Hour)

	return ganttScale{
		from:    from,
		to:      to,
		seconds: int64(to.Sub(from) / time.Second),
	}
}

// position traduit un instant en millièmes de la plage, borné à [0, 1000].
//
// Le calcul est entièrement en arithmétique entière : des secondes entières,
// multipliées avant d'être divisées. Passer par les secondes plutôt que par
// les nanosecondes de time.Duration évite le débordement — mille fois une
// plage en nanosecondes déborderait l'int64 dès quelques mois.
func (g ganttScale) position(instant time.Time) int {
	if g.seconds <= 0 {
		return 0
	}

	offset := int64(instant.Sub(g.from) / time.Second)
	mille := offset * milleScale / g.seconds

	return int(max(0, min(mille, milleScale)))
}

// dayAfter rend le lendemain 00:00 UTC du jour d'un instant : la fin exclusive
// d'une barre qui couvre ce jour en entier.
func dayAfter(instant time.Time) time.Time {
	return dayOf(instant).Add(24 * time.Hour)
}
