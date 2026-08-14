package planning

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Repository est le port de persistance du domaine.
//
// Les implémentations sont attendues sur quatre points que le domaine ne peut
// pas vérifier lui-même :
//
//   - rendre [ErrUnknownEtape] ou [ErrUnknownJalon] (éventuellement enveloppée)
//     quand une lecture ne trouve rien — et quand une réécriture ne touche
//     aucune ligne ;
//   - exécuter les réécritures ([Repository.UpdateEtape],
//     [Repository.StartEtape], [Repository.UpdateJalon]) en réécrivant la
//     ligne entière, SOUS GARDE OPTIMISTE : la réécriture ne touche la ligne
//     que si son horodatage de modification vaut encore expected — l'état que
//     l'appelant a lu avant de transformer. Aucune ligne touchée se départage
//     en relisant : l'élément a disparu → [ErrUnknownEtape] ou
//     [ErrUnknownJalon] ; il existe → [ErrConcurrentUpdate], et l'appelant
//     relit avant de recommencer. PIÈGE : expected doit provenir d'une
//     RELECTURE, jamais de la valeur que le service a rendue à l'écriture —
//     PostgreSQL stocke les horodatages en microsecondes, l'UpdatedAt en
//     nanosecondes d'une valeur jamais repassée par la base ne correspondra
//     pas, et la garde refuserait à tort ;
//   - SÉRIALISER toute écriture d'étape — création comprise — par un verrou
//     unique du planning (l'adapter PostgreSQL prend un verrou consultatif
//     transactionnel sur une clé constante), et REJOUER sous ce verrou ce que
//     le service a vérifié hors verrou : l'existence des étapes que DependsOn
//     désigne ([ErrUnknownDependency]) et l'acyclicité du graphe via
//     [CheckAcyclic] ([ErrDependencyCycle]). Sans ce rejeu, deux éditions
//     simultanées — chacune innocente sur l'état qu'elle a lu — pourraient
//     fermer un cycle à elles deux. Un seul verrou pour tout le planning, et
//     c'est un choix : une instance porte UN chantier, quelques dizaines
//     d'étapes, et une granularité plus fine (par composante du graphe ?)
//     n'achèterait aucune concurrence utile contre une vraie occasion de se
//     tromper ;
//   - rejouer sous ce même verrou, pour [Repository.StartEtape], la
//     vérification « tous les prérequis sont terminés »
//     ([ErrPrerequisitesNotDone]) : c'est ce qui sérialise le démarrage d'une
//     étape avec les écritures de ses prérequis, et rend l'invariant central
//     du domaine incontournable même sous concurrence.
//
// Tout le reste des erreurs remonte tel quel et sera traité comme une panne.
type Repository interface {
	// CreateEtape insère une étape, dépendances comprises, sous le verrou du
	// planning (voir le contrat du port).
	CreateEtape(ctx context.Context, etape Etape) error
	// EtapeByID lit une étape par son identifiant, dépendances comprises.
	EtapeByID(ctx context.Context, id ID) (Etape, error)
	// ListEtapes renvoie toutes les étapes, triées par début prévu puis
	// identifiant — l'ordre du Gantt et des listes.
	ListEtapes(ctx context.Context) ([]Etape, error)
	// UpdateEtape réécrit une étape entière — dépendances et modifie_le
	// compris — si la ligne porte encore l'horodatage de modification
	// expected, sous le verrou du planning et avec rejeu des vérifications de
	// graphe (voir le contrat du port).
	UpdateEtape(ctx context.Context, etape Etape, expected time.Time) error
	// StartEtape réécrit une étape que [Etape.Start] vient de démarrer, sous
	// la même garde optimiste que [Repository.UpdateEtape], et REJOUE sous le
	// verrou la vérification que tous ses prérequis sont terminés.
	StartEtape(ctx context.Context, etape Etape, expected time.Time) error

	// CreateJalon insère un jalon.
	CreateJalon(ctx context.Context, jalon Jalon) error
	// JalonByID lit un jalon par son identifiant.
	JalonByID(ctx context.Context, id ID) (Jalon, error)
	// ListJalons renvoie tous les jalons, triés par date prévue puis
	// identifiant.
	ListJalons(ctx context.Context) ([]Jalon, error)
	// UpdateJalon réécrit un jalon entier, sous la même garde optimiste que
	// [Repository.UpdateEtape].
	UpdateJalon(ctx context.Context, jalon Jalon, expected time.Time) error
}

// Service porte les cas d'usage du domaine.
//
// Il ne journalise pas et ne lit aucune variable d'environnement : ce qu'il
// lui faut arrive par [ServiceOptions], conformément à R1 de
// docs/ARCHITECTURE.md.
type Service struct {
	repo  Repository
	clock func() time.Time
	newID func() (ID, error)
}

// ServiceOptions rassemble les dépendances du service.
type ServiceOptions struct {
	// Repo est le port de persistance. Obligatoire.
	Repo Repository
	// Clock donne l'heure courante. Nil signifie time.Now.
	Clock func() time.Time
	// NewID tire un identifiant. Nil signifie [NewID].
	NewID func() (ID, error)
}

// NewService construit le service.
func NewService(opts ServiceOptions) (*Service, error) {
	if opts.Repo == nil {
		return nil, errors.New("planning : dépôt manquant")
	}

	service := &Service{repo: opts.Repo, clock: opts.Clock, newID: opts.NewID}
	if service.clock == nil {
		service.clock = time.Now
	}
	if service.newID == nil {
		service.newID = NewID
	}

	return service, nil
}

// EtapeInput est ce qu'il faut fournir pour créer une étape.
type EtapeInput struct {
	// Name est le nom du lot de travaux. Obligatoire.
	Name string
	// Description complète le nom. Facultative.
	Description string
	// PlannedStart et PlannedEnd sont les dates prévues. Obligatoires, avec
	// PlannedEnd ≥ PlannedStart.
	PlannedStart time.Time
	PlannedEnd   time.Time
	// DependsOn liste les prérequis : des étapes existantes, sans doublon ni
	// auto-référence, et sans fermer de cycle.
	DependsOn []ID
	// DevisID rattache l'étape à un devis retenu, par identifiant faible.
	// Vide pour une étape sans financement rattaché. C'est l'adapter appelant
	// qui vérifie que le devis existe et qu'il est retenu — le domaine ne sait
	// pas le lire (R2).
	DevisID string
	// By est l'acteur qui crée. Obligatoire.
	By ActeurID
}

// CreateEtape crée une étape et renvoie ce qui a été stocké.
//
// Les dépendances sont vérifiées deux fois, et c'est voulu : le service relit
// les étapes existantes pour refuser tôt un prérequis inconnu ou un cycle,
// avec un message clair — mais cette lecture ne garantit rien face à deux
// éditions simultanées. C'est le [Repository] qui fait foi, en rejouant les
// deux vérifications sous le verrou du planning.
func (s *Service) CreateEtape(ctx context.Context, in EtapeInput) (Etape, error) {
	etape, err := s.buildEtape(in)
	if err != nil {
		return Etape{}, err
	}

	if len(etape.DependsOn) > 0 {
		existing, listErr := s.repo.ListEtapes(ctx)
		if listErr != nil {
			return Etape{}, listErr
		}
		if checkErr := checkDependencies(etape, existing); checkErr != nil {
			return Etape{}, checkErr
		}
	}

	if writeErr := s.repo.CreateEtape(ctx, etape); writeErr != nil {
		return Etape{}, writeErr
	}

	return etape, nil
}

// buildEtape valide et assemble l'étape. Séparer la construction de l'écriture
// garde chacune lisible, et rend la validation testable sans dépôt.
func (s *Service) buildEtape(in EtapeInput) (Etape, error) {
	name, err := normalizeName(in.Name)
	if err != nil {
		return Etape{}, err
	}
	description, err := normalizeDescription(in.Description)
	if err != nil {
		return Etape{}, err
	}
	devisID, err := normalizeDevisID(in.DevisID)
	if err != nil {
		return Etape{}, err
	}
	if rangeErr := checkPlannedRange(in.PlannedStart, in.PlannedEnd); rangeErr != nil {
		return Etape{}, rangeErr
	}
	if in.By == "" {
		return Etape{}, ErrMissingActor
	}

	id, err := s.newID()
	if err != nil {
		return Etape{}, err
	}

	dependsOn, err := normalizeDependsOn(id, in.DependsOn)
	if err != nil {
		return Etape{}, err
	}

	now := s.clock().UTC()

	return Etape{
		ID:           id,
		Name:         name,
		Description:  description,
		PlannedStart: in.PlannedStart.UTC(),
		PlannedEnd:   in.PlannedEnd.UTC(),
		DependsOn:    dependsOn,
		DevisID:      devisID,
		CreatedBy:    in.By,
		CreatedAt:    now,
		UpdatedAt:    now,
	}, nil
}

// checkDependencies vérifie ce que seule la vue d'ensemble permet : les
// prérequis désignent des étapes existantes, et le graphe qui en résulte reste
// acyclique. others est l'état courant ; etape le remplace ou s'y ajoute.
func checkDependencies(etape Etape, others []Etape) error {
	known := make(map[ID]bool, len(others)+1)
	graph := make([]Etape, 0, len(others)+1)
	for _, other := range others {
		if other.ID == etape.ID {
			continue
		}
		known[other.ID] = true
		graph = append(graph, other)
	}
	graph = append(graph, etape)

	for _, dep := range etape.DependsOn {
		if !known[dep] {
			return fmt.Errorf("%w : %s", ErrUnknownDependency, dep)
		}
	}

	return CheckAcyclic(graph)
}

// Etapes renvoie toutes les étapes, triées par début prévu puis identifiant.
func (s *Service) Etapes(ctx context.Context) ([]Etape, error) {
	return s.repo.ListEtapes(ctx)
}

// Etape lit une étape par son identifiant.
func (s *Service) Etape(ctx context.Context, id ID) (Etape, error) {
	return s.repo.EtapeByID(ctx, id)
}

// UpdateEtapeInput est ce qu'il faut fournir pour modifier une étape. Tous les
// champs remplacent l'existant : c'est le formulaire entier qui revient, pas
// un correctif champ par champ.
type UpdateEtapeInput struct {
	// Name, Description, PlannedStart, PlannedEnd, DependsOn et DevisID : les
	// mêmes règles qu'à la création ([EtapeInput]).
	Name         string
	Description  string
	PlannedStart time.Time
	PlannedEnd   time.Time
	DependsOn    []ID
	DevisID      string
	// Expected est l'horodatage de modification que l'appelant a lu — celui
	// que le formulaire portait. La garde optimiste refuse la réécriture si
	// l'étape a changé depuis ([ErrConcurrentUpdate]).
	Expected time.Time
	// By est l'acteur qui modifie. Obligatoire.
	By ActeurID
}

// UpdateEtape modifie une étape et renvoie ce qui a été stocké.
//
// Tout reste modifiable, même sur une étape terminée — corriger un nom ou une
// date prévue mal saisie ne réécrit pas l'histoire, les dates réelles ne
// bougent pas. UNE exception : les prérequis d'une étape démarrée. Ils ont
// déjà joué leur rôle de garde au démarrage — les changer après coup ne
// retiendrait plus rien et raconterait un autre graphe que celui qui a
// autorisé le démarrage ([ErrDependenciesLocked]). Les resoumettre à
// l'identique reste permis : un formulaire renvoie tout ce qu'il affiche.
func (s *Service) UpdateEtape(ctx context.Context, id ID, in UpdateEtapeInput) (Etape, error) {
	current, err := s.guardedEtape(ctx, id, in.Expected, in.By)
	if err != nil {
		return Etape{}, err
	}

	updated, err := applyEtapeUpdate(current, in, s.clock().UTC())
	if err != nil {
		return Etape{}, err
	}

	if len(updated.DependsOn) > 0 && !sameIDs(updated.DependsOn, current.DependsOn) {
		existing, listErr := s.repo.ListEtapes(ctx)
		if listErr != nil {
			return Etape{}, listErr
		}
		if checkErr := checkDependencies(updated, existing); checkErr != nil {
			return Etape{}, checkErr
		}
	}

	if writeErr := s.repo.UpdateEtape(ctx, updated, in.Expected); writeErr != nil {
		return Etape{}, writeErr
	}

	return updated, nil
}

// applyEtapeUpdate valide les champs soumis et les applique à l'étape lue.
func applyEtapeUpdate(current Etape, in UpdateEtapeInput, now time.Time) (Etape, error) {
	name, err := normalizeName(in.Name)
	if err != nil {
		return Etape{}, err
	}
	description, err := normalizeDescription(in.Description)
	if err != nil {
		return Etape{}, err
	}
	devisID, err := normalizeDevisID(in.DevisID)
	if err != nil {
		return Etape{}, err
	}
	if rangeErr := checkPlannedRange(in.PlannedStart, in.PlannedEnd); rangeErr != nil {
		return Etape{}, rangeErr
	}
	dependsOn, err := normalizeDependsOn(current.ID, in.DependsOn)
	if err != nil {
		return Etape{}, err
	}
	if current.Statut() != StatutPrevue && !sameIDs(dependsOn, current.DependsOn) {
		return Etape{}, fmt.Errorf("%w : %s", ErrDependenciesLocked, current.Name)
	}

	updated := current
	updated.Name = name
	updated.Description = description
	updated.PlannedStart = in.PlannedStart.UTC()
	updated.PlannedEnd = in.PlannedEnd.UTC()
	updated.DependsOn = dependsOn
	updated.DevisID = devisID
	updated.UpdatedAt = now

	return updated, nil
}

// sameIDs compare deux listes d'identifiants comme des ensembles : l'ordre
// d'un formulaire n'est pas une modification.
func sameIDs(a, b []ID) bool {
	if len(a) != len(b) {
		return false
	}

	left := slices.Clone(a)
	right := slices.Clone(b)
	slices.Sort(left)
	slices.Sort(right)

	return slices.Equal(left, right)
}

// StartEtape démarre une étape, sous l'invariant central du domaine : tous ses
// prérequis doivent être terminés.
//
// Le service relit les prérequis et refuse tôt, en nommant les étapes
// bloquantes — mais cette lecture ne garantit rien face aux écritures
// simultanées : c'est [Repository.StartEtape] qui fait foi, en rejouant la
// vérification sous le verrou du planning.
func (s *Service) StartEtape(ctx context.Context, id ID, expected time.Time, by ActeurID) (Etape, error) {
	current, err := s.guardedEtape(ctx, id, expected, by)
	if err != nil {
		return Etape{}, err
	}

	if checkErr := s.checkPrerequisitesDone(ctx, current); checkErr != nil {
		return Etape{}, checkErr
	}

	started, err := current.Start(s.clock().UTC())
	if err != nil {
		return Etape{}, err
	}

	if writeErr := s.repo.StartEtape(ctx, started, expected); writeErr != nil {
		return Etape{}, writeErr
	}

	return started, nil
}

// guardedEtape est l'entrée commune des cas d'usage qui réécrivent une
// étape : l'action est signée, l'étape relue, et la garde optimiste appliquée
// tôt — le formulaire a été rempli sur un état qui n'existe plus, il se
// recharge avant d'écraser. Le dépôt rejouera la même garde à l'écriture ;
// celle-ci ne sert qu'à répondre vite avec le bon mot.
func (s *Service) guardedEtape(ctx context.Context, id ID, expected time.Time, by ActeurID) (Etape, error) {
	if by == "" {
		return Etape{}, ErrMissingActor
	}

	current, err := s.repo.EtapeByID(ctx, id)
	if err != nil {
		return Etape{}, err
	}
	if !current.UpdatedAt.Equal(expected) {
		return Etape{}, fmt.Errorf("%w : étape %s", ErrConcurrentUpdate, id)
	}

	return current, nil
}

// checkPrerequisitesDone relit les prérequis de l'étape et refuse si l'un
// d'eux n'est pas terminé, en les nommant tous — la personne voit d'un coup ce
// qui retient le démarrage.
func (s *Service) checkPrerequisitesDone(ctx context.Context, etape Etape) error {
	var blocking []string
	for _, dep := range etape.DependsOn {
		prerequisite, err := s.repo.EtapeByID(ctx, dep)
		if err != nil {
			return err
		}
		if prerequisite.Statut() != StatutTerminee {
			blocking = append(blocking, prerequisite.Name)
		}
	}

	if len(blocking) > 0 {
		return fmt.Errorf("%w : %s", ErrPrerequisitesNotDone, strings.Join(blocking, ", "))
	}

	return nil
}

// FinishEtape termine une étape. C'est l'entité qui dit ce qu'elle autorise —
// pas de fin sans début, pas de fin avant le début — et la garde optimiste qui
// départage deux transitions simultanées.
func (s *Service) FinishEtape(ctx context.Context, id ID, expected time.Time, by ActeurID) (Etape, error) {
	current, err := s.guardedEtape(ctx, id, expected, by)
	if err != nil {
		return Etape{}, err
	}

	finished, err := current.Finish(s.clock().UTC())
	if err != nil {
		return Etape{}, err
	}

	if writeErr := s.repo.UpdateEtape(ctx, finished, expected); writeErr != nil {
		return Etape{}, writeErr
	}

	return finished, nil
}

// JalonInput est ce qu'il faut fournir pour créer un jalon.
type JalonInput struct {
	// Name est l'intitulé du jalon. Obligatoire.
	Name string
	// Date est l'échéance prévue. Obligatoire.
	Date time.Time
	// By est l'acteur qui crée. Obligatoire.
	By ActeurID
}

// CreateJalon crée un jalon et renvoie ce qui a été stocké.
func (s *Service) CreateJalon(ctx context.Context, in JalonInput) (Jalon, error) {
	name, err := normalizeName(in.Name)
	if err != nil {
		return Jalon{}, err
	}
	if in.Date.IsZero() {
		return Jalon{}, fmt.Errorf("%w : date du jalon", ErrMissingDate)
	}
	if in.By == "" {
		return Jalon{}, ErrMissingActor
	}

	id, err := s.newID()
	if err != nil {
		return Jalon{}, err
	}

	now := s.clock().UTC()

	jalon := Jalon{
		ID:        id,
		Name:      name,
		Date:      in.Date.UTC(),
		CreatedBy: in.By,
		CreatedAt: now,
		UpdatedAt: now,
	}

	if writeErr := s.repo.CreateJalon(ctx, jalon); writeErr != nil {
		return Jalon{}, writeErr
	}

	return jalon, nil
}

// Jalons renvoie tous les jalons, triés par date prévue.
func (s *Service) Jalons(ctx context.Context) ([]Jalon, error) {
	return s.repo.ListJalons(ctx)
}

// Jalon lit un jalon par son identifiant.
func (s *Service) Jalon(ctx context.Context, id ID) (Jalon, error) {
	return s.repo.JalonByID(ctx, id)
}

// ReachJalon marque un jalon comme atteint, sous la même garde optimiste que
// les transitions d'étapes.
func (s *Service) ReachJalon(ctx context.Context, id ID, expected time.Time, by ActeurID) (Jalon, error) {
	if by == "" {
		return Jalon{}, ErrMissingActor
	}

	current, err := s.repo.JalonByID(ctx, id)
	if err != nil {
		return Jalon{}, err
	}
	if !current.UpdatedAt.Equal(expected) {
		// Même refus tôt que pour les étapes : le formulaire a été rempli sur
		// un état qui n'existe plus, il se recharge avant d'écraser.
		return Jalon{}, fmt.Errorf("%w : jalon %s", ErrConcurrentUpdate, id)
	}

	reached, err := current.Reach(s.clock().UTC())
	if err != nil {
		return Jalon{}, err
	}

	if writeErr := s.repo.UpdateJalon(ctx, reached, expected); writeErr != nil {
		return Jalon{}, writeErr
	}

	return reached, nil
}
