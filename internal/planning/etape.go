package planning

import (
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// Bornes des textes saisis. Elles ne défendent pas un format, elles bornent ce
// qu'une saisie peut faire stocker : un champ de formulaire n'a pas de limite
// naturelle, une colonne si.
const (
	maxNameLength        = 120
	maxDescriptionLength = 2000
	// maxDevisIDLength borne la référence faible vers un devis. Les
	// identifiants réels sont des UUID de 36 caractères ; la borne n'est pas là
	// pour eux mais pour qu'un POST forgé ne stocke pas un roman dans une
	// colonne de référence.
	maxDevisIDLength = 255
	// maxDependencies borne les prérequis d'une étape. Un chantier réel en
	// compte quelques-uns ; cinquante est déjà absurde, mais la borne n'est pas
	// là pour le cas réel — un POST forgé n'a pas de limite naturelle, et le
	// parcours du graphe n'a pas à encaisser des listes arbitraires.
	maxDependencies = 50
)

// Statut est l'état d'avancement d'une étape.
//
// Les valeurs sont en français : dérivées à l'affichage, filtrables telles
// quelles. Elles ne sont JAMAIS stockées — voir [Etape.Statut].
type Statut string

// Les trois états d'une étape, dans l'ordre du cycle de vie.
const (
	// StatutPrevue est l'état de naissance : rien n'a encore commencé.
	StatutPrevue Statut = "prevue"
	// StatutEnCours dit que l'étape est démarrée et pas encore terminée.
	StatutEnCours Statut = "en_cours"
	// StatutTerminee dit que l'étape est finie. C'est l'état qui libère les
	// étapes qui en dépendent.
	StatutTerminee Statut = "terminee"
)

// String rend le statut tel qu'il s'affiche et se filtre.
func (s Statut) String() string {
	return string(s)
}

// Etape est un lot de travaux ordonnancé : des dates prévues, des dates
// réelles quand le chantier avance, et des prérequis qui la retiennent tant
// qu'ils ne sont pas terminés.
//
// L'entité se manipule par valeur et ses transitions rendent une nouvelle
// Etape plutôt que de muter le récepteur : une étape passée dans une fonction
// ne peut pas revenir changée, et l'appelant décide explicitement de ce qu'il
// persiste.
type Etape struct {
	// ID identifie l'étape.
	ID ID
	// Name est le nom du lot de travaux affiché — « Charpente », « Réseau
	// électrique ». Obligatoire.
	Name string
	// Description complète le nom. Facultative.
	Description string
	// PlannedStart et PlannedEnd sont les dates prévues, obligatoires, en UTC,
	// avec PlannedEnd ≥ PlannedStart. Ce sont elles que le Gantt dessine et que
	// la détection de retard compare au réel.
	PlannedStart time.Time
	PlannedEnd   time.Time
	// ActualStart et ActualEnd sont les dates réelles. La valeur zéro signifie
	// « pas encore » — le même modèle que les horodatages du domaine finance.
	ActualStart time.Time
	ActualEnd   time.Time
	// DependsOn liste les prérequis de l'étape : les étapes qui doivent être
	// terminées avant qu'elle démarre. Sans doublon ni auto-référence, et sans
	// cycle à l'échelle du graphe — voir [CheckAcyclic].
	DependsOn []ID
	// DevisID rattache l'étape au devis retenu qui la finance, par identifiant
	// faible (R2 de docs/ARCHITECTURE.md) : une simple chaîne, jamais le type
	// du domaine devis. Vide, l'étape n'est financée par aucun lot engagé —
	// auto-construction, démarches.
	DevisID string
	// CreatedBy est l'acteur qui a créé l'étape.
	CreatedBy ActeurID
	// CreatedAt est la date de création dans Avanti.
	CreatedAt time.Time
	// UpdatedAt est la date de la dernière modification. C'est elle que la
	// garde optimiste du [Repository] compare.
	UpdatedAt time.Time
}

// Statut dérive l'état d'avancement des dates réelles.
//
// Le statut n'est pas stocké, et c'est une décision : un statut stocké peut
// mentir sur les dates — une colonne « terminee » sans fin réelle, une
// « prevue » qui a pourtant démarré — et chaque écriture devrait alors tenir
// deux vérités d'accord. Le statut dérivé, lui, ne peut pas mentir : il EST
// les dates, relues au moment où on l'affiche.
func (e Etape) Statut() Statut {
	switch {
	case !e.ActualEnd.IsZero():
		return StatutTerminee
	case !e.ActualStart.IsZero():
		return StatutEnCours
	default:
		return StatutPrevue
	}
}

// EnRetard dit si l'étape est en retard au jour donné : non démarrée alors que
// son début prévu est passé, ou non terminée alors que sa fin prévue l'est.
//
// Une étape terminée n'est jamais « en retard » : le retard est un écart entre
// le prévu et un présent qui court encore, et une étape finie n'a plus de
// présent — le retard qu'elle a pu prendre est un fait du passé, lisible dans
// ses dates réelles, pas une alerte à afficher.
//
// La comparaison se fait au jour près, en UTC : le jour même n'est PAS un
// retard — une étape prévue pour aujourd'hui a encore toute la journée.
func (e Etape) EnRetard(today time.Time) bool {
	switch e.Statut() {
	case StatutTerminee:
		return false
	case StatutEnCours:
		return dayOf(e.PlannedEnd).Before(dayOf(today))
	default:
		return dayOf(e.PlannedStart).Before(dayOf(today)) || dayOf(e.PlannedEnd).Before(dayOf(today))
	}
}

// RetardConstate rend le nombre de jours de retard au jour donné, pour
// l'affichage — zéro quand [Etape.EnRetard] est faux.
//
// Le calcul est en arithmétique entière sur des durées : les instants sont
// tronqués au jour UTC, et la différence se divise en jours entiers sans
// jamais passer par un flottant.
func (e Etape) RetardConstate(today time.Time) int {
	if !e.EnRetard(today) {
		return 0
	}

	// Une étape non démarrée est en retard sur son début, une étape en cours
	// sur sa fin ; la plus contraignante des deux références est la plus
	// ancienne dépassée.
	reference := e.PlannedEnd
	if e.Statut() == StatutPrevue && dayOf(e.PlannedStart).Before(dayOf(today)) {
		reference = e.PlannedStart
	}

	return daysBetween(reference, today)
}

// Start démarre l'étape à la date donnée.
//
// La transition ne va que dans un sens : un démarrage ne se reprend pas. Le
// refus ci-dessous attrape le double démarrage séquentiel ; deux démarrages
// réellement simultanés sont départagés par la garde optimiste du
// [Repository] (voir ErrConcurrentUpdate) : le second n'écrit pas.
//
// La règle « pas de démarrage avant les prérequis terminés » ne se vérifie
// pas ici : l'entité ne voit pas ses sœurs. C'est le [Service] qui relit les
// prérequis et refuse, et le [Repository] qui rejoue cette vérification sous
// verrou.
func (e Etape) Start(at time.Time) (Etape, error) {
	if at.IsZero() {
		return Etape{}, fmt.Errorf("%w : date de démarrage", ErrMissingDate)
	}
	if !e.ActualStart.IsZero() {
		return Etape{}, fmt.Errorf("%w : %s", ErrEtapeAlreadyStarted, e.Name)
	}

	started := e
	started.ActualStart = at.UTC()
	started.UpdatedAt = at.UTC()

	return started, nil
}

// Finish termine l'étape à la date donnée. Refuse une étape non démarrée, déjà
// terminée, ou une fin antérieure au début réel.
func (e Etape) Finish(at time.Time) (Etape, error) {
	switch {
	case at.IsZero():
		return Etape{}, fmt.Errorf("%w : date de fin", ErrMissingDate)
	case e.ActualStart.IsZero():
		return Etape{}, fmt.Errorf("%w : %s", ErrEtapeNotStarted, e.Name)
	case !e.ActualEnd.IsZero():
		return Etape{}, fmt.Errorf("%w : %s", ErrEtapeAlreadyFinished, e.Name)
	case at.UTC().Before(e.ActualStart):
		return Etape{}, fmt.Errorf("%w : %s", ErrFinishBeforeStart, e.Name)
	}

	finished := e
	finished.ActualEnd = at.UTC()
	finished.UpdatedAt = at.UTC()

	return finished, nil
}

// dayOf tronque un instant à son jour UTC. C'est la granularité de tout le
// planning : les dates viennent de champs <input type="date">, et comparer
// plus fin qu'au jour inventerait une précision que personne n'a saisie.
func dayOf(instant time.Time) time.Time {
	return instant.UTC().Truncate(24 * time.Hour)
}

// daysBetween compte les jours entiers entre deux instants, en arithmétique
// entière : time.Duration est un entier de nanosecondes, la division par un
// jour reste entière.
func daysBetween(from, to time.Time) int {
	return int(dayOf(to).Sub(dayOf(from)) / (24 * time.Hour))
}

// normalizeName met un nom d'étape ou de jalon sous sa forme canonique et
// refuse un nom vide. Les suites de blancs sont réduites, comme partout
// ailleurs : deux saisies du même lot doivent se lire pareil sur le Gantt.
func normalizeName(raw string) (string, error) {
	name := strings.Join(strings.Fields(raw), " ")
	if name == "" {
		return "", ErrEmptyName
	}
	if utf8.RuneCountInString(name) > maxNameLength {
		return "", fmt.Errorf("%w : nom de plus de %d caractères", ErrTextTooLong, maxNameLength)
	}

	return name, nil
}

// normalizeDescription borne la description sans en changer la mise en forme :
// les retours à la ligne font partie de ce qui a été saisi. Seuls les blancs
// de bordure partent.
func normalizeDescription(raw string) (string, error) {
	description := strings.TrimSpace(raw)
	if utf8.RuneCountInString(description) > maxDescriptionLength {
		return "", fmt.Errorf("%w : description de plus de %d caractères", ErrTextTooLong, maxDescriptionLength)
	}

	return description, nil
}

// normalizeDevisID nettoie une référence faible de devis. Vide reste vide —
// l'étape n'est financée par aucun lot — et le domaine ne vérifie pas que
// l'identifiant désigne quelque chose : il ne connaît pas le domaine devis
// (R2), il borne seulement ce qui se stocke.
func normalizeDevisID(raw string) (string, error) {
	devisID := strings.TrimSpace(raw)
	if utf8.RuneCountInString(devisID) > maxDevisIDLength {
		return "", fmt.Errorf("%w : plus de %d caractères", ErrInvalidDevisID, maxDevisIDLength)
	}

	return devisID, nil
}

// normalizeDependsOn vérifie la partie LOCALE des prérequis : bornés, sans
// doublon ni auto-référence. L'existence des étapes désignées et l'acyclicité
// du graphe demandent de voir les autres étapes — c'est le travail du
// [Service], rejoué par le [Repository] sous verrou.
func normalizeDependsOn(self ID, dependsOn []ID) ([]ID, error) {
	if len(dependsOn) == 0 {
		return nil, nil
	}
	if len(dependsOn) > maxDependencies {
		return nil, fmt.Errorf("%w : plus de %d prérequis", ErrTooManyDependencies, maxDependencies)
	}

	seen := make(map[ID]bool, len(dependsOn))
	deps := make([]ID, 0, len(dependsOn))
	for _, dep := range dependsOn {
		if dep == self {
			return nil, fmt.Errorf("%w : %s", ErrSelfDependency, self)
		}
		if seen[dep] {
			return nil, fmt.Errorf("%w : %s", ErrDuplicateDependency, dep)
		}
		seen[dep] = true
		deps = append(deps, dep)
	}

	return deps, nil
}

// checkPlannedRange vérifie les dates prévues : présentes, et dans l'ordre.
// L'égalité est acceptée — un lot d'une journée commence et finit le même
// jour.
func checkPlannedRange(start, end time.Time) error {
	if start.IsZero() || end.IsZero() {
		return fmt.Errorf("%w : dates prévues", ErrMissingDate)
	}
	if end.UTC().Before(start.UTC()) {
		return ErrInvalidPlannedRange
	}

	return nil
}

// CheckAcyclic vérifie que les dépendances des étapes données ne forment aucun
// cycle, et rend [ErrDependencyCycle] — en nommant un cycle trouvé — sinon.
//
// La fonction est PURE et exportée : le [Service] l'applique à la création et
// à la modification des dépendances, et l'adapter PostgreSQL la REJOUE sur
// l'état relu sous verrou dans la transaction d'écriture — c'est ce rejeu qui
// fait foi, deux éditions simultanées ne pouvant pas fabriquer un cycle à
// elles deux.
//
// Le parcours est un DFS trois couleurs : blanc (jamais visité), gris (en
// cours de visite — le revoir, c'est boucler), noir (visite finie, aucun cycle
// par là). Un prérequis qui ne désigne aucune étape de la liste est ignoré :
// l'existence des dépendances est une autre vérification, avec sa propre
// erreur ([ErrUnknownDependency]).
func CheckAcyclic(etapes []Etape) error {
	const (
		white = iota
		grey
		black
	)

	byID := make(map[ID]Etape, len(etapes))
	for _, etape := range etapes {
		byID[etape.ID] = etape
	}

	colors := make(map[ID]int, len(etapes))

	// Le parcours est récursif, avec le chemin courant porté explicitement :
	// un chantier ne compte que quelques dizaines d'étapes, et ce chemin rend
	// le cycle nommable dans le message sans second parcours.
	var visit func(id ID, trail []ID) error
	visit = func(id ID, trail []ID) error {
		colors[id] = grey
		trail = append(trail, id)

		for _, dep := range byID[id].DependsOn {
			if _, known := byID[dep]; !known {
				continue
			}
			switch colors[dep] {
			case grey:
				return fmt.Errorf("%w : %s", ErrDependencyCycle, cycleLabel(trail, dep, byID))
			case white:
				if err := visit(dep, trail); err != nil {
					return err
				}
			}
		}

		colors[id] = black

		return nil
	}

	for _, etape := range etapes {
		if colors[etape.ID] == white {
			if err := visit(etape.ID, nil); err != nil {
				return err
			}
		}
	}

	return nil
}

// cycleLabel met en mots le cycle trouvé : la portion du chemin qui boucle,
// nommée par les étapes, refermée sur son point de départ.
func cycleLabel(trail []ID, loop ID, byID map[ID]Etape) string {
	start := slices.Index(trail, loop)
	if start < 0 {
		start = 0
	}

	names := make([]string, 0, len(trail)-start+1)
	for _, id := range trail[start:] {
		names = append(names, byID[id].Name)
	}
	names = append(names, byID[loop].Name)

	return strings.Join(names, " → ")
}
